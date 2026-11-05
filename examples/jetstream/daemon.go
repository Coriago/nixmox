// daemon.go defines daemon ownership and the public lifecycle/action API.
package main

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
)

var daemonCount atomic.Uint64

// Daemon owns its NATS manager and local controllers.
// Persistence, stores, watchers, and controller scheduling are not implemented yet.
type Daemon struct {
	ID        string
	Name      string
	Directory string
	NATS      *NATSManager
	Members   *MemberController
}

// NewDaemon assigns a count-based name and prepares the supplied directory.
// A blank directory creates temporary storage. No servers or clients start here;
// loading an existing directory's persisted identity remains unimplemented.
func NewDaemon(directory string) (*Daemon, error) {
	if directory == "" {
		var err error
		directory, err = os.MkdirTemp("", "jetstream-daemon-")
		if err != nil {
			return nil, err
		}
	} else if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}

	name := fmt.Sprintf("daemon-%d", daemonCount.Add(1))
	d := &Daemon{
		ID:        name,
		Name:      name,
		Directory: directory,
		NATS:      &NATSManager{ID: name, Directory: directory},
	}
	d.Members = &MemberController{Daemon: d}
	return d, nil
}

// CreateCluster will start the initial primary and bootstrap before waiting for quorum.
func (d *Daemon) CreateCluster(ctx context.Context) error {
	return ErrNotImplemented
}

// Enroll will join an existing cluster as a client-only member.
func (d *Daemon) Enroll(ctx context.Context, seed *Daemon) error {
	return ErrNotImplemented
}

// Promote will send a promotion task event to the target and await completion.
func (d *Daemon) Promote(ctx context.Context, target string) error {
	return ErrNotImplemented
}

// Demote will send a demotion task event without immediately stopping the target's primary.
func (d *Daemon) Demote(ctx context.Context, target string) error {
	return ErrNotImplemented
}

// Remove will request permanent membership removal, not temporary shutdown.
func (d *Daemon) Remove(ctx context.Context, target string) error {
	return ErrNotImplemented
}

// Stop will announce planned termination and always stop after optional bounded evacuation.
func (d *Daemon) Stop(ctx context.Context, evict bool) error {
	return ErrNotImplemented
}

// Ready will wait for shared-state attachment after retained peers have started.
func (d *Daemon) Ready(ctx context.Context) error {
	return ErrNotImplemented
}

// Close will release local resources without removing retained peers or their storage.
func (d *Daemon) Close() error {
	return ErrNotImplemented
}
