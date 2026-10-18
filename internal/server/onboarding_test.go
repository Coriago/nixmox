package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"nixmox/internal/addhost"
	"nixmox/internal/cluster"
	"nixmox/internal/gitops"
	"nixmox/internal/hostinfo"
	"nixmox/internal/jobs"
)

func onboardingServer(t *testing.T) *Server {
	t.Helper()
	s := operationServer(t)
	s.Config.AdvertiseAddr = "192.0.2.1"
	supervisor := cluster.NewSupervisor(s.Config, s.Config.DataDir, "127.0.0.1")
	s.Cluster = supervisor
	if err := supervisor.CreateCluster(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	s.Git = &fakeGitOps{state: gitops.State{RepoURL: "https://example.test/config.git", Branch: "main"}}
	template := filepath.Join(t.TempDir(), "host.gotmpl")
	if err := os.WriteFile(template, []byte(`{ networking.hostName = {{ .Name }}; diskLayout.device = {{ .Drive }}; imports = [ ({{ .Hardware }}) ]; }`), 0600); err != nil {
		t.Fatal(err)
	}
	s.Config.AddHost = &addhost.Config{Path: "hosts/{{ .Name }}.nix", Template: template, Form: map[string]addhost.Field{"Name": {Type: "hostname", Default: "newhost"}, "Drive": {Type: "disk", Required: true}, "Modules": {Type: "multiselect", Values: []string{"base"}, Default: []string{"base"}}}}
	return s
}
func sessionFixture(t *testing.T, s *Server) onboardingSession {
	t.Helper()
	session := onboardingSession{ID: "test-session", NodeID: "installer", Address: "192.0.2.2:8080", Info: installerInfo{NodeID: "installer", BootID: "boot", Recipient: "age1test", Observation: addhost.Observation{Hardware: "{ ... }: {}", Disks: []addhost.Disk{{Path: "/dev/vda", Whole: true, Model: "disk", Serial: "123", Size: 1024}}}, Network: addhost.Network{Transferable: true}}}
	if err := s.saveSession(session); err != nil {
		t.Fatal(err)
	}
	return session
}
func TestOnboardingBrowserAndValidation(t *testing.T) {
	s := onboardingServer(t)
	sessionFixture(t, s)
	if w := call(s, "GET", "/add-host", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "onboarding-form") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call(s, "GET", "/api/v1/onboarding/coordinators", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"ready":true`) {
		t.Fatal(w.Body.String())
	}
	base := "/api/v1/hosts/local/onboarding"
	if w := call(s, "POST", base+"/inspect", `{"node_id":"fake","address":"127.0.0.1:1"}`); w.Code != 409 {
		t.Fatal("accepted undiscovered target", w.Code)
	}
	for _, values := range []string{`{"Name":"../bad","Drive":"/dev/vda"}`, `{"Name":"ok","Drive":"/dev/vdb"}`, `{"Name":"ok","Drive":"/dev/vda","Modules":["evil"]}`} {
		if w := call(s, "POST", base+"/preview", `{"session":"test-session","values":`+values+`}`); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := call(s, "POST", base+"/preview", `{"session":"test-session","values":{"Drive":"/dev/vda"}}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var session onboardingSession
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if session.Plan.Hostname != "newhost" || session.Result.Path != "hosts/newhost.nix" || session.Digest == "" {
		t.Fatal(session)
	}
	for _, body := range []string{`{"session":"test-session","confirm":"/dev/vda","digest":"stale"}`, fmt.Sprintf(`{"session":"test-session","confirm":"/dev/vdb","digest":%q}`, session.Digest)} {
		if w := call(s, "POST", base+"/start", body); w.Code != 400 {
			t.Fatal("accepted bad erase confirmation", w.Code)
		}
	}
	s.Config.AddHost.SOPS = &addhost.SOPSConfig{RulesFile: ".sops.yaml"}
	if w := call(s, "GET", base, ""); w.Code != 409 || !strings.Contains(w.Body.String(), "ageKeyFile") {
		t.Fatal(w.Code, w.Body.String())
	}
}
func TestTemporaryInstallerCannotVoteOrInstallWithoutPull(t *testing.T) {
	s := operationServer(t)
	s.Config.InstallerMode = true
	for _, path := range []string{"/api/v1/cluster/create", "/api/v1/cluster/join", "/api/v1/cluster/join/initiate"} {
		if w := call(s, "POST", path, `{}`); w.Code != 403 {
			t.Fatal(path, w.Code)
		}
	}
	if w := call(s, "POST", "/api/v1/installer/install", `{"id":"missing","confirm":"/dev/vda"}`); w.Code != 404 {
		t.Fatal(w.Code)
	}
	s.Config.InstallerMode = false
	if w := call(s, "GET", "/api/v1/installer/info", ""); w.Code != 404 {
		t.Fatal("installed node exposed install workflow", w.Code)
	}
}
func TestOnboardingRetryKeepsRevisionAndStopsOnPullFailure(t *testing.T) {
	s := onboardingServer(t)
	session := sessionFixture(t, s)
	session.Revision = strings.Repeat("a", 40)
	session.Plan = addhost.InstallPlan{ID: session.ID, Revision: session.Revision, Hostname: "newhost", Drive: session.Info.Disks[0], Repository: "test", Branch: "main"}
	calls := []string{}
	installer := installerBus(t, s, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		switch r.URL.Path {
		case "/api/v1/installer/info":
			respond(w, 200, session.Info)
		case "/api/v1/installer/reserve":
			respond(w, 200, map[string]string{"status": "reserved"})
		case "/api/v1/installer/pull":
			var plan addhost.InstallPlan
			_ = json.NewDecoder(r.Body).Decode(&plan)
			if plan.Revision != session.Revision {
				t.Error("retry changed revision")
			}
			respond(w, 202, jobs.Job{ID: "pull"})
		case "/api/v1/installer/jobs/pull":
			respond(w, 200, map[string]any{"job": jobs.Job{State: "failed", Error: "read access denied"}, "log": "pull failed"})
		default:
			t.Error("unexpected install after pull failure", r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	}))
	session.Address = installer
	if err := s.saveSession(session); err != nil {
		t.Fatal(err)
	}
	j := jobs.Job{ID: "test-job", Host: "local", Kind: "host-onboard", Args: []string{session.ID}}
	if err := s.Store.Create(j); err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	err := s.runOnboarding(context.Background(), &j, &log)
	if err == nil || !strings.Contains(err.Error(), "read access denied") {
		t.Fatal(err)
	}
	if len(calls) != 4 {
		t.Fatal(calls)
	}
}

func TestOnboardingPublishesPullsInstallsAndVerifiesJoin(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git required")
	}
	s := onboardingServer(t)
	root := t.TempDir()
	source := filepath.Join(root, "source")
	remote := filepath.Join(root, "remote.git")
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, b)
		}
	}
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	run(source, "init", "-b", "main")
	run(source, "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--allow-empty", "-m", "seed")
	run(root, "clone", "--bare", source, remote)
	s.Git = &fakeGitOps{state: gitops.State{RepoURL: remote, Branch: "main"}}
	session := sessionFixture(t, s)
	w := call(s, "POST", "/api/v1/hosts/local/onboarding/preview", `{"session":"test-session","values":{"Drive":"/dev/vda"}}`)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	session, _ = s.loadSession(session.ID)
	var pulled string
	calls := []string{}
	var installer string
	installer = installerBus(t, s, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		switch r.URL.Path {
		case "/api/v1/installer/info":
			respond(w, 200, session.Info)
		case "/api/v1/installer/reserve":
			respond(w, 200, map[string]string{"status": "reserved"})
		case "/api/v1/installer/pull":
			var plan addhost.InstallPlan
			if err := json.NewDecoder(r.Body).Decode(&plan); err != nil {
				t.Error(err)
			}
			pulled = plan.Revision
			cmd := exec.Command("git", "--git-dir", remote, "show", pulled+":hosts/newhost.nix")
			if b, err := cmd.Output(); err != nil || !strings.Contains(string(b), `"newhost"`) {
				t.Error("pull sent before host configuration was published", err)
			}
			respond(w, 202, jobs.Job{ID: "pull"})
		case "/api/v1/installer/jobs/pull":
			respond(w, 200, map[string]any{"job": jobs.Job{State: "applied", GitSHA: pulled}, "log": "pulled\n"})
		case "/api/v1/installer/install":
			if pulled == "" {
				t.Error("installation preceded pull")
			}
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req["confirm"] != "/dev/vda" {
				t.Error("missing exact erase confirmation")
			}
			respond(w, 202, jobs.Job{ID: "install"})
		case "/api/v1/installer/jobs/install":
			respond(w, 200, map[string]any{"job": jobs.Job{State: "applied", GitSHA: pulled}, "log": "installed\n"})
		case "/api/v1/installer/reboot":
			s.View.Upsert(cluster.HostPayload{NodeID: "newhost", Hostname: "newhost", AgeRecipient: session.Info.Recipient, HTTPAddress: installer})
			respond(w, 202, map[string]string{"status": "reboot scheduled"})
		case "/api/v1/host":
			respond(w, 200, hostinfo.HostInfo{Hostname: "newhost", AgeRecipient: session.Info.Recipient})
		default:
			t.Error("unexpected request", r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	}))
	session.Address = installer
	if err := s.saveSession(session); err != nil {
		t.Fatal(err)
	}
	job := jobs.Job{ID: "full-onboarding", Kind: "host-onboard", Host: "local", Args: []string{session.ID}}
	if err := s.Store.Create(job); err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	if err := s.runOnboarding(context.Background(), &job, &log); err != nil {
		t.Fatal(err, log.String())
	}
	saved, err := s.loadSession(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !saved.Completed || !saved.Installed || saved.Revision != pulled || job.GitSHA != pulled {
		t.Fatalf("incomplete workflow: %+v", saved)
	}
	before := len(calls)
	if err := s.runOnboarding(context.Background(), &job, &log); err != nil {
		t.Fatal(err)
	}
	if len(calls) != before {
		t.Fatal("completed retry repeated installer effects")
	}
}

func TestOnboardingRoutesToConfiguredWriter(t *testing.T) {
	s := operationServer(t)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/onboarding/capability":
			respond(w, 200, map[string]any{"node_id": "writer", "ready": true})
		case "/api/v1/hosts/writer/onboarding":
			respond(w, 200, map[string]any{"form": map[string]any{"Name": map[string]string{"type": "hostname"}}})
		case "/api/v1/hosts/writer/onboarding/start":
			respond(w, 202, map[string]string{"job": "writer-job"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer remote.Close()
	s.View.Upsert(cluster.HostPayload{NodeID: "writer", HTTPAddress: strings.TrimPrefix(remote.URL, "http://")})
	if w := call(s, "GET", "/api/v1/onboarding/coordinators", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"node_id":"writer","ready":true`) {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call(s, "GET", "/api/v1/hosts/writer/onboarding", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "hostname") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call(s, "POST", "/api/v1/hosts/writer/onboarding/start", `{}`); w.Code != 202 || !strings.Contains(w.Body.String(), "writer-job") {
		t.Fatal(w.Code, w.Body.String())
	}
}
