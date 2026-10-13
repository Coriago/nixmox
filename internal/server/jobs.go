package server

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"nixmox/internal/jobs"
	"nixmox/internal/portal"
)

func (s *Server) jobTarget(w http.ResponseWriter, r *http.Request) bool {
	id := chi.URLParam(r, "nodeID")
	return id != "" && s.forward(w, r, id)
}

func (s *Server) mountJobs(p *portal.Portal) {
	for _, prefix := range []string{"/api/v1/jobs", "/api/v1/hosts/{nodeID}/jobs"} {
		s.Mux.Get(prefix, func(w http.ResponseWriter, r *http.Request) {
			if s.jobTarget(w, r) {
				return
			}
			list, err := s.Store.List()
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			if list == nil {
				list = []jobs.Job{}
			}
			respond(w, 200, list)
		})
		s.Mux.Get(prefix+"/{id}", func(w http.ResponseWriter, r *http.Request) {
			if s.jobTarget(w, r) {
				return
			}
			j, err := s.Store.Get(chi.URLParam(r, "id"))
			if err != nil {
				http.Error(w, "unknown job", 404)
				return
			}
			respond(w, 200, struct {
				jobs.Job
				Log []string `json:"log"`
			}{j, readLogTail(j, 500)})
		})
		s.Mux.Post(prefix, s.submitJob)
	}
	for _, path := range []string{"/jobs", "/hosts/{nodeID}/jobs"} {
		s.Mux.Get(path, func(w http.ResponseWriter, r *http.Request) {
			if s.jobTarget(w, r) {
				return
			}
			list, err := s.Store.List()
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			p.Jobs(w, list)
		})
		s.Mux.Get(path+"/{id}", func(w http.ResponseWriter, r *http.Request) {
			if s.jobTarget(w, r) {
				return
			}
			j, err := s.Store.Get(chi.URLParam(r, "id"))
			if err != nil {
				http.Error(w, "unknown job", 404)
				return
			}
			p.JobDetail(w, portal.JobDetailView{Job: j, Log: readLogTail(j, 500)})
		})
	}
	for _, path := range []string{"/partials/jobs", "/partials/hosts/{nodeID}/jobs"} {
		s.Mux.Get(path, func(w http.ResponseWriter, r *http.Request) {
			if s.jobTarget(w, r) {
				return
			}
			list, err := s.Store.List()
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			p.JobsPartial(w, list)
		})
		s.Mux.Get(path+"/{id}/log", func(w http.ResponseWriter, r *http.Request) {
			if s.jobTarget(w, r) {
				return
			}
			j, err := s.Store.Get(chi.URLParam(r, "id"))
			if err != nil {
				http.Error(w, "unknown job", 404)
				return
			}
			p.JobLogPartial(w, portal.JobDetailView{Job: j, Log: readLogTail(j, 500)})
		})
	}
}

func (s *Server) submitJob(w http.ResponseWriter, r *http.Request) {
	if s.jobTarget(w, r) {
		return
	}
	var req struct {
		Kind      string   `json:"kind"`
		Name      string   `json:"name"`
		Command   string   `json:"command"`
		Args      []string `json:"args"`
		Directory string   `json:"directory"`
	}
	if err := decode(r, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	var j jobs.Job
	var err error
	switch req.Kind {
	case "command":
		if !s.Config.EnableCustomTasks {
			http.Error(w, "custom tasks disabled (enable_custom_tasks / NIXMOX_ENABLE_CUSTOM_TASKS)", 403)
			return
		}
		if strings.TrimSpace(req.Command) == "" || strings.ContainsRune(req.Command, 0) || strings.HasPrefix(req.Command, "-") {
			http.Error(w, "a command executable is required", 400)
			return
		}
		if req.Directory != "" && !filepath.IsAbs(req.Directory) {
			http.Error(w, "directory must be absolute", 400)
			return
		}
		for _, a := range req.Args {
			if strings.ContainsRune(a, 0) {
				http.Error(w, "arguments cannot contain NUL", 400)
				return
			}
		}
		j, err = s.Runner.Submit(jobs.Job{Kind: req.Kind, Host: s.Config.Hostname, Name: req.Name, Command: req.Command, Args: req.Args, Directory: req.Directory})
	case "echo":
		j, err = s.Runner.Submit(jobs.Job{Kind: req.Kind, Host: s.Config.Hostname, Args: req.Args})
	case "dry-build":
		builder, ok := s.Apply.(interface {
			DryBuild(context.Context) (jobs.Job, error)
		})
		if !ok {
			http.Error(w, "dry-build unavailable", 503)
			return
		}
		if len(req.Args) > 0 {
			http.Error(w, "dry-build uses the configured GitOps flake; custom arguments are unsupported", 400)
			return
		}
		j, err = builder.DryBuild(r.Context())
	default:
		http.Error(w, "kind not supported", 400)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	// Keep the original local endpoint's response compatible.
	status := http.StatusAccepted
	if r.URL.Path == "/api/v1/jobs" {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if status == http.StatusOK {
		json.NewEncoder(w).Encode(map[string]string{"id": j.ID})
		return
	}
	json.NewEncoder(w).Encode(j)
}
