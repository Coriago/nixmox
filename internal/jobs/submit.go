package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

// Submit is the single creation path for daemon-scoped tasks. A rejected task
// is terminal, never an orphaned pending job. Start serializes work per host.
func (r *Runner) Submit(j Job) (Job, error) {
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
