package recipe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/cluster"
	"nixmox/internal/source"
)

type Versions struct{ Records cluster.Store[Record] }

// List returns the latest immutable version per recipe, using keys rather than
// retaining every historical payload while assembling a page.
func (v Versions) List(ctx context.Context, after string, limit int) ([]Record, string, error) {
	if limit < 1 || limit > 500 {
		return nil, "", errors.New("invalid recipe page size")
	}
	allKeys, err := v.Records.KV.Keys(ctx)
	if errors.Is(err, jetstream.ErrNoKeysFound) {
		return []Record{}, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	latest := map[string]uint64{}
	for _, key := range allKeys {
		id, number, ok := strings.Cut(key, ".")
		if !ok || id <= after {
			continue
		}
		version, err := strconv.ParseUint(number, 10, 64)
		if err != nil {
			return nil, "", err
		}
		if version > latest[id] {
			latest[id] = version
		}
	}
	ids := keys(latest)
	next := ""
	if len(ids) > limit {
		ids = ids[:limit]
		next = ids[len(ids)-1]
	}
	result := make([]Record, 0, len(ids))
	for _, id := range ids {
		record, err := v.Get(ctx, id, latest[id])
		if err != nil {
			return nil, "", err
		}
		result = append(result, record)
	}
	return result, next, nil
}

type Patch struct {
	Fields     map[string]any `json:"fields,omitempty"`
	TargetRepo *source.Pin    `json:"targetRepo,omitempty"`
	Definition *Definition    `json:"sourceDefinition,omitempty"`
}

func versionKey(id string, version uint64) (string, error) {
	if uuid.Validate(id) != nil || version == 0 {
		return "", errors.New("valid recipe ID and positive version required")
	}
	return fmt.Sprintf("%s.%020d", id, version), nil
}
func (v Versions) Get(ctx context.Context, id string, version uint64) (Record, error) {
	key, err := versionKey(id, version)
	if err != nil {
		return Record{}, err
	}
	record, err := v.Records.Get(ctx, key)
	return record.Value, err
}

// Latest scans a native KV snapshot rather than maintaining a mutable head or
// mirrored registry. A crash cannot leave a published version behind its head.
func (v Versions) Latest(ctx context.Context, id string) (Record, error) {
	if uuid.Validate(id) != nil {
		return Record{}, errors.New("invalid recipe ID")
	}
	var latest Record
	complete := errors.New("recipe snapshot complete")
	err := v.Records.Watch(ctx, id+".*", func(record *cluster.Record[Record], deleted bool) error {
		if record == nil {
			return complete
		}
		if !deleted && record.Value.Version > latest.Version {
			latest = record.Value
		}
		return nil
	})
	if !errors.Is(err, complete) {
		return Record{}, err
	}
	if latest.Version == 0 {
		return Record{}, jetstream.ErrKeyNotFound
	}
	return latest, nil
}

func (v Versions) Create(ctx context.Context, record Record) (Record, error) {
	result, err := clone(record)
	if err != nil {
		return Record{}, err
	}
	result.ID = uuid.NewString()
	result.Version = 1
	return v.persist(ctx, result)
}
func (v Versions) persist(ctx context.Context, result Record) (Record, error) {
	if err := result.Normalize(ctx); err != nil {
		return Record{}, err
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return Record{}, err
	}
	if len(encoded) > 900<<10 {
		return Record{}, errors.New("recipe exceeds shared-record size limit")
	}
	key, err := versionKey(result.ID, result.Version)
	if err != nil {
		return Record{}, err
	}
	_, err = v.Records.Create(ctx, key, result)
	return result, err
}
func (v Versions) current(ctx context.Context, id string, expected uint64) (Record, error) {
	record, err := v.Latest(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if record.Version != expected {
		return Record{}, fmt.Errorf("recipe version changed: %w", jetstream.ErrKeyExists)
	}
	if record.Version == ^uint64(0) {
		return Record{}, errors.New("recipe version exhausted")
	}
	return record, nil
}

// Update detaches edits to a Git-sourced definition into a new recipe. Merely
// changing user fields or the target pin retains the source relationship.
func (v Versions) Update(ctx context.Context, id string, expected uint64, patch Patch) (Record, error) {
	current, err := v.current(ctx, id, expected)
	if err != nil {
		return Record{}, err
	}
	result, err := clone(current)
	if err != nil {
		return Record{}, err
	}
	result.Version++
	if patch.TargetRepo != nil {
		result.TargetRepo = *patch.TargetRepo
	}
	detached := patch.Definition != nil && current.RecipeSource != nil && !reflect.DeepEqual(*patch.Definition, current.SourceDefinition)
	if patch.Definition != nil {
		definition, err := clone(*patch.Definition)
		if err != nil {
			return Record{}, err
		}
		result.SourceDefinition = definition
	}
	for name, value := range patch.Fields {
		field, ok := result.SourceDefinition.Fields[name]
		if !ok {
			return Record{}, fmt.Errorf("unknown recipe field %q", name)
		}
		if value == nil {
			delete(result.Collected.Fields, name)
			continue
		}
		if field.Type == "text" {
			if _, ok := value.(string); !ok {
				return Record{}, fmt.Errorf("field %s requires text", name)
			}
		}
		if field.Type == "boolean" {
			if _, ok := value.(bool); !ok {
				return Record{}, fmt.Errorf("field %s requires a boolean", name)
			}
		}
		copied, err := clone(value)
		if err != nil {
			return Record{}, err
		}
		result.Collected.Fields[name] = copied
	}
	if detached {
		result.RecipeSource = nil
		return v.Create(ctx, result)
	}
	return v.persist(ctx, result)
}

func (v Versions) Observe(ctx context.Context, id string, expected uint64, name string, collector func(context.Context, Observation) (CollectedObservation, error)) (Record, error) {
	current, err := v.current(ctx, id, expected)
	if err != nil {
		return Record{}, err
	}
	result, err := Collect(ctx, current, name, collector)
	if err != nil {
		return Record{}, err
	}
	result.Version++
	return v.persist(ctx, result)
}
