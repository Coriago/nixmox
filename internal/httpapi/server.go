// Package httpapi adapts HTTP and HTML to the common service.
package httpapi

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/presentation"
	"nixmox/internal/service"
)

//go:embed assets/*
var assets embed.FS

type Server struct {
	service   *service.Service
	templates *template.Template
	cluster   ClusterReader
}
type ClusterReader interface {
	Status() (domain.ClusterStatus, error)
}

func New(s *service.Service, clusters ...ClusterReader) http.Handler {
	a := &Server{service: s, templates: template.Must(template.New("network.html").Funcs(template.FuncMap{"networkText": presentation.Network, "taskText": presentation.Task}).ParseFS(assets, "assets/*.html"))}
	if len(clusters) > 0 {
		a.cluster = clusters[0]
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/cluster/status", func(w http.ResponseWriter, r *http.Request) {
		if a.cluster == nil {
			writeError(w, domain.Err("UNAVAILABLE", "Cluster status unavailable.", 503), "")
			return
		}
		v, err := a.cluster.Status()
		respond(w, v, err)
	})
	mux.HandleFunc("GET /api/v1/identity", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.Identity()) })
	mux.HandleFunc("GET /api/v1/network/status", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.Network()) })
	mux.HandleFunc("POST /api/v1/commands", a.command)
	mux.HandleFunc("GET /api/v1/commands/{id}", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Receipt(r.PathValue("id"))
		respond(w, v, err)
	})
	mux.HandleFunc("GET /api/v1/tasks", a.tasks)
	mux.HandleFunc("GET /api/v1/tasks/{id}", func(w http.ResponseWriter, r *http.Request) { v, err := s.Task(r.PathValue("id")); respond(w, v, err) })
	mux.HandleFunc("GET /api/v1/tasks/{id}/logs", a.logs)
	mux.HandleFunc("GET /api/v1/tasks/{id}/logs/download", a.downloadLogs)
	mux.HandleFunc("GET /api/v1/events", a.events)
	mux.HandleFunc("GET /ui/network", a.networkPage)
	mux.HandleFunc("GET /ui/cluster", a.clusterPage)
	mux.HandleFunc("GET /ui/cluster/status", a.clusterFragment)
	mux.HandleFunc("GET /ui/network/status", a.networkFragment)
	mux.HandleFunc("GET /ui/tasks/{id}", a.taskPage)
	mux.HandleFunc("GET /ui/tasks/{id}/status", a.taskFragment)
	mux.HandleFunc("POST /ui/commands", a.formCommand)
	sub, _ := fs.Sub(assets, "assets")
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServer(http.FS(sub))))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "/ui/network", http.StatusSeeOther) })
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, domain.Err("NOT_FOUND", "Route not found.", 404), "")
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'")
		if r.Method == http.MethodPost {
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				writeError(w, domain.Err("FORBIDDEN", "Cross-origin mutation refused.", 403), "")
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || u.Scheme != "http" || u.Host != r.Host {
					writeError(w, domain.Err("FORBIDDEN", "Cross-origin mutation refused.", 403), "")
					return
				}
			}
		}
		mux.ServeHTTP(w, r)
	})
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, err error, id string) {
	var e *domain.Error
	if !errors.As(err, &e) {
		e = domain.Err("UNAVAILABLE", "The requested data is unavailable.", 503)
	}
	writeJSON(w, e.Status, struct {
		Error     *domain.Error `json:"error"`
		CommandID string        `json:"command_id,omitempty"`
	}{e, id})
}
func respond(w http.ResponseWriter, v any, err error) {
	if err != nil {
		writeError(w, err, "")
		return
	}
	writeJSON(w, 200, v)
}
func (a *Server) command(w http.ResponseWriter, r *http.Request) {
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" {
		writeError(w, domain.Err("INVALID_REQUEST", "Content-Type must be application/json.", 400), "")
		return
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, domain.MaxCommandBytes))
	if err != nil {
		writeError(w, domain.Err("INVALID_REQUEST", "Command exceeds 256 KiB or could not be read.", 400), "")
		return
	}
	var c domain.Command
	if domain.StrictJSON(b, &c) != nil {
		writeError(w, domain.Err("INVALID_REQUEST", "Invalid command fields or JSON.", 400), "")
		return
	}
	receipt, err := a.service.Admit(c)
	if err != nil {
		writeError(w, err, c.ID)
		return
	}
	writeJSON(w, 202, receipt)
}
func taskQuery(r *http.Request) (service.TaskFilter, string, string, error) {
	q := r.URL.Query()
	for key, values := range q {
		if len(values) != 1 {
			return service.TaskFilter{}, "", "", domain.Err("INVALID_QUERY", "Duplicate query parameter.", 400)
		}
		switch key {
		case "host_id", "kind", "state", "since", "until", "limit", "cursor":
		default:
			return service.TaskFilter{}, "", "", domain.Err("INVALID_QUERY", "Unknown query parameter.", 400)
		}
	}
	return service.TaskFilter{HostID: q.Get("host_id"), Kind: q.Get("kind"), State: q.Get("state"), Since: q.Get("since"), Until: q.Get("until")}, q.Get("limit"), q.Get("cursor"), nil
}
func (a *Server) tasks(w http.ResponseWriter, r *http.Request) {
	f, l, c, err := taskQuery(r)
	if err != nil {
		writeError(w, err, "")
		return
	}
	v, err := a.service.ListTasks(f, l, c)
	respond(w, v, err)
}
func (a *Server) logs(w http.ResponseWriter, r *http.Request) {
	_, err := a.service.Task(r.PathValue("id"))
	if err != nil {
		writeError(w, err, "")
		return
	}
	q := r.URL.Query()
	for k, v := range q {
		if (k != "offset" && k != "limit") || len(v) != 1 {
			writeError(w, domain.Err("INVALID_QUERY", "Invalid log query.", 400), "")
			return
		}
	}
	offset := 0
	limit := 65536
	if q.Has("offset") {
		offset, err = strconv.Atoi(q.Get("offset"))
		if err != nil || offset < 0 {
			writeError(w, domain.Err("INVALID_QUERY", "Invalid offset.", 400), "")
			return
		}
	}
	if q.Has("limit") {
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || limit < 1 || limit > 1048576 {
			writeError(w, domain.Err("INVALID_QUERY", "Invalid limit.", 400), "")
			return
		}
	}
	chunk, err := a.service.Logs(r.PathValue("id"), offset, limit)
	respond(w, chunk, err)
}
func (a *Server) downloadLogs(w http.ResponseWriter, r *http.Request) {
	log, _, err := a.service.LogSnapshot(r.PathValue("id"))
	if err != nil {
		writeError(w, err, "")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Log-Retained-From", strconv.Itoa(log.Start))
	w.Header().Set("X-Log-End-Offset", strconv.Itoa(log.End()))
	w.Header().Set("X-Log-Truncated", strconv.FormatBool(log.Start > 0))
	w.WriteHeader(200)
	_, _ = w.Write(log.Data)
}
func (a *Server) events(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	topics := q.Get("topics")
	if topics == "" {
		topics = "identity,network.status,tasks"
	}
	selected := map[string]bool{}
	for _, topic := range strings.Split(topics, ",") {
		switch topic {
		case "identity", "network.status", "tasks":
			selected[topic] = true
		default:
			writeError(w, domain.Err("INVALID_QUERY", "Unsupported event topic.", 400), "")
			return
		}
	}
	for k, v := range q {
		if k != "topics" || len(v) != 1 {
			writeError(w, domain.Err("INVALID_QUERY", "Invalid event selector.", 400), "")
			return
		}
	}
	ch, unsub, err := a.service.Subscribe()
	if err != nil {
		writeError(w, err, "")
		return
	}
	defer unsub()
	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(kind string, v any) error {
		if err := rc.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return err
		}
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if _, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, b); err != nil {
			return err
		}
		return rc.Flush()
	}
	snapshot := func() map[string]any {
		v := map[string]any{"type": "snapshot", "observed_at": time.Now().UTC()}
		if selected["identity"] {
			v["identity"] = a.service.Identity()
		}
		if selected["network.status"] {
			v["network.status"] = a.service.Network()
		}
		if selected["tasks"] {
			p, _ := a.service.ListTasks(service.TaskFilter{}, "100", "")
			v["tasks"] = map[string]any{"items": p.Items, "next_cursor": p.NextCursor, "href": "/api/v1/tasks"}
		}
		return v
	}
	if send("snapshot", snapshot()) != nil {
		return
	}
	versions := map[string]int64{}
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			if send("snapshot", snapshot()) != nil {
				return
			}
		case <-ch:
			if selected["tasks"] {
				page, _ := a.service.ListTasks(service.TaskFilter{}, "100", "")
				latest := make(map[string]int64, len(page.Items))
				for _, task := range page.Items {
					latest[task.ID] = task.Version
					if task.Version <= versions[task.ID] {
						continue
					}
					if send("resource.updated", map[string]any{
						"type": "resource.updated", "resource": "task", "id": task.ID,
						"version": task.Version, "observed_at": task.UpdatedAt,
					}) != nil {
						return
					}
				}
				versions = latest
			}
			if send("snapshot", snapshot()) != nil {
				return
			}
		}
	}
}

type view struct {
	CommandID   string
	TargetID    string
	Network     domain.NetworkStatus
	Tasks       []domain.Task
	Receipt     *domain.Receipt
	Error       string
	Task        *domain.Task
	Controls    []controlView
	Cluster     *domain.ClusterStatus
	ClusterPage bool
}

type controlView struct {
	CommandID string
	TaskID    string
	TargetID  string
	Version   int64
	Action    presentation.TaskAction
}

func (a *Server) view() view {
	p, _ := a.service.ListTasks(service.TaskFilter{}, "20", "")
	return view{CommandID: domain.NewID("cmd_"), TargetID: a.service.Identity().NodeID, Network: a.service.Network(), Tasks: p.Items}
}
func (a *Server) render(w http.ResponseWriter, r *http.Request, name string, status int, v view) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Vary", "HX-Request")
	w.WriteHeader(status)
	_ = a.templates.ExecuteTemplate(w, name, v)
}
func (a *Server) networkPage(w http.ResponseWriter, r *http.Request) {
	name := "network.html"
	if r.Header.Get("HX-Request") == "true" {
		name = "content"
	}
	a.render(w, r, name, 200, a.view())
}
func (a *Server) networkFragment(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "status", 200, a.view())
}
func (a *Server) clusterView() view {
	v := a.view()
	v.ClusterPage = true
	if a.cluster == nil {
		v.Error = "Cluster status is unavailable."
		return v
	}
	c, err := a.cluster.Status()
	if err != nil {
		v.Error = err.Error()
		return v
	}
	v.Cluster = &c
	return v
}
func (a *Server) clusterPage(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "network.html", 200, a.clusterView())
}
func (a *Server) clusterFragment(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "cluster-status", 200, a.clusterView())
}
func (a *Server) taskView(id string) (view, error) {
	task, err := a.service.Task(id)
	if err != nil {
		return view{}, err
	}
	v := view{Task: &task}
	for _, action := range presentation.TaskActions(task) {
		v.Controls = append(v.Controls, controlView{CommandID: domain.NewID("cmd_"), TaskID: task.ID, TargetID: task.TargetID, Version: task.Version, Action: action})
	}
	return v, nil
}
func (a *Server) taskPage(w http.ResponseWriter, r *http.Request) {
	v, err := a.taskView(r.PathValue("id"))
	if err != nil {
		writeError(w, err, "")
		return
	}
	name := "network.html"
	if r.Header.Get("HX-Request") == "true" {
		name = "task-content"
	}
	a.render(w, r, name, 200, v)
}
func (a *Server) taskFragment(w http.ResponseWriter, r *http.Request) {
	v, err := a.taskView(r.PathValue("id"))
	if err != nil {
		writeError(w, err, "")
		return
	}
	a.render(w, r, "task-status", 200, v)
}
func (a *Server) formCommand(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, domain.MaxCommandBytes)
	if err := r.ParseForm(); err != nil {
		a.render(w, r, "receipt", 400, view{Error: "Invalid form."})
		return
	}
	for k, v := range r.PostForm {
		if (k != "id" && k != "target_id" && k != "type" && k != "task_id" && k != "expected_version" && k != "name") || len(v) != 1 {
			a.render(w, r, "receipt", 422, view{Error: "Invalid command fields."})
			return
		}
	}
	c := domain.Command{ID: r.PostForm.Get("id"), TargetID: r.PostForm.Get("target_id"), Type: r.PostForm.Get("type"), Payload: json.RawMessage(`{}`)}
	if c.Type == "cluster.create" {
		c.Payload, _ = json.Marshal(map[string]string{"name": r.PostForm.Get("name")})
	} else if r.PostForm.Has("name") {
		a.render(w, r, "receipt", 422, view{Error: "Unexpected cluster fields."})
		return
	}
	if c.Type == "task.cancel" || c.Type == "task.retry" {
		version, err := strconv.ParseInt(r.PostForm.Get("expected_version"), 10, 64)
		if err != nil || version <= 0 {
			a.render(w, r, "receipt", 422, view{Error: "A positive reviewed task version is required."})
			return
		}
		c.Payload, _ = json.Marshal(map[string]string{"task_id": r.PostForm.Get("task_id")})
		c.ExpectedVersions = map[string]int64{"task:" + r.PostForm.Get("task_id"): version}
	} else if r.PostForm.Has("task_id") || r.PostForm.Has("expected_version") {
		a.render(w, r, "receipt", 422, view{Error: "Unexpected task fields."})
		return
	}
	receipt, err := a.service.Admit(c)
	if err != nil {
		var e *domain.Error
		status := 503
		message := "UNAVAILABLE"
		if errors.As(err, &e) {
			status = e.Status
			message = e.Error()
		}
		a.render(w, r, "receipt", status, view{Error: message})
		return
	}
	w.Header().Set("HX-Trigger", "commandAccepted")
	if r.Header.Get("HX-Request") != "true" {
		v := a.view()
		v.Receipt = &receipt
		a.render(w, r, "network.html", 202, v)
		return
	}
	a.render(w, r, "receipt", 202, view{Receipt: &receipt})
}
