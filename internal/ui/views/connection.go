package views

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"nixmox/internal/client"
	"nixmox/internal/domain"
)

type Selection struct{ Host, Task string }
type Request struct {
	Method, Path, Target string
	Body                 json.RawMessage
}
type Snapshot struct {
	Health                                domain.Health
	Cluster                               domain.Cluster
	Hosts                                 []domain.Member
	Tasks                                 []domain.Task
	Repositories, Recipes, Configurations []Document
	Warnings                              []string
}
type Update struct {
	Snapshot                  *Snapshot
	Task                      *domain.Task
	Resource, EventID, Target string
	Log, Response             json.RawMessage
	Err                       error
	Submitted                 bool
}

// Connection owns bounded HTTP work and selected-resource streams. It never
// touches presentation state; adapters deliver updates on their UI event loop.
type Connection struct {
	API       *client.Client
	selection chan Selection
	commands  chan Request
}

func NewConnection(api *client.Client) *Connection {
	return &Connection{API: api, selection: make(chan Selection, 1), commands: make(chan Request, 1)}
}
func (c *Connection) Select(selection Selection) {
	select {
	case <-c.selection:
	default:
	}
	c.selection <- selection
}
func (c *Connection) Submit(request Request) bool {
	select {
	case c.commands <- request:
		return true
	default:
		return false
	}
}

func readCollection[T any](ctx context.Context, api *client.Client, path string) ([]T, error) {
	items := []T{}
	cursor := ""
	for {
		var page client.Page[T]
		if err := api.Do(ctx, http.MethodGet, path+"?limit=100&cursor="+url.QueryEscape(cursor), nil, &page); err != nil {
			return nil, err
		}
		items = append(items, page.Items...)
		if len(items) > 10000 {
			return nil, errors.New("console collection exceeds 10000 records")
		}
		if page.NextCursor == "" {
			return items, nil
		}
		if page.NextCursor == cursor {
			return nil, errors.New("repeated page cursor")
		}
		cursor = page.NextCursor
	}
}
func (c *Connection) snapshot(ctx context.Context) Update {
	snapshot := &Snapshot{}
	if err := c.API.Do(ctx, http.MethodGet, "/health", nil, &snapshot.Health); err != nil {
		return Update{Err: err}
	}
	if err := c.API.Do(ctx, http.MethodGet, "/cluster", nil, &snapshot.Cluster); err != nil {
		var failure *client.Error
		if errors.As(err, &failure) && failure.Status == http.StatusServiceUnavailable && snapshot.Health.Checks["cluster"].Status == "not-applicable" {
			return Update{Snapshot: snapshot}
		}
		return Update{Err: err}
	}
	var workers sync.WaitGroup
	failures := make([]error, 5)
	workers.Go(func() { snapshot.Hosts, failures[0] = readCollection[domain.Member](ctx, c.API, "/hosts") })
	workers.Go(func() { snapshot.Tasks, failures[1] = readCollection[domain.Task](ctx, c.API, "/tasks") })
	workers.Go(func() { snapshot.Repositories, failures[2] = readCollection[Document](ctx, c.API, "/repos") })
	workers.Go(func() { snapshot.Recipes, failures[3] = readCollection[Document](ctx, c.API, "/recipes") })
	workers.Go(func() { snapshot.Configurations, failures[4] = readCollection[Document](ctx, c.API, "/configurations") })
	workers.Wait()
	for i, err := range failures {
		if err != nil {
			snapshot.Warnings = append(snapshot.Warnings, fmt.Sprintf("%s: %v", []string{"Hosts", "Tasks", "Repositories", "Recipes", "Configurations"}[i], err))
		}
	}
	return Update{Snapshot: snapshot}
}

func (c *Connection) watch(ctx context.Context, selection Selection, emit func(Update)) {
	var workers sync.WaitGroup
	defer workers.Wait()
	if selection.Task != "" {
		workers.Go(func() {
			err := c.API.Watch(ctx, "/tasks/"+url.PathEscape(selection.Task)+"/events", "", func(event client.Event) error {
				var task domain.Task
				if err := json.Unmarshal(event.Data, &task); err != nil {
					return err
				}
				emit(Update{Task: &task})
				return nil
			})
			if err != nil && ctx.Err() == nil {
				emit(Update{Err: err, Resource: "tasks/" + selection.Task})
			}
		})
	}
	kind, id := "hosts", selection.Host
	if selection.Task != "" {
		kind, id = "tasks", selection.Task
	}
	if id == "" {
		return
	}
	err := c.API.Watch(ctx, "/"+kind+"/"+url.PathEscape(id)+"/logs/stream", "", func(event client.Event) error {
		emit(Update{Resource: kind + "/" + id, EventID: event.ID, Log: event.Data})
		return nil
	})
	if err != nil && ctx.Err() == nil {
		emit(Update{Err: err, Resource: kind + "/" + id})
	}
}

func (c *Connection) Run(ctx context.Context, emit func(Update)) {
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	refresh := make(chan struct{}, 1)
	send := func(update Update) {
		if ctx.Err() == nil {
			emit(update)
		}
	}
	workers.Go(func() {
		timer := time.NewTicker(5 * time.Second)
		defer timer.Stop()
		for {
			send(c.snapshot(ctx))
			select {
			case <-ctx.Done():
				return
			case <-refresh:
			case <-timer.C:
			}
		}
	})
	var stopSelection context.CancelFunc
	for {
		select {
		case <-ctx.Done():
			return
		case selection := <-c.selection:
			if stopSelection != nil {
				stopSelection()
			}
			life, stop := context.WithCancel(ctx)
			stopSelection = stop
			workers.Go(func() {
				c.watch(life, selection, func(update Update) {
					if life.Err() == nil {
						send(update)
					}
				})
			})
		case request := <-c.commands:
			if request.Target == "refresh" {
				select {
				case refresh <- struct{}{}:
				default:
				}
				send(Update{Submitted: true})
				continue
			}
			var response json.RawMessage
			var body any
			if len(request.Body) > 0 {
				body = request.Body
			}
			err := c.API.Do(ctx, request.Method, request.Path, body, &response)
			send(Update{Target: request.Target, Response: response, Err: err, Submitted: true})
			select {
			case refresh <- struct{}{}:
			default:
			}
		}
	}
}
