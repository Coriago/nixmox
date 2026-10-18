package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"nixmox/internal/addhost"
	"nixmox/internal/hostinfo"
	"nixmox/internal/jobs"
	"nixmox/internal/statefile"
)

type installerInfo struct {
	addhost.Observation
	NodeID    string          `json:"node_id"`
	BootID    string          `json:"boot_id"`
	Recipient string          `json:"recipient"`
	Network   addhost.Network `json:"network"`
}

func (s *Server) installerInfo(ctx context.Context, retryDrive string) (installerInfo, error) {
	if !s.Config.InstallerMode {
		return installerInfo{}, fmt.Errorf("node is not a temporary installer")
	}
	observation, err := addhost.InspectRetry(ctx, retryDrive)
	if err != nil {
		return installerInfo{}, err
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return installerInfo{}, err
	}
	h := hostinfo.Get(s.Config)
	if h.AgeRecipient == "" {
		return installerInfo{}, fmt.Errorf("installer SSH host age recipient unavailable: %s", h.AgeRecipientError)
	}
	return installerInfo{Observation: observation, NodeID: s.Config.Hostname, BootID: strings.TrimSpace(string(boot)), Recipient: h.AgeRecipient, Network: addhost.InspectNetwork(ctx)}, nil
}
func (s *Server) installPath(id string) string {
	return s.Config.DataDirPath("onboarding-installer", id)
}
func (s *Server) loadInstall(id string) (addhost.InstallPlan, error) {
	var p addhost.InstallPlan
	if !onboardingID.MatchString(id) {
		return p, fmt.Errorf("invalid session")
	}
	data, err := os.ReadFile(filepath.Join(s.installPath(id), "plan.json"))
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(data, &p)
	return p, err
}
func (s *Server) installerGate(w http.ResponseWriter, r *http.Request) bool {
	if !s.Config.InstallerMode {
		http.Error(w, "local installation is only enabled on temporary installers", 403)
		return false
	}
	return true
}
func (s *Server) mountInstaller() {
	s.Mux.Post("/api/v1/installer/reset-plan", s.resetInstallerPlan)
	s.Mux.Get("/api/v1/installer/info", func(w http.ResponseWriter, r *http.Request) {
		if !s.installerGate(w, r) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		retryDrive := ""
		if id := r.URL.Query().Get("session"); id != "" {
			if plan, err := s.loadInstall(id); err == nil {
				retryDrive = plan.Drive.Path
			}
		}
		info, err := s.installerInfo(ctx, retryDrive)
		if err != nil {
			http.Error(w, err.Error(), 503)
			return
		}
		respond(w, 200, info)
	})
	s.Mux.Post("/api/v1/installer/reserve", func(w http.ResponseWriter, r *http.Request) {
		if !s.installerGate(w, r) {
			return
		}
		s.onboardingMu.Lock()
		defer s.onboardingMu.Unlock()
		var p addhost.InstallPlan
		if err := decode(r, &p); err != nil || !onboardingID.MatchString(p.ID) {
			http.Error(w, "invalid installation plan", 400)
			return
		}
		boot, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
		if p.BootID != strings.TrimSpace(string(boot)) {
			http.Error(w, "installer rebooted; select it again", 409)
			return
		}
		reservation := s.Config.DataDirPath("onboarding-installer", "reservation")
		old, err := os.ReadFile(reservation)
		if err != nil && !os.IsNotExist(err) {
			http.Error(w, err.Error(), 500)
			return
		}
		if len(old) > 0 && string(old) != p.ID {
			http.Error(w, "installer already reserved by another onboarding session", 409)
			return
		}
		if err := os.MkdirAll(filepath.Dir(reservation), 0700); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if err := os.WriteFile(reservation, []byte(p.ID), 0600); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		respond(w, 200, map[string]string{"status": "reserved"})
	})
	if s.Runner != nil {
		s.Runner.Register("host-pull", s.pullInstall)
		s.Runner.Register("host-install", s.runLocalInstall)
	}
	s.Mux.Post("/api/v1/installer/pull", func(w http.ResponseWriter, r *http.Request) {
		if !s.installerGate(w, r) {
			return
		}
		s.onboardingMu.Lock()
		defer s.onboardingMu.Unlock()
		var p addhost.InstallPlan
		if err := decode(r, &p); err != nil || !onboardingID.MatchString(p.ID) {
			http.Error(w, "invalid plan", 400)
			return
		}
		reservation, _ := os.ReadFile(s.Config.DataDirPath("onboarding-installer", "reservation"))
		if err := addhost.ValidateRepositoryURL(p.Repository); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if string(reservation) != p.ID {
			http.Error(w, "installer reservation does not match", 409)
			return
		}
		if !s.installerIdle(w) {
			return
		}
		if old, err := s.loadInstall(p.ID); err == nil {
			if old.Revision != p.Revision || old.Hostname != p.Hostname || old.Drive != p.Drive || old.Repository != p.Repository || old.ClusterAddress != p.ClusterAddress || old.FlakeDirectory != p.FlakeDirectory || old.Recipient != p.Recipient || old.BootID != p.BootID {
				http.Error(w, "retry must use the recorded installation plan and revision", 409)
				return
			}
		} else if !os.IsNotExist(err) {
			http.Error(w, err.Error(), 500)
			return
		}
		if err := statefile.Write(filepath.Join(s.installPath(p.ID), "plan.json"), p); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		j, err := s.Runner.Submit(jobs.Job{Kind: "host-pull", Host: s.Config.Hostname, Args: []string{p.ID}})
		if err != nil {
			http.Error(w, err.Error(), 503)
			return
		}
		respond(w, 202, j)
	})
	s.Mux.Post("/api/v1/installer/install", func(w http.ResponseWriter, r *http.Request) {
		if !s.installerGate(w, r) {
			return
		}
		s.onboardingMu.Lock()
		defer s.onboardingMu.Unlock()
		var req struct {
			ID                 string `json:"id"`
			Confirm            string `json:"confirm"`
			AlternativeNetwork bool   `json:"alternative_network"`
		}
		if err := decode(r, &req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		p, err := s.loadInstall(req.ID)
		if err != nil {
			http.Error(w, err.Error(), 404)
			return
		}
		if req.Confirm != p.Drive.Path {
			http.Error(w, "confirm the exact drive before erasure", 400)
			return
		}
		if !s.installerIdle(w) {
			return
		}
		if _, err := os.Stat(filepath.Join(s.installPath(p.ID), "pulled")); err != nil {
			http.Error(w, "pull has not completed", 409)
			return
		}
		p.AlternativeNetwork = req.AlternativeNetwork
		if err := statefile.Write(filepath.Join(s.installPath(p.ID), "plan.json"), p); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		j, err := s.Runner.Submit(jobs.Job{Kind: "host-install", Host: s.Config.Hostname, Args: []string{p.ID}, GitSHA: p.Revision})
		if err != nil {
			http.Error(w, err.Error(), 503)
			return
		}
		respond(w, 202, j)
	})
	s.Mux.Get("/api/v1/installer/jobs/{jobID}", func(w http.ResponseWriter, r *http.Request) {
		if !s.installerGate(w, r) {
			return
		}
		j, err := s.Store.Get(chi.URLParam(r, "jobID"))
		if err != nil || (j.Kind != "host-pull" && j.Kind != "host-install") {
			http.NotFound(w, r)
			return
		}
		log := tailFile(j.LogPath, 64<<10)
		respond(w, 200, map[string]any{"job": j, "log": log})
	})
	s.Mux.Post("/api/v1/installer/reboot", func(w http.ResponseWriter, r *http.Request) {
		if !s.installerGate(w, r) {
			return
		}
		var req struct {
			Job string `json:"job"`
		}
		if err := decode(r, &req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		j, err := s.Store.Get(req.Job)
		if err != nil || j.Kind != "host-install" || j.State != "applied" {
			http.Error(w, "installation has not completed successfully", 409)
			return
		}
		if err := exec.CommandContext(r.Context(), "shutdown", "-r", "+1", "nixmox installation complete").Run(); err != nil {
			http.Error(w, "schedule reboot failed", 500)
			return
		}
		respond(w, 202, map[string]string{"status": "reboot scheduled"})
	})
}

// Reset only preparation state. Once an installation job has been submitted,
// changing its plan is deliberately forbidden even if that job later failed.
func (s *Server) resetInstallerPlan(w http.ResponseWriter, r *http.Request) {
	if !s.installerGate(w, r) {
		return
	}
	s.onboardingMu.Lock()
	defer s.onboardingMu.Unlock()
	var plan addhost.InstallPlan
	if err := decode(r, &plan); err != nil || !onboardingID.MatchString(plan.ID) {
		http.Error(w, "invalid plan", 400)
		return
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil || strings.TrimSpace(string(boot)) != plan.BootID {
		http.Error(w, "installer identity changed; select it again", 409)
		return
	}
	reservation, err := os.ReadFile(s.Config.DataDirPath("onboarding-installer", "reservation"))
	if err != nil || string(reservation) != plan.ID {
		http.Error(w, "installer reservation does not match", 409)
		return
	}
	if !s.installerIdle(w) {
		return
	}
	list, err := s.Store.List()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	for _, job := range list {
		if job.Kind == "host-install" && len(job.Args) > 0 && job.Args[0] == plan.ID {
			http.Error(w, "installation has already started; cannot replace its plan", 409)
			return
		}
	}
	old, err := s.loadInstall(plan.ID)
	if os.IsNotExist(err) {
		respond(w, 200, map[string]string{"status": "ready"})
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if old.BootID != plan.BootID || old.Recipient != plan.Recipient {
		http.Error(w, "installer identity does not match recorded plan", 409)
		return
	}
	archive := s.Config.DataDirPath("onboarding-installer", "archive")
	if err := os.MkdirAll(archive, 0700); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if err := os.Rename(s.installPath(plan.ID), filepath.Join(archive, fmt.Sprintf("%s-%d", plan.ID, time.Now().UnixNano()))); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	respond(w, 200, map[string]string{"status": "archived"})
}
func (s *Server) installerIdle(w http.ResponseWriter) bool {
	list, err := s.Store.List()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return false
	}
	for _, j := range list {
		if (j.Kind == "host-pull" || j.Kind == "host-install") && (j.State == "pending" || j.State == "building" || j.State == "applying" || j.State == "cancelling") {
			http.Error(w, "installer already has an active task", 409)
			return false
		}
	}
	return true
}
func (s *Server) pullInstall(ctx context.Context, j *jobs.Job, out io.Writer) error {
	p, err := s.loadInstall(j.Args[0])
	if err != nil {
		return err
	}
	if s.Git == nil {
		return fmt.Errorf("installer GitOps is unavailable")
	}
	pin, ok := s.Git.(interface {
		PinInstall(context.Context, string, string) error
	})
	if !ok {
		return fmt.Errorf("installer cannot pin Git revisions")
	}
	dir := filepath.Join(s.installPath(p.ID), "repo")
	if _, err := os.Stat(filepath.Join(s.installPath(p.ID), "pulled")); err == nil {
		j.GitSHA = p.Revision
		fmt.Fprintln(out, "Reusing recorded pulled revision", p.Revision)
		return nil
	}
	if err := s.Git.Configure(p.Repository, p.Branch); err != nil {
		return err
	}
	fmt.Fprintln(out, "Pulling published host configuration through local GitOps")
	if err := pin.PinInstall(ctx, p.Revision, dir); err != nil {
		return err
	}
	if _, err := addhost.CheckInstall(ctx, dir, p); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(s.installPath(p.ID), "pulled"), []byte(p.Revision), 0600); err != nil {
		return err
	}
	j.GitSHA = p.Revision
	fmt.Fprintln(out, "Pinned installation revision", p.Revision)
	return nil
}
func (s *Server) runLocalInstall(ctx context.Context, j *jobs.Job, out io.Writer) error {
	if !s.Config.InstallerMode {
		return fmt.Errorf("local installation disabled")
	}
	p, err := s.loadInstall(j.Args[0])
	if err != nil {
		return err
	}
	boot, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if p.BootID != strings.TrimSpace(string(boot)) {
		return fmt.Errorf("installer boot identity changed")
	}
	marker, err := os.ReadFile(filepath.Join(s.installPath(p.ID), "pulled"))
	if err != nil || string(marker) != p.Revision {
		return fmt.Errorf("recorded pull is missing or mismatched")
	}
	j.GitSHA = p.Revision
	return addhost.LocalInstall(ctx, filepath.Join(s.installPath(p.ID), "repo"), p, out)
}
func tailFile(path string, limit int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return ""
	}
	if stat.Size() > limit {
		_, _ = f.Seek(-limit, io.SeekEnd)
	}
	data, _ := io.ReadAll(io.LimitReader(f, limit))
	return string(data)
}
