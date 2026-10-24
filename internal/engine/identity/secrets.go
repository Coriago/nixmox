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

var _ engine.Secrets = (*SecretStore)(nil)

type stagedSecret struct {
	Ingress      engine.SecretIngress
	Stage        domain.SecretStaging
	Value        []byte
	CredentialID domain.ID
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
		return stagedSecret{}, errors.New("invalid private staging record")
	}
	return record, nil
}

func secretConflict() error {
	return &domain.Error{Code: "IDEMPOTENCY_CONFLICT", Message: "Import command ID already used"}
}

// Inspect returns public staging metadata only. Expiry cleanup must consult the
// durable command claim first: accepted but unfinished imports cannot be erased.
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

// Consume is called only after durable admission. An interrupted promotion can be
// retried; an existing different value is never silently replaced.
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
	dir, err := root.Open("staging")
	if errors.Is(err, os.ErrNotExist) {
		return []domain.Credential{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(5001)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > 5000 {
		return nil, errors.New("credential staging inventory exceeds bound")
	}
	items := map[domain.ID]domain.Credential{}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".new") {
			continue
		}
		record, err := readStaged(root, "staging/"+entry.Name())
		clear(record.Value)
		if err != nil {
			return nil, err
		}
		if record.CredentialID == "" {
			continue
		}
		if _, err := root.Stat(shared.Filename(record.CredentialID)); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		items[record.CredentialID] = domain.Credential{Record: domain.Record{SchemaVersion: 1, ID: record.CredentialID, Version: 1}, Name: record.Ingress.Name, Kind: record.Ingress.Kind, Scope: domain.CredentialScope{RepositoryIDs: []domain.ID{}}, Replicas: []domain.CredentialReplica{{NodeID: s.NodeID, State: "available", Source: "managed"}}}
	}
	result := make([]domain.Credential, 0, len(items))
	for _, item := range items {
		result = append(result, item)
	}
	return result, nil
}

// PendingSecret contains only public ingress metadata, never secret bytes.
type PendingSecret struct {
	Ingress engine.SecretIngress
	Stage   domain.SecretStaging
}

func (s *SecretStore) Expired(ctx context.Context) ([]PendingSecret, error) {
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
	dir, err := root.Open("staging")
	if errors.Is(err, os.ErrNotExist) {
		return []PendingSecret{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(5001)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > 5000 {
		return nil, errors.New("credential staging inventory exceeds bound")
	}
	result := []PendingSecret{}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), ".new") {
			continue
		}
		record, err := readStaged(root, "staging/"+entry.Name())
		clear(record.Value)
		if err != nil {
			return nil, err
		}
		if record.CredentialID == "" && time.Now().After(record.Stage.ExpiresAt) {
			result = append(result, PendingSecret{Ingress: record.Ingress, Stage: record.Stage})
		}
	}
	return result, nil
}

// DiscardExpired requires the authority to have fenced the command ID first.
// Accepted work is protected by the durable command claim, not a local timer.
func (s *SecretStore) DiscardExpired(ctx context.Context, ref, command domain.ID) error {
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
	path := "staging/" + shared.Filename(ref)
	record, err := readStaged(root, path)
	clear(record.Value)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if record.CredentialID != "" || record.Stage.CommandID != command || !time.Now().After(record.Stage.ExpiresAt) {
		return errors.New("staging is not eligible for expiry")
	}
	if err = root.Remove(path); err != nil {
		return err
	}
	dir, err := root.Open("staging")
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
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
