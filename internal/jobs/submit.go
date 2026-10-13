package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Submit persists a task before admitting it to the daemon-scoped FIFO queue.
func (r *Runner) Submit(j Job) (Job, error) {
	r.submitMu.Lock()
	defer r.submitMu.Unlock()
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Job{}, err
	}
	j.ID = "job-" + hex.EncodeToString(id[:])
	j.State = "pending"
	j.CreatedAt = time.Now().UTC()
	j.UpdatedAt = j.CreatedAt
	if err := r.store.Create(j); err != nil {
		return Job{}, err
	}
	var err error
	j, err = r.store.Get(j.ID)
	if err != nil {
		return Job{}, err
	}
	if err = r.Start(context.Background(), j); err != nil {
		r.failJob(j, err)
		return Job{}, err
	}
	return j, nil
}
