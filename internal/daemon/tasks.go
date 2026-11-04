package daemon

import (
	"nixmox/internal/task"
	"path/filepath"
	"slices"
)

func (r *Runtime) taskJournal() *task.Journal {
	return &task.Journal{Directory: filepath.Join(r.config.StateDir, "tasks"), HostID: r.node.State.NodeID, SetResumable: func(id string, resumable bool) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		ids := slices.Clone(r.node.State.ResumeTaskIDs)
		index := slices.Index(ids, id)
		if resumable && index == -1 {
			ids = append(ids, id)
		}
		if !resumable && index >= 0 {
			ids = slices.Delete(ids, index, index+1)
		}
		if slices.Equal(ids, r.node.State.ResumeTaskIDs) {
			return nil
		}
		previous := r.node.State.ResumeTaskIDs
		r.node.State.ResumeTaskIDs = ids
		if err := r.node.Save(); err != nil {
			r.node.State.ResumeTaskIDs = previous
			return err
		}
		return nil
	}}
}
