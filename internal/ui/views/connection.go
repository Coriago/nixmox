package views

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"nixmox/internal/client"
	"nixmox/internal/domain"
)

// Connection owns HTTP lifetimes, not presentation state. Both adapters deliver
// its updates on their UI loop and cancel it when the interface closes.
type Connection struct {
	API       *client.Client
	selection chan Selection
	commands  chan domain.Command
}
type Selection struct{ Host, Task domain.ID }
type Snapshot struct {
	Identity    domain.Identity
	Cluster     domain.ClusterStatus
	Hosts       []domain.Host
	Tasks       []domain.Task
	Discoveries []domain.Discovery
	Requests    []domain.JoinRequest
	Warnings    []string
}
type Update struct {
	Snapshot  *Snapshot
	Tasks     []domain.Task
	Task      *domain.Task
	Telemetry *domain.HostTelemetry
	Log       *domain.LogChunk
	Resource  domain.ID
	Receipt   *domain.Receipt
	CommandID domain.ID
	Reconcile bool
	Err       error
}

func NewConnection(api *client.Client) *Connection {
	return &Connection{API: api, selection: make(chan Selection, 1), commands: make(chan domain.Command, 1)}
}
func (c *Connection) Select(s Selection) {
	select {
	case <-c.selection:
	default:
	}
	c.selection <- s
}
func (c *Connection) Submit(command domain.Command) { c.commands <- command }

func allPages[T any](ctx context.Context, read func(context.Context, domain.PageRequest) (domain.Page[T], error)) ([]T, error) {
	items := []T{}
	page := domain.PageRequest{Limit: domain.PageLimit}
	for {
		result, err := read(ctx, page)
		if err != nil {
			return nil, err
		}
		items = append(items, result.Items...)
		if result.NextCursor == nil {
			return items, nil
		}
		if len(items) >= 10000 || *result.NextCursor == page.Cursor {
			return nil, errors.New("console collection exceeds its read bound")
		}
		page.Cursor = *result.NextCursor
	}
}
func (c *Connection) snapshot(ctx context.Context) Update {
	s := &Snapshot{}
	var err error
	s.Identity, err = c.API.Identity(ctx)
	if err != nil {
		return Update{Err: err}
	}
	var wg sync.WaitGroup
	failures := make([]error, 5)
	wg.Go(func() { s.Cluster, failures[0] = c.API.Cluster(ctx) })
	wg.Go(func() {
		s.Hosts, failures[1] = allPages(ctx, func(ctx context.Context, p domain.PageRequest) (domain.Page[domain.Host], error) {
			return c.API.Hosts(ctx, p)
		})
	})
	wg.Go(func() { s.Tasks, failures[2] = allPages(ctx, c.API.Tasks) })
	wg.Go(func() {
		s.Discoveries, failures[3] = allPages(ctx, func(ctx context.Context, p domain.PageRequest) (domain.Page[domain.Discovery], error) {
			return c.API.Discoveries(ctx, p)
		})
	})
	if s.Identity.ClusterID != nil {
		wg.Go(func() {
			s.Requests, failures[4] = allPages(ctx, func(ctx context.Context, p domain.PageRequest) (domain.Page[domain.JoinRequest], error) {
				p.Filters = map[string]string{"state": "awaiting_approval"}
				return c.API.Requests(ctx, p)
			})
		})
	}
	wg.Wait()
	for i, err := range failures {
		if err != nil {
			s.Warnings = append(s.Warnings, fmt.Sprintf("%s: %v", []string{"Cluster", "Hosts", "Tasks", "Discovery", "Join requests"}[i], err))
		}
	}
	return Update{Snapshot: s}
}

// watchTask retains log offsets when an owner disappears or admission has not
// reached the read projection yet. Reconnection never resubmits commands.
func (c *Connection) watchTask(ctx context.Context, id domain.ID, logs bool, emit func(Update)) {
	offset := int64(0)
	initialized := !logs
	for ctx.Err() == nil {
		var err error
		if !initialized {
			var task domain.Task
			task, err = c.API.Task(ctx, id)
			if err == nil {
				emit(Update{Task: &task})
				offset = max(0, task.Log.NextOffset-domain.LogReadDefault)
				initialized = true
			}
		}
		if err == nil {
			err = c.API.WatchTask(ctx, id, logs, offset, func(data json.RawMessage) error {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if logs {
					var chunk domain.LogChunk
					if err := json.Unmarshal(data, &chunk); err != nil {
						return err
					}
					emit(Update{Resource: id, Log: &chunk})
					offset = chunk.NextOffset
				} else {
					var task domain.Task
					if err := json.Unmarshal(data, &task); err != nil {
						return err
					}
					emit(Update{Task: &task})
				}
				return nil
			})
		}
		if ctx.Err() != nil || err == nil {
			return
		}
		emit(Update{Resource: id, Err: err})
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func (c *Connection) Run(ctx context.Context, emit func(Update)) {
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	send := func(u Update) {
		if ctx.Err() == nil {
			emit(u)
		}
	}
	refresh := make(chan struct{}, 1)
	inventory := make(chan []domain.Host, 1)
	workers.Go(func() {
		full := time.Time{}
		timer := time.NewTicker(2 * time.Second)
		defer timer.Stop()
		for {
			if time.Since(full) >= domain.InventoryInterval {
				update := c.snapshot(ctx)
				send(update)
				if update.Snapshot != nil && update.Snapshot.Hosts != nil {
					select {
					case <-inventory:
					default:
					}
					select {
					case inventory <- update.Snapshot.Hosts:
					case <-ctx.Done():
						return
					}
				}
				if update.Snapshot != nil {
					full = time.Now()
				}
			} else {
				tasks, err := allPages(ctx, c.API.Tasks)
				send(Update{Tasks: tasks, Err: err})
			}
			select {
			case <-ctx.Done():
				return
			case <-refresh:
				full = time.Time{}
			case <-timer.C:
			}
		}
	})
	var stopSelection context.CancelFunc
	hostStreams := map[domain.ID]context.CancelFunc{}
	// Bound concurrent subscriptions. Larger inventories remain browsable and
	// selecting a host gives its telemetry priority over background tiles.
	var hosts []domain.Host
	var selectedHost domain.ID
	watchHosts := func() {
		wanted := map[domain.ID]bool{}
		for _, h := range hosts {
			if h.ID == selectedHost && h.Reachability == domain.Online {
				wanted[h.ID] = true
			}
		}
		for _, h := range hosts {
			if h.Reachability == domain.Online && len(wanted) < 64 {
				wanted[h.ID] = true
			}
		}
		for id, stop := range hostStreams {
			if !wanted[id] {
				stop()
				delete(hostStreams, id)
			}
		}
		for id := range wanted {
			if hostStreams[id] != nil {
				continue
			}
			life, stop := context.WithCancel(ctx)
			hostStreams[id] = stop
			workers.Go(func() {
				for {
					err := c.API.WatchTelemetry(life, id, func(sample domain.HostTelemetry) error {
						if life.Err() == nil {
							send(Update{Telemetry: &sample})
						}
						return nil
					})
					if life.Err() != nil {
						return
					}
					if err != nil {
						send(Update{Resource: id, Err: err})
					}
					select {
					case <-life.Done():
						return
					case <-time.After(5 * time.Second):
					}
				}
			})
		}
	}
	for {
		select {
		case <-ctx.Done():
			if stopSelection != nil {
				stopSelection()
			}
			return
		case next := <-inventory:
			hosts = next
			watchHosts()
		case s := <-c.selection:
			selectedHost = s.Host
			watchHosts()
			if stopSelection != nil {
				stopSelection()
			}
			selected, stop := context.WithCancel(ctx)
			stopSelection = stop
			if s.Task != "" {
				for _, logs := range []bool{false, true} {
					workers.Go(func() { c.watchTask(selected, s.Task, logs, send) })
				}
			}
		case command := <-c.commands:
			var receipt domain.Receipt
			var err error
			if command.Type == "" {
				receipt, err = c.API.Receipt(ctx, command.ID)
			} else {
				receipt, err = c.API.Submit(ctx, command)
			}
			send(Update{Receipt: &receipt, CommandID: command.ID, Reconcile: command.Type == "", Err: err})
			select {
			case refresh <- struct{}{}:
			default:
			}
		}
	}
}
