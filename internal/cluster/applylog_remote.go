package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/nats-io/nats.go/jetstream"
)

// KVRecordStore is the clustered RecordStore: history lives in the `applylog`
// KV bucket as applylog.<nodeID>.<unix-ts> entries (separate bucket from
// `hosts` because bucket TTL is bucket-wide and liveness keys must expire
// while history must not).
type KVRecordStore struct {
	kv jetstream.KeyValue
}

func NewKVRecordStore(kv jetstream.KeyValue) *KVRecordStore {
	return &KVRecordStore{kv: kv}
}

func (s *KVRecordStore) Record(nodeID string, r Record) error {
	key := fmt.Sprintf("applylog.%s.%d", nodeID, r.Timestamp.Unix())
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if _, err := s.kv.Put(context.Background(), key, data); err != nil {
		return err
	}
	return s.prune(nodeID)
}

func (s *KVRecordStore) Latest(nodeID string) (Record, bool, error) {
	records, err := s.History(nodeID)
	if err != nil || len(records) == 0 {
		return Record{}, false, err
	}
	return records[0], true, nil
}

func (s *KVRecordStore) History(nodeID string) ([]Record, error) {
	keys, err := s.kv.Keys(context.Background(), jetstream.IgnoreDeletes())
	if err != nil {
		if strings.Contains(err.Error(), "no keys") {
			return nil, nil
		}
		return nil, err
	}
	prefix := "applylog." + nodeID + "."
	var tsKeys []int64
	for _, k := range keys {
		if strings.HasPrefix(k, prefix) {
			if ts, err := strconv.ParseInt(strings.TrimPrefix(k, prefix), 10, 64); err == nil {
				tsKeys = append(tsKeys, ts)
			}
		}
	}
	sort.Slice(tsKeys, func(i, j int) bool { return tsKeys[i] > tsKeys[j] })

	var records []Record
	for _, ts := range tsKeys {
		entry, err := s.kv.Get(context.Background(), fmt.Sprintf("%s%d", prefix, ts))
		if err != nil {
			continue
		}
		var r Record
		if err := json.Unmarshal(entry.Value(), &r); err == nil {
			records = append(records, r)
		}
	}
	return records, nil
}

func (s *KVRecordStore) prune(nodeID string) error {
	records, err := s.History(nodeID)
	if err != nil || len(records) <= applyLogKeep {
		return err
	}
	prefix := "applylog." + nodeID + "."
	for _, r := range records[applyLogKeep:] {
		if err := s.kv.Delete(context.Background(), fmt.Sprintf("%s%d", prefix, r.Timestamp.Unix())); err != nil {
			return err
		}
	}
	return nil
}
