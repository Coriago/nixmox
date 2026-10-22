// Package state adapts quorum-owned metadata and immutable Git increments.
package state

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/nats-io/nats.go"
	"nixmox/internal/broker"
	"nixmox/internal/domain"
)

const MaxArtifactBytes int64 = 64 << 20

type Store struct {
	Acquire func() (*broker.Stores, error)
}
type Entry struct {
	Key      string
	Revision uint64
	Data     []byte
}

var keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[A-Za-z0-9_-]+$`)

func (s Store) Get(ctx context.Context, key string, out any) (uint64, error) {
	if !keyPattern.MatchString(key) {
		return 0, domain.Err("INVALID_ID", "Invalid metadata key.", 422)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	stores, err := s.Acquire()
	if err != nil {
		return 0, err
	}
	entry, err := stores.State.Get(key)
	if errors.Is(err, nats.ErrKeyNotFound) || errors.Is(err, nats.ErrKeyDeleted) {
		return 0, domain.Err("NOT_FOUND", "Resource not found.", 404)
	}
	if err != nil {
		return 0, domain.Err("UNAVAILABLE", "Cluster metadata is unavailable.", 503)
	}
	if domain.StrictJSON(entry.Value(), out) != nil {
		return 0, domain.Err("UNAVAILABLE", "Stored resource is invalid.", 503)
	}
	return entry.Revision(), nil
}
func (s Store) CAS(ctx context.Context, key string, value any, revision uint64) (uint64, error) {
	if !keyPattern.MatchString(key) {
		return 0, domain.Err("INVALID_ID", "Invalid metadata key.", 422)
	}
	data, err := json.Marshal(value)
	if err != nil || len(data) > domain.MaxCommandBytes {
		return 0, domain.Err("STORAGE_LIMIT", "Resource exceeds the metadata bound.", 507)
	}
	if err = ctx.Err(); err != nil {
		return 0, err
	}
	stores, err := s.Acquire()
	if err != nil {
		return 0, err
	}
	var next uint64
	if revision == 0 {
		next, err = stores.State.Create(key, data)
	} else {
		next, err = stores.State.Update(key, data, revision)
	}
	if errors.Is(err, nats.ErrKeyExists) {
		return 0, domain.Err("VERSION_CONFLICT", "Resource changed; review its current version.", 409)
	}
	if err != nil {
		return 0, domain.Err("OUTCOME_UNKNOWN", "Metadata acknowledgment unavailable; reconcile the original command.", 503)
	}
	return next, nil
}
func (s Store) List(ctx context.Context, prefix string) ([]Entry, error) {
	if !keyPattern.MatchString(prefix + "x") {
		return nil, domain.Err("INVALID_ID", "Invalid metadata prefix.", 422)
	}
	stores, err := s.Acquire()
	if err != nil {
		return nil, err
	}
	keys, err := stores.State.ListKeys(nats.Context(ctx))
	if errors.Is(err, nats.ErrNoKeysFound) {
		return []Entry{}, nil
	}
	if err != nil {
		return nil, domain.Err("UNAVAILABLE", "Catalog unavailable.", 503)
	}
	defer keys.Stop()
	entries := []Entry{}
	for {
		select {
		case <-ctx.Done():
			return nil, domain.Err("UNAVAILABLE", "Catalog scan timed out.", 503)
		case key, ok := <-keys.Keys():
			if !ok {
				return entries, nil
			}
			if !strings.HasPrefix(key, prefix) {
				continue
			}
			if len(entries) >= 1000 {
				return nil, domain.Err("STORAGE_LIMIT", "Catalog exceeds its bounded scan.", 507)
			}
			entry, err := stores.State.Get(key)
			if errors.Is(err, nats.ErrKeyNotFound) || errors.Is(err, nats.ErrKeyDeleted) {
				continue
			}
			if err != nil {
				return nil, domain.Err("UNAVAILABLE", "Catalog entry unavailable.", 503)
			}
			entries = append(entries, Entry{Key: key, Revision: entry.Revision(), Data: entry.Value()})
		}
	}
}
func (s Store) PutArtifact(ctx context.Context, key string, reader io.Reader, size int64, digest string) error {
	if size < 1 || size > MaxArtifactBytes || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(digest) || !strings.HasSuffix(key, "/"+digest) {
		return domain.Err("STORAGE_LIMIT", "Incremental Git artifact exceeds its storage bound.", 507)
	}
	// Validate before publication. A content-addressed key cannot be overwritten
	// with different bytes, including by a concurrent uploader.
	f, err := os.CreateTemp("", "nixmox-increment-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	hash := sha256.New()
	count, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(reader, size+1))
	if err != nil || count != size || hex.EncodeToString(hash.Sum(nil)) != digest {
		return domain.Err("ARTIFACT_INVALID", "Increment bytes differ from their manifest.", 409)
	}
	if _, err = f.Seek(0, 0); err != nil {
		return err
	}
	stores, err := s.Acquire()
	if err != nil {
		return err
	}
	if info, getErr := stores.Git.GetInfo(key); getErr == nil && !info.Deleted {
		if info.Size == uint64(size) && info.Metadata["sha256"] == digest {
			return nil
		}
		return domain.Err("ARTIFACT_CONFLICT", "Artifact identity already contains different bytes.", 409)
	} else if getErr != nil && !errors.Is(getErr, nats.ErrObjectNotFound) {
		return domain.Err("UNAVAILABLE", "Artifact status unavailable.", 503)
	}
	_, err = stores.Git.Put(&nats.ObjectMeta{Name: key, Metadata: map[string]string{"sha256": digest}}, f, nats.Context(ctx))
	if err != nil {
		return domain.Err("OUTCOME_UNKNOWN", "Artifact storage acknowledgment unavailable.", 503)
	}
	info, err := stores.Git.GetInfo(key)
	if err != nil || info.Size != uint64(size) {
		return domain.Err("ARTIFACT_INVALID", "Stored artifact length differs from its manifest.", 409)
	}
	return nil
}
func (s Store) ReadArtifact(ctx context.Context, key, digest string, size int64, destination io.Writer) error {
	if size < 1 || size > MaxArtifactBytes {
		return domain.Err("ARTIFACT_INVALID", "Invalid artifact size.", 409)
	}
	stores, err := s.Acquire()
	if err != nil {
		return err
	}
	object, err := stores.Git.Get(key, nats.Context(ctx))
	if err != nil {
		return domain.Err("SOURCE_UNAVAILABLE", "Required Git increment is unavailable.", 503)
	}
	defer object.Close()
	hash := sha256.New()
	count, err := io.Copy(io.MultiWriter(destination, hash), io.LimitReader(object, size+1))
	if err != nil || count != size || hex.EncodeToString(hash.Sum(nil)) != digest {
		return domain.Err("ARTIFACT_INVALID", "Git increment failed integrity verification.", 409)
	}
	return ctx.Err()
}
