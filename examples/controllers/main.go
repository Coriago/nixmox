// Run: go run ./examples/controllers
//
// A self-contained explanation, NOT production infrastructure:
//   - An ephemeral embedded JetStream server, two KV buckets, no cache.
//   - Host A owns a demotion task; host B owns its member record.
//   - Both hosts could compute placement; here the deterministic result is B.
//   - Watches only enqueue work. Each controller serializes its own reconciliation.
//   - Slow startup runs separately, with cancellation and stale-result protection.
//
// The scripted demo requests bootstrap, cancels during startup, then requests it
// again. Look for task updates being processed while bootstrap is starting.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

type Task struct {
	Demoting bool   `json:"demoting"` // Input: request/cancel demotion of A.
	Phase    string `json:"phase"`    // Output: maintained by A's task controller.
}

type Member struct {
	RunBootstrap bool `json:"runBootstrap"`
	Ready        bool `json:"ready"`
}

// One key per controller in this tiny demo, so a one-slot channel is enough.
// Notifications coalesce: reconciliation reads current state, not event history.
func enqueue(q chan struct{}) {
	select {
	case q <- struct{}{}:
	default:
	}
}

func read[T any](ctx context.Context, kv jetstream.KeyValue, key string) (T, uint64, error) {
	var value T
	entry, err := kv.Get(ctx, key)
	if err != nil {
		return value, 0, err
	}
	err = json.Unmarshal(entry.Value(), &value)
	return value, entry.Revision(), err
}

// A's input writer and task controller update different fields in the SAME task.
// Revision checks prevent them from overwriting each other's changes.
func changeTask(ctx context.Context, tasks jetstream.KeyValue, change func(*Task)) error {
	for {
		before, revision, err := read[Task](ctx, tasks, "demote-A")
		if err != nil {
			return err
		}
		after := before
		change(&after)
		if after == before {
			return nil // No write -> no self-triggering update loop.
		}
		data, _ := json.Marshal(after)
		_, err = tasks.Update(ctx, "demote-A", data, revision)
		if errors.Is(err, jetstream.ErrKeyRevisionMismatch) {
			continue // Read latest input and recompute.
		}
		return err
	}
}

// The watcher does no slow work, reads no other records, and starts no processes.
func watch(ctx context.Context, kv jetstream.KeyValue, queues ...chan struct{}) error {
	w, err := kv.WatchAll(ctx)
	if err != nil {
		return err
	}
	go func() {
		defer w.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case entry, ok := <-w.Updates():
				if !ok {
					return
				}
				if entry == nil {
					continue // End of initial snapshot, not a record update.
				}
				for _, q := range queues {
					enqueue(q)
				}
			}
		}
	}()
	return nil
}

// Host A: member changes wake this controller, which projects the prerequisite
// onto A's TASK record. A task runner could watch only that task record.
func taskController(ctx context.Context, tasks, members jetstream.KeyValue, q chan struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-q:
			member, _, err := read[Member](ctx, members, "B")
			if err == nil {
				err = changeTask(ctx, tasks, func(task *Task) {
					phase := "idle"
					if task.Demoting {
						phase = "waiting-for-bootstrap"
						if member.RunBootstrap && member.Ready {
							phase = "bootstrap-ready" // Evacuation would follow.
						}
					}
					if task.Phase != phase {
						log.Printf("A: task -> %s", phase)
					}
					task.Phase = phase
				})
			}
			if err != nil && ctx.Err() == nil {
				log.Printf("A: reconcile error: %v", err)
			}
		}
	}
}

// Host B: placement and local runtime are combined for brevity. Only this loop
// writes B's member record; the startup goroutine NEVER writes shared state.
func bootstrapController(ctx context.Context, tasks, members jetstream.KeyValue, q chan struct{}) {
	var local Member
	var cancel context.CancelFunc
	var generation uint64
	completed := make(chan uint64, 1)
	defer func() {
		if cancel != nil {
			cancel()
		}
	}()

	for {
		var finished uint64
		select {
		case <-ctx.Done():
			return
		case <-q:
		case finished = <-completed:
		}

		// Re-read intent even on startup completion: the request may have changed
		// before its watch notification reached this controller.
		task, _, err := read[Task](ctx, tasks, "demote-A")
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("B: reconcile error: %v", err)
			}
			continue
		}
		before := local
		local.RunBootstrap = task.Demoting // Demo election always selects B.

		switch {
		case !local.RunBootstrap:
			if cancel != nil {
				log.Print("B: cancel startup / stop bootstrap")
				cancel()
				cancel = nil
				generation++ // Any late completion is now stale.
			}
			local.Ready = false
		case finished != 0 && finished == generation:
			local.Ready = true
			log.Print("B: bootstrap ready")
		case cancel == nil:
			generation++
			opCtx, stop := context.WithCancel(ctx)
			cancel = stop
			log.Print("B: initiating 5-second startup; reconciliation returns immediately")
			go func(token uint64) {
				timer := time.NewTimer(5 * time.Second)
				defer timer.Stop()
				select {
				case <-opCtx.Done():
					return
				case <-timer.C:
				}
				select {
				case completed <- token:
				case <-opCtx.Done():
				}
			}(generation)
		}

		if local != before {
			data, _ := json.Marshal(local)
			if _, err := members.Put(ctx, "B", data); err != nil && ctx.Err() == nil {
				log.Printf("B: publish error: %v", err)
			}
		}
	}
}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func main() {
	log.SetFlags(log.Lmicroseconds)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Embedded server makes the example runnable without any external setup.
	dir, err := os.MkdirTemp("", "controllers-demo-*")
	must(err)
	defer os.RemoveAll(dir)
	s, err := server.NewServer(&server.Options{
		Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: dir,
	})
	must(err)
	go s.Start()
	defer s.Shutdown()
	if !s.ReadyForConnections(5 * time.Second) {
		log.Fatal("NATS did not start")
	}
	nc, err := nats.Connect(s.ClientURL())
	must(err)
	defer nc.Close()
	js, err := jetstream.New(nc)
	must(err)



	members, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "members"})
	must(err)
	tasks, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "tasks"})
	must(err)
	_, err = members.Put(ctx, "B", []byte(`{"runBootstrap":false,"ready":false}`))
	must(err)
	_, err = tasks.Put(ctx, "demote-A", []byte(`{"demoting":false,"phase":"idle"}`))
	must(err)

	aQueue, bQueue := make(chan struct{}, 1), make(chan struct{}, 1)
	// Fan-out: each interested controller has its OWN queue and worker.
	must(watch(ctx, tasks, aQueue, bQueue))
	must(watch(ctx, members, aQueue, bQueue))
	go taskController(ctx, tasks, members, aQueue)
	go bootstrapController(ctx, tasks, members, bQueue)

	request := func(demoting bool) {
		log.Printf("INPUT: demoting A = %v", demoting)
		must(changeTask(ctx, tasks, func(t *Task) { t.Demoting = demoting }))
	}
	request(true)
	time.Sleep(2 * time.Second)
	request(false) // Arrives while the first startup is still running.
	time.Sleep(time.Second)
	request(true) // Starts a NEW operation; old results cannot mark it ready.
	time.Sleep(7 * time.Second)
	final, _, err := read[Task](ctx, tasks, "demote-A")
	must(err)
	fmt.Printf("\nFinal task: %+v\n", final)
	cancel()
	// For clarity this demo omits watch recovery, retries, shutdown joining,
	// readiness freshness, and real process stop/cleanup before restarting.
}
