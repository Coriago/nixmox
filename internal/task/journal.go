package task

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"nixmox/internal/domain"
	"nixmox/internal/identity"
)

// Journal retains only tasks explicitly marked for transition recovery. Shared
// task state remains authoritative; local snapshots travel with the host identity
// into the destination system, rather than becoming a second scheduler.
type Journal struct {
	Directory    string
	HostID       string
	SetResumable func(id string, resumable bool) error
}

func (j *Journal) path(task domain.Task) (string, error) {
	if j == nil || j.Directory == "" {
		return "", errors.New("persistent task journal required")
	}
	if task.HostID != j.HostID {
		return "", errors.New("cannot journal another host's task")
	}
	name, err := SnapshotName(task)
	if err != nil {
		return "", err
	}
	return filepath.Join(j.Directory, name), nil
}

// SnapshotName validates task identity before using it as a private filename.
func SnapshotName(task domain.Task) (string, error) {
	id := task.ID
	if owner, suffix, qualified := strings.Cut(id, "."); qualified {
		if owner != task.HostID {
			return "", errors.New("persistent task owner mismatch")
		}
		id = suffix
	}
	if _, err := uuid.Parse(id); err != nil {
		return "", errors.New("invalid persistent task identity")
	}
	return task.ID + ".json", nil
}

func (j *Journal) Save(task domain.Task) error {
	path, err := j.path(task)
	if err != nil {
		return err
	}
	if !task.Persist {
		return errors.New("only persistent tasks may be journaled")
	}
	if err = os.MkdirAll(j.Directory, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(task)
	if err != nil {
		return err
	}
	if err = identity.WritePrivate(path, data); err != nil {
		return err
	}
	if j.SetResumable != nil {
		return j.SetResumable(task.ID, true)
	}
	return nil
}

func (j *Journal) Remove(task domain.Task) error {
	path, err := j.path(task)
	if err != nil {
		return err
	}
	if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if directory, err := os.Open(j.Directory); err == nil {
		syncErr := directory.Sync()
		closeErr := directory.Close()
		if err = errors.Join(syncErr, closeErr); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if j.SetResumable != nil {
		return j.SetResumable(task.ID, false)
	}
	return nil
}

func (m *Manager) persist(task domain.Task) error {
	if !task.Persist {
		return nil
	}
	return m.Journal.Save(task)
}
