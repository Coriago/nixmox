package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"nixmox/internal/jobs"
	"nixmox/internal/portal"
	"strings"
)

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
	s.Runner.Register("git-pull", func(ctx context.Context, j *jobs.Job, out io.Writer) error {
		if s.Git == nil {
			return fmt.Errorf("gitops not available")
		}
		fmt.Fprintln(out, "Pulling configured GitOps repository")
		sha, err := s.Git.Ensure(ctx)
		if err == nil {
			j.GitSHA = sha
			fmt.Fprintln(out, "Pulled", sha)
		}
		return err
	})
	s.Mux.Get("/api/v1/gitops", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		v := s.gitOpsView()
		json.NewEncoder(w).Encode(map[string]interface{}{
			"repo_url":           v.State.RepoURL,
			"branch":             v.State.Branch,
			"flake_directory":    s.Config.FlakeDirectory,
			"flake_attribute":    s.Config.FlakeAttribute,
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
		j, err := s.Runner.Submit(jobs.Job{Kind: "git-pull", Host: s.Config.Hostname})
		if err != nil {
			http.Error(w, err.Error(), 503)
			return
		}
		respond(w, 202, j)
	})

	s.Mux.Get("/gitops", func(w http.ResponseWriter, r *http.Request) {
		p.GitOps(w, s.gitOpsView())
	})

	s.Mux.Get("/partials/gitops", func(w http.ResponseWriter, r *http.Request) {
		p.GitOpsPartial(w, s.gitOpsView())
	})
}
