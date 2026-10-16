package cluster

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const applyLogKeep = 50

type Record struct {
	Kind      string    `json:"kind,omitempty"`
	Mode      string    `json:"mode,omitempty"`
	Timestamp time.Time `json:"timestamp"`
	Status    string    `json:"status"`
	GitSHA    string    `json:"git_sha"`
	GenBefore int       `json:"gen_before"`
	GenAfter  int       `json:"gen_after"`
	JobID     string    `json:"job_id"`
}

type RecordStore interface {
	Record(nodeID string, r Record) error
	Latest(nodeID string) (Record, bool, error)
	History(nodeID string) ([]Record, error)
}

// LocalRecordStore keeps per-node apply history as JSONL under
// {dataDir}/applylog/<nodeID>.jsonl, pruned to the last applyLogKeep entries.
// SwitchingRecordStore mirrors local records to a KV-backed implementation.
type LocalRecordStore struct {
	dir string
	mu  sync.Mutex
}

func NewLocalRecordStore(dataDir string) (*LocalRecordStore, error) {
	dir := filepath.Join(dataDir, "applylog")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create applylog dir: %w", err)
	}
	return &LocalRecordStore{dir: dir}, nil
}

func (s *LocalRecordStore) path(nodeID string) string {
	return filepath.Join(s.dir, nodeID+".jsonl")
}

func (s *LocalRecordStore) Record(nodeID string, r Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	records, err := s.historyLocked(nodeID)
	if err != nil {
		return err
	}
	records = append([]Record{r}, records...)
	if len(records) > applyLogKeep {
		records = records[:applyLogKeep]
	}
	return s.writeLocked(nodeID, records)
}

func (s *LocalRecordStore) Latest(nodeID string) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	records, err := s.historyLocked(nodeID)
	if err != nil || len(records) == 0 {
		return Record{}, false, err
	}
	return records[0], true, nil
}

func (s *LocalRecordStore) History(nodeID string) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.historyLocked(nodeID)
}

// historyLocked returns records newest-first.
func (s *LocalRecordStore) historyLocked(nodeID string) ([]Record, error) {
	f, err := os.Open(s.path(nodeID))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var records []Record
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r Record
		if err := json.Unmarshal(line, &r); err != nil {
			continue
		}
		records = append(records, r)
	}
	return records, sc.Err()
}

func (s *LocalRecordStore) writeLocked(nodeID string, records []Record) error {
	var buf []byte
	for _, r := range records {
		line, err := json.Marshal(r)
		if err != nil {
			return err
		}
		buf = append(buf, line...)
		buf = append(buf, '\n')
	}
	return os.WriteFile(s.path(nodeID), buf, 0o640)
}
