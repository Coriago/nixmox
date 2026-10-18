package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"nixmox/internal/addhost"
	"nixmox/internal/cluster"
	"nixmox/internal/hostinfo"
	"nixmox/internal/jobs"
	"nixmox/internal/messaging"
	"nixmox/internal/natsx"
	"nixmox/internal/portal"
	"nixmox/internal/statefile"
)

var onboardingID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

type onboardingSession struct {
	ReviewedSOPS       *addhost.SOPSConfig `json:"reviewed_sops,omitempty"`
	AuthorizeRecipient bool                `json:"authorize_recipient,omitempty"`
	Existing           bool                `json:"existing,omitempty"`
	ID                 string              `json:"id"`
	NodeID             string              `json:"node_id"`
	Address            string              `json:"address"`
	Info               installerInfo       `json:"info"`
	Values             map[string]any      `json:"values"`
	Result             addhost.Result      `json:"result"`
	Digest             string              `json:"digest"`
	Revision           string              `json:"revision"`
	JobID              string              `json:"job_id"`
	Phase              string              `json:"phase"`
	Plan               addhost.InstallPlan `json:"plan"`
	InstallJob         string              `json:"install_job"`
	Installed          bool                `json:"installed"`
	Completed          bool                `json:"completed"`
}

func (s *Server) sessionPath(id string) string { return s.Config.DataDirPath("onboarding", id+".json") }
func (s *Server) loadSession(id string) (onboardingSession, error) {
	var session onboardingSession
	if !onboardingID.MatchString(id) {
		return session, fmt.Errorf("invalid session")
	}
	data, err := os.ReadFile(s.sessionPath(id))
	if err != nil {
		return session, err
	}
	err = json.Unmarshal(data, &session)
	return session, err
}
func (s *Server) saveSession(session onboardingSession) error {
	return statefile.Write(s.sessionPath(session.ID), session)
}
func (s *Server) writerError() error {
	c := s.Config.AddHost
	if c == nil {
		return fmt.Errorf("services.nixmox.addHost is not configured on this node")
	}
	if s.Config.InstallerMode {
		return fmt.Errorf("installers cannot prepare repositories")
	}
	if s.Git == nil || s.Git.State().RepoURL == "" {
		return fmt.Errorf("configure a GitOps repository with write access")
	}
	if err := addhost.ValidateRepositoryURL(s.Git.State().RepoURL); err != nil {
		return err
	}
	if c.Form["Name"].Type != "hostname" || c.Form["Drive"].Type != "disk" {
		return fmt.Errorf("onboarding requires Name (hostname) and Drive (disk) form fields")
	}
	if c.SOPS != nil {
		if c.AgeKeyFile == "" {
			return fmt.Errorf("configure addHost.ageKeyFile with an authorized age identity")
		}
		if strings.HasPrefix(c.AgeKeyFile, "/nix/store/") {
			return fmt.Errorf("age identity must not be stored in the Nix store")
		}
		f, err := os.Open(c.AgeKeyFile)
		if err != nil {
			return fmt.Errorf("configured age identity is not readable")
		}
		f.Close()
	}
	return nil
}
func (s *Server) mountOnboarding(p *portal.Portal) {
	s.mountInstaller()
	s.Mux.Post("/api/v1/hosts/{nodeID}/onboarding/configurations", s.listExistingConfigurations)
	s.Mux.Post("/api/v1/hosts/{nodeID}/onboarding/select", s.selectExistingConfiguration)
	if s.Runner != nil {
		s.Runner.Register("host-onboard", s.runOnboarding)
	}
	s.Mux.Get("/add-host", func(w http.ResponseWriter, r *http.Request) { p.AddHost(w) })
	s.Mux.Get("/api/v1/onboarding/coordinators", func(w http.ResponseWriter, r *http.Request) {
		type candidate struct {
			NodeID string `json:"node_id"`
			Ready  bool   `json:"ready"`
			Error  string `json:"error"`
		}
		candidates := []candidate{}
		local := candidate{NodeID: s.Config.Hostname, Ready: true}
		if err := s.writerError(); err != nil {
			local.Ready = false
			local.Error = err.Error()
		}
		candidates = append(candidates, local)
		if s.View != nil {
			for _, peer := range s.View.List() {
				if !peer.Online || peer.NodeID == s.Config.Hostname {
					continue
				}
				var remote candidate
				ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
				err := s.requestPeer(ctx, peer, "GET", "/api/v1/onboarding/capability", nil, &remote)
				cancel()
				if err == nil {
					candidates = append(candidates, remote)
				}
			}
		}
		respond(w, 200, candidates)
	})
	s.Mux.Get("/api/v1/onboarding/capability", func(w http.ResponseWriter, r *http.Request) {
		problem := ""
		if err := s.writerError(); err != nil {
			problem = err.Error()
		}
		respond(w, 200, map[string]any{"node_id": s.Config.Hostname, "ready": problem == "", "error": problem})
	})
	s.Mux.Get("/api/v1/hosts/{nodeID}/onboarding", func(w http.ResponseWriter, r *http.Request) {
		if s.forward(w, r, chi.URLParam(r, "nodeID")) {
			return
		}
		if err := s.writerError(); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		respond(w, 200, map[string]any{"form": s.Config.AddHost.Form, "repository": s.Git.State().RepoURL, "branch": s.Git.State().Branch, "sops": s.Config.AddHost.SOPS != nil})
	})
	s.Mux.Get("/api/v1/hosts/{nodeID}/onboarding/sessions", func(w http.ResponseWriter, r *http.Request) {
		if s.forward(w, r, chi.URLParam(r, "nodeID")) {
			return
		}
		sessions := []onboardingSession{}
		files, _ := filepath.Glob(s.Config.DataDirPath("onboarding", "*.json"))
		for _, file := range files {
			id := strings.TrimSuffix(filepath.Base(file), ".json")
			session, err := s.loadSession(id)
			if err == nil {
				sessions = append(sessions, session)
			}
		}
		respond(w, 200, sessions)
	})
	s.Mux.Get("/api/v1/hosts/{nodeID}/onboarding/sessions/{sessionID}", func(w http.ResponseWriter, r *http.Request) {
		if s.forward(w, r, chi.URLParam(r, "nodeID")) {
			return
		}
		session, err := s.loadSession(chi.URLParam(r, "sessionID"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		var j jobs.Job
		if session.JobID != "" {
			j, _ = s.Store.Get(session.JobID)
		}
		respond(w, 200, map[string]any{"session": session, "job": j, "log": tailFile(j.LogPath, 128<<10)})
	})
	s.Mux.Post("/api/v1/hosts/{nodeID}/onboarding/inspect", s.inspectOnboarding)
	s.Mux.Post("/api/v1/hosts/{nodeID}/onboarding/preview", s.previewOnboarding)
	s.Mux.Post("/api/v1/hosts/{nodeID}/onboarding/start", s.startOnboarding)
}
func (s *Server) onboardingGate(w http.ResponseWriter, r *http.Request) bool {
	if s.forward(w, r, chi.URLParam(r, "nodeID")) {
		return false
	}
	if err := s.writerError(); err != nil {
		http.Error(w, err.Error(), 409)
		return false
	}
	if s.clusterState().Status != "clustered" {
		http.Error(w, "create or join a cluster before setting up a host", 409)
		return false
	}
	host, _, err := net.SplitHostPort(s.Config.PeerAddress())
	if err != nil || host == "" || host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback() {
		http.Error(w, "configure a reachable cluster advertise address", 409)
		return false
	}
	return true
}
func (s *Server) inspectOnboarding(w http.ResponseWriter, r *http.Request) {
	if !s.onboardingGate(w, r) {
		return
	}
	var req struct {
		NodeID  string `json:"node_id"`
		Address string `json:"address"`
	}
	if err := decode(r, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	found := false
	if s.Discoveries != nil {
		for _, peer := range s.Discoveries.List() {
			if peer.NodeID == req.NodeID && peer.Address == req.Address && peer.Cluster == "" && peer.Installer {
				found = true
			}
		}
	}
	if !found {
		http.Error(w, "installer discovery expired or address changed; refresh discovery", 409)
		return
	}
	var info installerInfo
	if err := s.installerRequest(r.Context(), req.NodeID, req.Address, "GET", "/api/v1/installer/info", nil, &info); err != nil {
		http.Error(w, err.Error(), 502)
		return
	}
	if info.NodeID != req.NodeID || info.BootID == "" || info.Recipient == "" {
		http.Error(w, "installer identity mismatch", 409)
		return
	}
	session := onboardingSession{ID: messaging.NewID(), NodeID: req.NodeID, Address: req.Address, Info: info, Phase: "Configure host"}
	if err := s.saveSession(session); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	respond(w, 200, session)
}
func (s *Server) sessionIdle(session onboardingSession) error {
	if session.Completed {
		return fmt.Errorf("host setup is already complete")
	}
	if session.JobID != "" {
		j, err := s.Store.Get(session.JobID)
		if err != nil {
			return err
		}
		if j.State == "pending" || j.State == "building" || j.State == "applying" {
			return fmt.Errorf("host setup is already running")
		}
	}
	return nil
}
func (s *Server) previewOnboarding(w http.ResponseWriter, r *http.Request) {
	if !s.onboardingGate(w, r) {
		return
	}
	s.onboardingMu.Lock()
	defer s.onboardingMu.Unlock()
	var req struct {
		Session string         `json:"session"`
		Values  map[string]any `json:"values"`
	}
	if err := decode(r, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	session, err := s.loadSession(req.Session)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.sessionIdle(session); err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	if session.Revision != "" {
		http.Error(w, "published setup is immutable; retry its recorded revision", 409)
		return
	}
	input := addhost.Input{Values: req.Values, Hardware: session.Info.Hardware, Disks: session.Info.Disks, ClusterAddress: s.Config.PeerAddress()}
	result, err := addhost.Generate(*s.Config.AddHost, input)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// Normalize defaults needed for the assigned host configuration and drive.
	values := map[string]any{}
	for key, field := range s.Config.AddHost.Form {
		values[key] = field.Default
	}
	for key, value := range req.Values {
		values[key] = value
	}
	name, _ := values["Name"].(string)
	drive, _ := values["Drive"].(string)
	if name == s.Config.Hostname {
		http.Error(w, "hostname already belongs to the coordinator", 409)
		return
	}
	if _, ok := s.peer(name); ok {
		http.Error(w, "hostname already belongs to a cluster member", 409)
		return
	}
	var selected addhost.Disk
	for _, d := range session.Info.Disks {
		if d.Path == drive {
			selected = d
		}
	}
	if selected.Path == "" {
		http.Error(w, "select an installation drive", 400)
		return
	}
	session.Values = values
	session.Result = result
	sum := sha256.Sum256([]byte(result.Path + "\x00" + result.Content))
	session.Digest = hex.EncodeToString(sum[:])
	session.Plan = addhost.InstallPlan{ID: session.ID, Repository: s.Git.State().RepoURL, Branch: s.Git.State().Branch, Hostname: name, FlakeDirectory: s.Config.FlakeDirectory, Drive: selected, ClusterAddress: s.Config.PeerAddress(), BootID: session.Info.BootID, Recipient: session.Info.Recipient}
	session.Phase = "Review configuration and confirm erasure"
	if err := s.saveSession(session); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	respond(w, 200, session)
}
func (s *Server) startOnboarding(w http.ResponseWriter, r *http.Request) {
	if !s.onboardingGate(w, r) {
		return
	}
	s.onboardingMu.Lock()
	defer s.onboardingMu.Unlock()
	var req struct {
		Session            string `json:"session"`
		Digest             string `json:"digest"`
		Confirm            string `json:"confirm"`
		AlternativeNetwork bool   `json:"alternative_network"`
	}
	if err := decode(r, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	session, err := s.loadSession(req.Session)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.sessionIdle(session); err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	if session.Digest == "" || req.Digest != session.Digest || req.Confirm != session.Plan.Drive.Path {
		http.Error(w, "review the current preview and confirm the exact drive path", 400)
		return
	}
	if !session.Info.Network.Transferable && !req.AlternativeNetwork {
		http.Error(w, "confirm an alternative network connection plan", 400)
		return
	}
	session.Plan.AlternativeNetwork = req.AlternativeNetwork
	if err := s.saveSession(session); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	// The task reads this file after taking onboardingMu, so JobID is persisted first.
	j, err := s.Runner.Submit(jobs.Job{Kind: "host-onboard", Host: s.Config.Hostname, Name: "Set up " + session.Plan.Hostname, Args: []string{session.ID}})
	if err != nil {
		http.Error(w, err.Error(), 503)
		return
	}
	session.JobID = j.ID
	if err := s.saveSession(session); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	respond(w, 202, map[string]any{"session": session, "job": j})
}
func (s *Server) installerRequest(ctx context.Context, nodeID, address, method, path string, body, out any) error {
	if s.Bus == nil || nodeID == "" {
		return fmt.Errorf("installer requires the cluster NATS transport")
	}
	connector, ok := s.Cluster.(interface{ ConnectInstaller(string) error })
	if !ok {
		return fmt.Errorf("temporary NATS participation is unavailable")
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	if err := connector.ConnectInstaller(net.JoinHostPort(host, fmt.Sprint(natsx.InstallerPort))); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	// Connecting the leaf and arming its durable consumer are asynchronous.
	// Read-only readiness queries may be retried; mutations use Bus.Command's
	// durable receipts and never fall back to an installer HTTP connection.
	for {
		probe, stop := context.WithTimeout(ctx, time.Second)
		_, err = s.Bus.Query(probe, messaging.Request{Target: nodeID, Method: "GET", Path: "/api/v1/host"})
		stop()
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("temporary installer NATS connection: %w", ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
	return s.requestPeer(ctx, cluster.HostPayload{NodeID: nodeID}, method, path, body, out)
}
func (s *Server) runOnboarding(ctx context.Context, j *jobs.Job, out io.Writer) error {
	s.onboardingMu.Lock()
	session, err := s.loadSession(j.Args[0])
	s.onboardingMu.Unlock()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Hour)
	defer cancel()
	phase := func(value string) error {
		session.Phase = value
		j.Phase = value
		fmt.Fprintln(out, value)
		if err := s.Store.Update(*j); err != nil {
			return err
		}
		return s.saveSession(session)
	}
	if err := s.writerError(); err != nil {
		return err
	}
	if session.InstallJob != "" {
		if old, ok := s.peer(session.Plan.Hostname); ok && old.Online && old.AgeRecipient == session.Info.Recipient {
			if err := s.removeTemporary(ctx, session); err != nil {
				return err
			}
			session.Completed = true
			return phase("Installed host is reachable and joined; temporary installer removed")
		}
		if session.Installed {
			return s.finishOnboarding(ctx, &session, j, out)
		}
		var previous struct {
			Job jobs.Job `json:"job"`
		}
		if err := s.installerRequest(ctx, session.NodeID, session.Address, "GET", "/api/v1/installer/jobs/"+session.InstallJob, nil, &previous); err != nil {
			return err
		}
		if previous.Job.State == "applied" {
			session.Installed = true
			if err := s.saveSession(session); err != nil {
				return err
			}
			return s.finishOnboarding(ctx, &session, j, out)
		}
		if previous.Job.State != "failed" {
			return fmt.Errorf("previous installation is still active; refusing a second erase")
		}
	}
	if err := phase("Checking selected installer identity and disk"); err != nil {
		return err
	}
	var info installerInfo
	if err := s.installerRequest(ctx, session.NodeID, session.Address, "GET", "/api/v1/installer/info?session="+session.ID, nil, &info); err != nil {
		return err
	}
	if info.NodeID != session.NodeID || info.BootID != session.Info.BootID || info.Recipient != session.Info.Recipient {
		return fmt.Errorf("installer identity changed; select it again")
	}
	valid := false
	for _, d := range info.Disks {
		if d == session.Plan.Drive {
			valid = true
		}
	}
	if !valid {
		return fmt.Errorf("drive changed or became busy; refresh the installer before proceeding")
	}
	if err := s.installerRequest(ctx, session.NodeID, session.Address, "POST", "/api/v1/installer/reserve", session.Plan, nil); err != nil {
		return err
	}
	if session.AuthorizeRecipient {
		if s.Config.AddHost.SOPS == nil || session.ReviewedSOPS == nil {
			return fmt.Errorf("SOPS configuration was removed; review again")
		}
		if err := phase("Authorizing replacement installer in SOPS and pushing Git"); err != nil {
			return err
		}
		repo := addhost.Repository{URL: session.Plan.Repository, Branch: session.Plan.Branch, Directory: s.Config.DataDirPath("onboarding", session.ID+"-"+j.ID), SSHKey: s.Config.SSHKeyPath, Netrc: s.Config.NetrcPath, AgeKey: s.Config.AddHost.AgeKeyFile}
		sha, err := repo.AuthorizeExisting(ctx, *session.ReviewedSOPS, session.Revision, session.Info.Recipient, out)
		if err != nil {
			return err
		}
		session.Revision, session.Plan.Revision, session.AuthorizeRecipient = sha, sha, false
		if err := s.saveSession(session); err != nil {
			return err
		}
	}
	if session.Revision == "" {
		if err := phase("Generating configuration, updating SOPS, and pushing Git"); err != nil {
			return err
		}
		// Render again from the reviewed observations and require an identical preview.
		result, err := addhost.Generate(*s.Config.AddHost, addhost.Input{Values: session.Values, Hardware: session.Info.Hardware, Disks: session.Info.Disks, ClusterAddress: session.Plan.ClusterAddress})
		if err != nil {
			return err
		}
		if result != session.Result {
			return fmt.Errorf("template changed after preview; review the configuration again")
		}
		repo := addhost.Repository{URL: session.Plan.Repository, Branch: session.Plan.Branch, Directory: s.Config.DataDirPath("onboarding", session.ID+"-"+j.ID), SSHKey: s.Config.SSHKeyPath, Netrc: s.Config.NetrcPath, AgeKey: s.Config.AddHost.AgeKeyFile}
		sha, err := repo.Publish(ctx, *s.Config.AddHost, result, session.Info.Recipient, out)
		if err != nil {
			return err
		}
		session.Revision = sha
		session.Plan.Revision = sha
		j.GitSHA = sha
		if err := s.saveSession(session); err != nil {
			return err
		}
	}
	j.GitSHA = session.Revision
	if err := phase("Installer pulling the exact published revision"); err != nil {
		return err
	}
	var pull jobs.Job
	if err := s.installerRequest(ctx, session.NodeID, session.Address, "POST", "/api/v1/installer/pull", session.Plan, &pull); err != nil {
		return err
	}
	if err := s.waitInstallerJob(ctx, session.NodeID, session.Address, pull.ID, out); err != nil {
		return err
	}
	if err := phase("Installing locally on the selected disk"); err != nil {
		return err
	}
	var install jobs.Job
	if err := s.installerRequest(ctx, session.NodeID, session.Address, "POST", "/api/v1/installer/install", map[string]any{"id": session.ID, "confirm": session.Plan.Drive.Path, "alternative_network": session.Plan.AlternativeNetwork}, &install); err != nil {
		return err
	}
	session.InstallJob = install.ID
	if err := s.saveSession(session); err != nil {
		return err
	}
	if err := s.waitInstallerJob(ctx, session.NodeID, session.Address, install.ID, out); err != nil {
		return err
	}
	session.Installed = true
	if err := s.saveSession(session); err != nil {
		return err
	}
	return s.finishOnboarding(ctx, &session, j, out)
}

func (s *Server) finishOnboarding(ctx context.Context, session *onboardingSession, j *jobs.Job, out io.Writer) error {
	session.Phase = "Rebooting into the installed configuration; waiting for cluster join"
	j.Phase = session.Phase
	if err := s.Store.Update(*j); err != nil {
		return err
	}
	if err := s.saveSession(*session); err != nil {
		return err
	}
	fmt.Fprintln(out, session.Phase)
	// A lost reply can mean the reboot already began. Never erase again here.
	if err := s.installerRequest(ctx, session.NodeID, session.Address, "POST", "/api/v1/installer/reboot", map[string]string{"job": session.InstallJob}, nil); err != nil {
		fmt.Fprintln(out, "Installer reboot reply unavailable; checking installed-host reachability")
	}
	deadline := time.NewTimer(20 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()
	for {
		if peer, ok := s.peer(session.Plan.Hostname); ok && peer.Online && peer.AgeRecipient == session.Info.Recipient {
			var h hostinfo.HostInfo
			if err := s.requestPeer(ctx, peer, "GET", "/api/v1/host", nil, &h); err == nil && h.Hostname == session.Plan.Hostname && h.AgeRecipient == session.Info.Recipient {
				if err := s.removeTemporary(ctx, *session); err != nil {
					return err
				}
				session.Completed = true
				session.Phase = "Installed host is reachable and joined; temporary installer removed"
				j.Phase = session.Phase
				fmt.Fprintln(out, session.Phase)
				return s.saveSession(*session)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("installed host did not join within 20 minutes; check boot and networking before retrying")
		case <-tick.C:
		}
	}
}
func (s *Server) removeTemporary(ctx context.Context, session onboardingSession) error {
	connector, ok := s.Cluster.(interface {
		DisconnectInstaller(context.Context, string, string) error
	})
	if !ok {
		return fmt.Errorf("temporary NATS removal is unavailable")
	}
	host, _, err := net.SplitHostPort(session.Address)
	if err != nil {
		return err
	}
	if err := connector.DisconnectInstaller(ctx, session.NodeID, net.JoinHostPort(host, fmt.Sprint(natsx.InstallerPort))); err != nil {
		return err
	}
	if s.Bus == nil {
		return fmt.Errorf("NATS transport unavailable")
	}
	if err := s.Bus.ForgetTemporary(ctx, session.NodeID); err != nil {
		return err
	}
	if s.View != nil {
		s.View.Remove(session.NodeID)
	}
	return nil
}

func (s *Server) waitInstallerJob(ctx context.Context, nodeID, address, id string, out io.Writer) error {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	previous := ""
	for {
		var result struct {
			Job jobs.Job `json:"job"`
			Log string   `json:"log"`
		}
		if err := s.installerRequest(ctx, nodeID, address, "GET", "/api/v1/installer/jobs/"+id, nil, &result); err != nil {
			return fmt.Errorf("installer task status unavailable (inspect before retrying): %w", err)
		}
		if result.Log != previous {
			if strings.HasPrefix(result.Log, previous) {
				io.WriteString(out, strings.TrimPrefix(result.Log, previous))
			} else {
				io.WriteString(out, result.Log)
			}
			previous = result.Log
		}
		if result.Job.State == "applied" {
			return nil
		}
		if result.Job.State == "failed" {
			return fmt.Errorf("installer task failed: %s", result.Job.Error)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
