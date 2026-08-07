package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"nixmox/internal/hostinfo"
	"nixmox/internal/jobs"
	"nixmox/internal/portal"
)

func (s *Server) MountHandlers(p *portal.Portal, auth Middleware) {
	s.Mux.Use(auth.Wrap)

	s.Mux.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	s.Mux.Get("/api/v1/hosts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		hosts := []hostinfo.HostInfo{hostinfo.Get(s.Config)}
		json.NewEncoder(w).Encode(hosts)
	})

	s.Mux.Get("/api/v1/host", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(hostinfo.Get(s.Config))
	})

	s.Mux.Get("/api/v1/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":       "unclustered",
			"cluster_name": "",
			"member_count": 1,
		})
	})

	s.Mux.Get("/api/v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		list, err := s.Store.List()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(list)
	})

	s.Mux.Get("/api/v1/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "id")
		job, err := s.Store.Get(id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}

		logData, _ := os.ReadFile(job.LogPath)
		lines := strings.Split(string(logData), "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		if len(lines) > 100 {
			lines = lines[len(lines)-100:]
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"id":         job.ID,
			"kind":       job.Kind,
			"host":       job.Host,
			"args":       job.Args,
			"state":      job.State,
			"created_at": job.CreatedAt,
			"updated_at": job.UpdatedAt,
			"log":        lines,
		})
	})

	s.Mux.Post("/api/v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Kind string   `json:"kind"`
			Args []string `json:"args"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if req.Kind != "echo" && req.Kind != "dry-build" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "kind not supported"})
			return
		}

		now := time.Now().Truncate(time.Millisecond)
		job := jobs.Job{
			ID:        fmt.Sprintf("job-%d", now.UnixNano()),
			Kind:      req.Kind,
			Host:      hostinfo.Get(s.Config).Hostname,
			Args:      req.Args,
			State:     "pending",
			CreatedAt: now,
			UpdatedAt: now,
		}

		if err := s.Store.Create(job); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		job, _ = s.Store.Get(job.ID)

		if err := s.Runner.Start(context.Background(), job); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"id": job.ID})
	})

	s.Mux.Get("/", func(w http.ResponseWriter, r *http.Request) {
		hosts := []hostinfo.HostInfo{hostinfo.Get(s.Config)}
		p.Index(w, hosts)
	})

	s.Mux.Get("/jobs", func(w http.ResponseWriter, r *http.Request) {
		list, _ := s.Store.List()
		p.Jobs(w, list)
	})

	s.Mux.Get("/partials/hosts", func(w http.ResponseWriter, r *http.Request) {
		hosts := []hostinfo.HostInfo{hostinfo.Get(s.Config)}
		p.HostsPartial(w, hosts)
	})

	s.Mux.Get("/partials/jobs", func(w http.ResponseWriter, r *http.Request) {
		list, _ := s.Store.List()
		p.JobsPartial(w, list)
	})

	s.Mux.Handle("/assets/*", p.AssetsHandler())
}

func (s *Server) gitOpsView() portal.GitOpsView {
	view := portal.GitOpsView{
		PollInterval:     s.Config.GitPollInterval,
		SSHKeyPath:       s.Config.SSHKeyPath,
		NetrcPath:        s.Config.NetrcPath,
		AllowConfigWrite: s.Config.AllowConfigWrite,
	}
	if s.Git != nil {
		view.State = s.Git.State()
	}
	return view
}

func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

func (s *Server) MountGitOps(p *portal.Portal) {
	s.Mux.Get("/api/v1/gitops", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		v := s.gitOpsView()
		json.NewEncoder(w).Encode(map[string]interface{}{
			"repo_url":           v.State.RepoURL,
			"branch":             v.State.Branch,
			"last_pulled_sha":    v.State.LastPulledSHA,
			"update_available":   v.State.UpdateAvailable,
			"poll_interval":      v.PollInterval,
			"ssh_key_path":       v.SSHKeyPath,
			"netrc_path":         v.NetrcPath,
			"allow_config_write": v.AllowConfigWrite,
		})
	})

	s.Mux.Put("/api/v1/gitops", func(w http.ResponseWriter, r *http.Request) {
		if !s.Config.AllowConfigWrite {
			http.Error(w, "config write disabled (set allow_config_write / NIXMOX_ALLOW_CONFIG_WRITE)", http.StatusMethodNotAllowed)
			return
		}
		if s.Git == nil {
			http.Error(w, "gitops not available", http.StatusServiceUnavailable)
			return
		}
		var url, branch string
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			var req struct {
				RepoURL string `json:"repo_url"`
				Branch  string `json:"branch"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			url, branch = req.RepoURL, req.Branch
		} else {
			url, branch = r.FormValue("repo_url"), r.FormValue("branch")
		}
		if url == "" {
			http.Error(w, "repo_url is required", http.StatusBadRequest)
			return
		}
		if branch == "" {
			branch = "main"
		}
		if err := s.Git.Configure(url, branch); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if isHTMX(r) {
			p.GitOpsPartial(w, s.gitOpsView())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(s.Git.State())
	})

	s.Mux.Post("/api/v1/gitops/pull", func(w http.ResponseWriter, r *http.Request) {
		if s.Git == nil {
			http.Error(w, "gitops not available", http.StatusServiceUnavailable)
			return
		}
		sha, err := s.Git.Ensure(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if isHTMX(r) {
			p.GitOpsPartial(w, s.gitOpsView())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"last_pulled_sha": sha})
	})

	s.Mux.Get("/gitops", func(w http.ResponseWriter, r *http.Request) {
		p.GitOps(w, s.gitOpsView())
	})

	s.Mux.Get("/partials/gitops", func(w http.ResponseWriter, r *http.Request) {
		p.GitOpsPartial(w, s.gitOpsView())
	})
}
