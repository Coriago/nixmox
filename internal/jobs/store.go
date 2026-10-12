package jobs

import (
	"encoding/json"
	"fmt"
	"nixmox/internal/statefile"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

// Job represents a single execution task.
type Job struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Host      string    `json:"host"`
	Args      []string  `json:"args"`
	Mode      string    `json:"mode,omitempty"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	LogPath   string    `json:"log_path"`
	GitSHA    string    `json:"git_sha,omitempty"`
	GenBefore int       `json:"gen_before,omitempty"`
	GenAfter  int       `json:"gen_after,omitempty"`
}

// Store defines the interface for job persistence.
type Store interface {
	Create(Job) error
	Get(id string) (Job, error)
	List() ([]Job, error)
	Update(Job) error
	AppendLog(id string, line string) error
}

var validID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

type fileStore struct {
	dir string
	mu  sync.Mutex
}

// NewFileStore creates a new file-backed job store.
func NewFileStore(dataDir string) (Store, error) {
	dir := filepath.Join(dataDir, "jobs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create jobs dir: %w", err)
	}
	return &fileStore{dir: dir}, nil
}

func (s *fileStore) jobPath(id string) string {
	return filepath.Join(s.dir, id+".json")
}

func (s *fileStore) logPath(id string) string {
	return filepath.Join(s.dir, id+".log")
}

func (s *fileStore) Create(j Job) error {
	if !validID.MatchString(j.ID) {
		return fmt.Errorf("invalid job ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.jobPath(j.ID)
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("job %s already exists", j.ID)
	}

	j.LogPath = s.logPath(j.ID)
	return s.writeJob(j)
}

func (s *fileStore) Get(id string) (Job, error) {
	if !validID.MatchString(id) {
		return Job{}, fmt.Errorf("invalid job ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.readJob(id)
}

func (s *fileStore) List() ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("read jobs dir: %w", err)
	}

	var jobs []Job
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := entry.Name()[:len(entry.Name())-5]
		j, err := s.readJob(id)
		if err != nil {
			continue // skip unreadable jobs
		}
		jobs = append(jobs, j)
	}

	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].CreatedAt.After(jobs[j].CreatedAt)
	})

	return jobs, nil
}

func (s *fileStore) Update(j Job) error {
	if !validID.MatchString(j.ID) {
		return fmt.Errorf("invalid job ID")
	}
	j.LogPath = s.logPath(j.ID)
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.jobPath(j.ID)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("job %s not found", j.ID)
	}

	return s.writeJob(j)
}

func (s *fileStore) AppendLog(id string, line string) error {
	if !validID.MatchString(id) {
		return fmt.Errorf("invalid job ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.logPath(id)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}
	defer f.Close()

	if len(line) > 0 && line[len(line)-1] != '\n' {
		line += "\n"
	}

	if _, err := f.WriteString(line); err != nil {
		return fmt.Errorf("write log line: %w", err)
	}
	return nil
}

// writeJob writes a job to disk atomically.
// Caller must hold s.mu.
func (s *fileStore) writeJob(j Job) error {
	return statefile.Write(s.jobPath(j.ID), j)
}

// readJob reads a job from disk.
// Caller must hold s.mu.
func (s *fileStore) readJob(id string) (Job, error) {
	path := s.jobPath(id)
	data, err := os.ReadFile(path)
	if err != nil {
		return Job{}, fmt.Errorf("read job file: %w", err)
	}

	var j Job
	if err := json.Unmarshal(data, &j); err != nil {
		return Job{}, fmt.Errorf("unmarshal job: %w", err)
	}

	return j, nil
}
