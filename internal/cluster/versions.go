package cluster

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
)

// VersionKey and VersionNumbers share key conventions only. Domain repositories
// own decoding, schema validation and immutable writes.
func VersionKey(id string, version uint64) (string, error) {
	if uuid.Validate(id) != nil || version == 0 {
		return "", errors.New("UUID and positive version required")
	}
	return fmt.Sprintf("%s.%020d", id, version), nil
}

func VersionNumbers(ctx context.Context, kv jetstream.KeyValue) (map[string]uint64, error) {
	keys, err := kv.Keys(ctx)
	if errors.Is(err, jetstream.ErrNoKeysFound) {
		return map[string]uint64{}, nil
	}
	if err != nil {
		return nil, err
	}
	latest := map[string]uint64{}
	for _, key := range keys {
		id, number, ok := strings.Cut(key, ".")
		n, err := strconv.ParseUint(number, 10, 64)
		if !ok || uuid.Validate(id) != nil || err != nil || n == 0 {
			return nil, errors.New("unrecognized version keys: explicit storage migration required")
		}
		if n > latest[id] {
			latest[id] = n
		}
	}
	return latest, nil
}
