package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"nixmox/internal/addhost"
)

func (s *Server) existingCheckout(ctx context.Context, revision string) (string, string, func(), error) {
	parent := s.Config.DataDirPath("onboarding-review")
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", "", func() {}, err
	}
	dir, err := os.MkdirTemp(parent, "review-")
	if err != nil {
		return "", "", func() {}, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	state := s.Git.State()
	repo := addhost.Repository{URL: state.RepoURL, Branch: state.Branch, Directory: filepath.Join(dir, "repo"), SSHKey: s.Config.SSHKeyPath, Netrc: s.Config.NetrcPath}
	sha, err := repo.CheckoutExisting(ctx, revision)
	return repo.Directory, sha, cleanup, err
}

func (s *Server) listExistingConfigurations(w http.ResponseWriter, r *http.Request) {
	if !s.onboardingGate(w, r) {
		return
	}
	var req struct {
		Revision string `json:"revision"`
	}
	if err := decode(r, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	dir, sha, cleanup, err := s.existingCheckout(ctx, req.Revision)
	defer cleanup()
	if err != nil {
		http.Error(w, err.Error(), 422)
		return
	}
	names, err := addhost.ExistingConfigurations(ctx, dir, s.Config.FlakeDirectory)
	if err != nil {
		http.Error(w, err.Error(), 422)
		return
	}
	respond(w, 200, map[string]any{"revision": sha, "configurations": names})
}

func (s *Server) selectExistingConfiguration(w http.ResponseWriter, r *http.Request) {
	if !s.onboardingGate(w, r) {
		return
	}
	s.onboardingMu.Lock()
	defer s.onboardingMu.Unlock()
	var req struct {
		Session            string `json:"session"`
		Revision           string `json:"revision"`
		Hostname           string `json:"hostname"`
		Drive              string `json:"drive"`
		AuthorizeRecipient bool   `json:"authorize_recipient"`
	}
	if err := decode(r, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if req.Revision == "" {
		http.Error(w, "load configurations and select a revision first", 400)
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
	if session.InstallJob != "" || session.Installed {
		http.Error(w, "installation has already started; the recorded plan cannot be replaced", 409)
		return
	}
	if req.Hostname == s.Config.Hostname {
		http.Error(w, "hostname already belongs to the coordinator", 409)
		return
	}
	if _, ok := s.peer(req.Hostname); ok {
		http.Error(w, "hostname already belongs to a cluster member", 409)
		return
	}
	var disk addhost.Disk
	for _, d := range session.Info.Disks {
		if d.Path == req.Drive && d.Whole && !d.Installer {
			disk = d
		}
	}
	if disk.Path == "" {
		http.Error(w, "select an installation drive", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	dir, sha, cleanup, err := s.existingCheckout(ctx, req.Revision)
	defer cleanup()
	if err != nil {
		http.Error(w, err.Error(), 422)
		return
	}
	state := s.Git.State()
	plan := addhost.InstallPlan{ID: session.ID, Repository: state.RepoURL, Branch: state.Branch, Revision: sha, Hostname: req.Hostname, Drive: disk, FlakeDirectory: s.Config.FlakeDirectory, ClusterAddress: s.Config.PeerAddress(), BootID: session.Info.BootID, Recipient: session.Info.Recipient}
	nm, err := addhost.CheckInstall(ctx, dir, plan)
	if err != nil {
		http.Error(w, err.Error(), 422)
		return
	}
	if session.Info.Network.WiFi && !nm {
		http.Error(w, "selected configuration disables NetworkManager; configure networking before selecting this host", 422)
		return
	}
	if err := addhost.CheckExistingRecipients(dir, s.Config.AddHost.SOPS, plan.Recipient); err != nil && !req.AuthorizeRecipient {
		http.Error(w, err.Error(), 422)
		return
	}
	if session.Revision != "" && (session.Plan.Revision != plan.Revision || session.Plan.Hostname != plan.Hostname || session.Plan.Drive != plan.Drive || session.Plan.Repository != plan.Repository || session.Plan.ClusterAddress != plan.ClusterAddress || session.Plan.FlakeDirectory != plan.FlakeDirectory) {
		if err := s.installerRequest(ctx, session.NodeID, session.Address, "POST", "/api/v1/installer/reset-plan", session.Plan, nil); err != nil {
			http.Error(w, fmt.Sprintf("could not archive the previous installer plan: %v. Older installer images require clearing the failed plan on the installer before changing revisions", err), 409)
			return
		}
	}
	if req.AuthorizeRecipient {
		c := s.Config.AddHost.SOPS
		if c == nil {
			http.Error(w, "No SOPS files are configured", 422)
			return
		}
		if err := addhost.UpdateRules(dir, *c, plan.Recipient); err != nil {
			http.Error(w, err.Error(), 422)
			return
		}
	}
	data, _ := json.MarshalIndent(struct {
		Plan      addhost.InstallPlan `json:"plan"`
		Authorize bool                `json:"authorize_recipient"`
		SOPS      *addhost.SOPSConfig `json:"sops,omitempty"`
	}{plan, req.AuthorizeRecipient, s.Config.AddHost.SOPS}, "", "  ")
	sum := sha256.Sum256(data)
	session.Existing = true
	session.AuthorizeRecipient = req.AuthorizeRecipient
	session.ReviewedSOPS = s.Config.AddHost.SOPS
	session.Revision, session.Plan, session.Digest = sha, plan, hex.EncodeToString(sum[:])
	session.Result = addhost.Result{Path: "nixosConfigurations." + req.Hostname, Content: string(data)}
	session.JobID = ""
	session.Phase = "Review existing configuration and confirm erasure; no Git changes"
	if session.AuthorizeRecipient {
		session.Phase = "Review SOPS authorization for replacement installer and confirm erasure"
	}
	if err := s.saveSession(session); err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	respond(w, 200, session)
}
