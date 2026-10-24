package identity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/internal/shared"
)

// SecretStore contains holder-local values only. Neither records nor their hashes
// belong in replicated state, command payloads, logs or diagnostics.
type SecretStore struct {
	Root   string
	NodeID domain.ID
	mu     sync.Mutex
}

type stagedSecret struct {
	Ingress      engine.SecretIngress
	Stage        domain.SecretStaging
	Value        []byte
	CredentialID domain.ID
	Expired      bool
}

func (s *SecretStore) root() (*os.Root, error) {
	path := filepath.Join(s.Root, "credentials")
	if !filepath.IsAbs(s.Root) {
		return nil, errors.New("absolute credential state directory required")
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	return os.OpenRoot(path)
}

func secretBytes(r io.Reader) ([]byte, error) {
	value, err := io.ReadAll(io.LimitReader(r, domain.CommandPayloadLimit+1))
	if err != nil || len(value) == 0 || len(value) > domain.CommandPayloadLimit {
		clear(value)
		return nil, errors.New("credential value unavailable or outside size bounds")
	}
	return value, nil
}

func (s *SecretStore) Stage(ctx context.Context, in engine.SecretIngress, r io.Reader) (domain.SecretStaging, error) {
	if err := ctx.Err(); err != nil {
		return domain.SecretStaging{}, err
	}
	if in.CommandID == "" || in.HolderID != s.NodeID || in.Name == "" || in.Kind == "" {
		return domain.SecretStaging{}, errors.New("invalid secret ingress")
	}
	value, err := secretBytes(r)
	if err != nil {
		return domain.SecretStaging{}, err
	}
	defer clear(value)
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.root()
	if err != nil {
		return domain.SecretStaging{}, err
	}
	defer root.Close()
	ref := shared.ID("stage_", in.CommandID)
	path := "staging/" + shared.Filename(ref)
	old, err := readStaged(root, path)
	if err == nil {
		defer clear(old.Value)
		if old.Ingress != in {
			return domain.SecretStaging{}, secretConflict()
		}
		if old.CredentialID == "" && (old.Expired || time.Now().After(old.Stage.ExpiresAt)) {
			return domain.SecretStaging{}, &domain.Error{Code: "STAGING_EXPIRED", Message: "Staging expired; use a new command ID"}
		}
		prior := old.Value
		if old.CredentialID != "" {
			prior, err = root.ReadFile(shared.Filename(old.CredentialID))
			defer clear(prior)
			if err != nil {
				return domain.SecretStaging{}, errors.New("accepted credential is no longer available")
			}
		}
		if sha256.Sum256(prior) != sha256.Sum256(value) {
			return domain.SecretStaging{}, secretConflict()
		}
		return old.Stage, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return domain.SecretStaging{}, err
	}
	// Bound retained private staging/acceptance records before writing a new ID.
	if directory, openErr := root.Open("staging"); openErr == nil {
		entries, readErr := directory.ReadDir(5001)
		directory.Close()
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return domain.SecretStaging{}, readErr
		}
		if len(entries) >= 5000 {
			return domain.SecretStaging{}, &domain.Error{Code: "REQUEST_LIMIT", Message: "Private credential staging capacity reached"}
		}
	} else if !errors.Is(openErr, os.ErrNotExist) {
		return domain.SecretStaging{}, openErr
	}
	stage := domain.SecretStaging{Reference: ref, OwnerNodeID: s.NodeID, CommandID: in.CommandID, ExpiresAt: time.Now().UTC().Add(time.Hour)}
	data, err := json.Marshal(stagedSecret{Ingress: in, Stage: stage, Value: value})
	if err != nil {
		return domain.SecretStaging{}, err
	}
	defer clear(data)
	return stage, writeFile(root, path, data)
}

func readStaged(root *os.Root, path string) (stagedSecret, error) {
	data, err := root.ReadFile(path)
	if err != nil {
		return stagedSecret{}, err
	}
	defer clear(data)
	var record stagedSecret
	if json.Unmarshal(data, &record) != nil {
		clear(record.Value)
		return stagedSecret{}, errors.New("invalid private staging record")
	}
	return record, nil
}

func secretConflict() error {
	return &domain.Error{Code: "IDEMPOTENCY_CONFLICT", Message: "Import command ID already used"}
}

// Inspect returns public staging metadata only.
func (s *SecretStore) Inspect(ctx context.Context, ref domain.ID) (engine.SecretIngress, domain.SecretStaging, error) {
	if err := ctx.Err(); err != nil {
		return engine.SecretIngress{}, domain.SecretStaging{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.root()
	if err != nil {
		return engine.SecretIngress{}, domain.SecretStaging{}, err
	}
	defer root.Close()
	record, err := readStaged(root, "staging/"+shared.Filename(ref))
	clear(record.Value)
	return record.Ingress, record.Stage, err
}

// Consume and expiry share the holder lock; expired bytes can never be promoted.
func (s *SecretStore) Consume(ctx context.Context, ref, id domain.ID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if ref == "" || id == "" {
		return errors.New("credential and staging IDs required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.root()
	if err != nil {
		return err
	}
	defer root.Close()
	path := "staging/" + shared.Filename(ref)
	record, err := readStaged(root, path)
	if err != nil {
		return err
	}
	defer clear(record.Value)
	if record.CredentialID != "" {
		if record.CredentialID != id {
			return secretConflict()
		}
		_, err := root.Stat(shared.Filename(id))
		return err
	}
	if time.Now().After(record.Stage.ExpiresAt) {
		return &domain.Error{Code: "STAGING_EXPIRED", Message: "Credential staging expired; start a new import"}
	}
	prior, err := root.ReadFile(shared.Filename(id))
	defer clear(prior)
	if err == nil && sha256.Sum256(prior) != sha256.Sum256(record.Value) {
		return secretConflict()
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = writeFile(root, shared.Filename(id), record.Value); err != nil {
		return err
	}
	clear(record.Value)
	record.Value, record.CredentialID = nil, id
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return writeFile(root, path, data)
}

func (s *SecretStore) Open(ctx context.Context, id domain.ID) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.root()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(shared.Filename(id))
	if err != nil {
		return nil, err
	}
	defer file.Close()
	value, err := secretBytes(file)
	if err != nil {
		return nil, err
	}
	return &secretReader{Reader: bytes.NewReader(value), value: value}, nil
}

type secretReader struct {
	*bytes.Reader
	value []byte
}

func (r *secretReader) Close() error { clear(r.value); r.value = nil; r.Reader.Reset(nil); return nil }

// Inventory exposes only consumed values and never their private staging records.
func (s *SecretStore) Inventory(ctx context.Context) ([]domain.Credential, error) {
	items := map[domain.ID]domain.Credential{}
	err := s.walkStaging(ctx, func(root *os.Root, record stagedSecret) error {
		if record.CredentialID == "" {
			return nil
		}
		if _, err := root.Stat(shared.Filename(record.CredentialID)); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		items[record.CredentialID] = domain.Credential{Record: domain.Record{SchemaVersion: 1, ID: record.CredentialID, Version: 1}, Name: record.Ingress.Name, Kind: record.Ingress.Kind, Scope: domain.CredentialScope{RepositoryIDs: []domain.ID{}}, Replicas: []domain.CredentialReplica{{NodeID: s.NodeID, State: "available", Source: "managed"}}}
		return nil
	})
	if err != nil {
		return nil, err
	}
	result := make([]domain.Credential, 0, len(items))
	for _, item := range items {
		result = append(result, item)
	}
	return result, nil
}

// Expire clears overdue bytes under the same holder lock as promotion. Only
// private expiry markers survive; no authority RPC or task receipt is involved.
func (s *SecretStore) Expire(ctx context.Context) error {
	return s.walkStaging(ctx, func(root *os.Root, record stagedSecret) error {
		if record.CredentialID != "" || record.Expired || time.Now().Before(record.Stage.ExpiresAt) {
			return nil
		}
		record.Expired = true
		data, err := json.Marshal(record)
		if err != nil {
			return err
		}
		return writeFile(root, "staging/"+shared.Filename(record.Stage.Reference), data)
	})
}

// walkStaging serializes bounded metadata scans with imports/removals. Private
// values are cleared before visiting a record and never accumulated in a snapshot.
func (s *SecretStore) walkStaging(ctx context.Context, visit func(*os.Root, stagedSecret) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.root()
	if err != nil {
		return err
	}
	defer root.Close()
	dir, err := root.Open("staging")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(5001)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(entries) > 5000 {
		return errors.New("credential staging inventory exceeds bound")
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".new") {
			continue
		}
		record, err := readStaged(root, "staging/"+entry.Name())
		clear(record.Value)
		record.Value = nil
		if err != nil {
			return err
		}
		if err := visit(root, record); err != nil {
			return err
		}
	}
	return nil
}

func (s *SecretStore) Remove(ctx context.Context, id domain.ID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.root()
	if err != nil {
		return err
	}
	defer root.Close()
	if err = root.Remove(shared.Filename(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
