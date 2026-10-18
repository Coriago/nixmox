package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"nixmox/internal/addhost"
	"nixmox/internal/gitops"
	"nixmox/internal/jobs"
	"nixmox/internal/statefile"
)

func TestExistingConfigurationSelectionDoesNotPublish(t *testing.T) {
	testExistingSelection(t, false)
}
func TestExistingConfigurationAuthorizesReplacement(t *testing.T) {
	testExistingSelection(t, true)
}
func testExistingSelection(t *testing.T, authorize bool) {
	s := onboardingServer(t)
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v %s", err, b)
		}
		return strings.TrimSpace(string(b))
	}
	git("init", "-b", "main")
	git("-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "--allow-empty", "-m", "existing")
	sha := git("rev-parse", "HEAD")
	s.Git = &fakeGitOps{state: gitops.State{RepoURL: repo, Branch: "main"}}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "nix"), []byte(`#!/bin/sh
case "$*" in
  *builtins.attrNames*) echo '["newhost"]';;
  *) echo '{"enabled":true,"seed":"192.0.2.1:8080","hostname":"newhost","networkManager":true,"disks":["/dev/vda"]}';;
esac
`), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	session := sessionFixture(t, s)
	if authorize {
		for _, tool := range []string{"age-keygen", "sops"} {
			if _, err := exec.LookPath(tool); err != nil {
				t.Fatal("run in devshell", err)
			}
		}
		run := func(name string, args ...string) []byte {
			t.Helper()
			cmd := exec.Command(name, args...)
			cmd.Dir = repo
			out, err := cmd.Output()
			if err != nil {
				t.Fatal(err)
			}
			return out
		}
		oldKey, newKey := filepath.Join(t.TempDir(), "old.age"), filepath.Join(t.TempDir(), "new.age")
		run("age-keygen", "-o", oldKey)
		run("age-keygen", "-o", newKey)
		oldRecipient := strings.TrimSpace(string(run("age-keygen", "-y", oldKey)))
		session.Info.Recipient = strings.TrimSpace(string(run("age-keygen", "-y", newKey)))
		s.Config.AddHost.AgeKeyFile = oldKey
		s.Config.AddHost.SOPS = &addhost.SOPSConfig{RulesFile: ".sops.yaml", Files: []string{"secrets.yaml"}, RulePathRegex: "^secrets.yaml$"}
		os.WriteFile(filepath.Join(repo, ".sops.yaml"), []byte("creation_rules:\n  - path_regex: '^secrets.yaml$'\n    age: "+oldRecipient+"\n"), 0600)
		os.WriteFile(filepath.Join(repo, "secrets.yaml"), []byte("password: test-value\n"), 0600)
		encrypted := run("sops", "--encrypt", "secrets.yaml")
		os.WriteFile(filepath.Join(repo, "secrets.yaml"), encrypted, 0600)
		git("add", ".")
		git("-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "-m", "secrets")
		git("config", "receive.denyCurrentBranch", "updateInstead")
		sha = git("rev-parse", "HEAD")
		if err := s.saveSession(session); err != nil {
			t.Fatal(err)
		}
	}
	base := "/api/v1/hosts/local/onboarding"
	w := call(s, "POST", base+"/configurations", `{}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), sha) || !strings.Contains(w.Body.String(), "newhost") {
		t.Fatal(w.Code, w.Body.String())
	}
	request := `{"session":"test-session","revision":"` + sha + `","hostname":"newhost","drive":"/dev/vda"}`
	for _, bad := range []string{strings.Replace(request, sha, "main", 1), strings.Replace(request, "/dev/vda", "/dev/vdb", 1)} {
		if w := call(s, "POST", base+"/select", bad); w.Code == 200 {
			t.Fatal("accepted invalid selection")
		}
	}
	if authorize {
		if w := call(s, "POST", base+"/select", request); w.Code != 422 {
			t.Fatal("missing recipient should require authorization", w.Code)
		}
		request = strings.TrimSuffix(request, "}") + `,"authorize_recipient":true}`
	}
	w = call(s, "POST", base+"/select", request)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	session, _ = s.loadSession(session.ID)
	if !session.Existing || session.Revision != sha || session.Plan.Revision != sha || session.Digest == "" {
		t.Fatal(session)
	}
	if git("rev-parse", "HEAD") != sha {
		t.Fatal("review pushed changes")
	}
	if session.AuthorizeRecipient != authorize {
		t.Fatal("authorization choice lost")
	}
	pulled := ""
	session.Address = installerBus(t, s, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/installer/info":
			respond(w, 200, session.Info)
		case "/api/v1/installer/reserve":
			respond(w, 200, map[string]string{"status": "reserved"})
		case "/api/v1/installer/pull":
			var plan addhost.InstallPlan
			json.NewDecoder(r.Body).Decode(&plan)
			pulled = plan.Revision
			respond(w, 202, jobs.Job{ID: "pull-existing"})
		case "/api/v1/installer/jobs/pull-existing":
			respond(w, 200, map[string]any{"job": jobs.Job{State: "failed", Error: "stop before disk installation"}})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.Error(w, "unexpected", 500)
		}
	}))
	if err := s.saveSession(session); err != nil {
		t.Fatal(err)
	}
	job := jobs.Job{ID: "existing-onboard", Kind: "host-onboard", Host: "local", Args: []string{session.ID}}
	if err := s.Store.Create(job); err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	err := s.runOnboarding(context.Background(), &job, &log)
	if err == nil || !strings.Contains(err.Error(), "stop before disk installation") {
		t.Fatal(err, log.String())
	}
	if authorize {
		current, _ := s.loadSession(session.ID)
		if pulled == sha || pulled != git("rev-parse", "HEAD") || current.AuthorizeRecipient {
			t.Fatal("replacement revision not persisted and pulled", pulled, current)
		}
		if err := addhost.CheckExistingRecipients(repo, s.Config.AddHost.SOPS, session.Info.Recipient); err != nil {
			t.Fatal(err)
		}
	} else if pulled != sha || git("rev-parse", "HEAD") != sha || strings.Contains(log.String(), "pushing Git") {
		t.Fatal("existing selection published or pulled wrong revision", pulled, log.String())
	}
	session.InstallJob = "installation-started"
	if err := s.saveSession(session); err != nil {
		t.Fatal(err)
	}
	if w := call(s, "POST", base+"/select", request); w.Code != 409 {
		t.Fatal("changed plan after installation", w.Code)
	}
}

func TestResetInstallerPlanArchivesOnlyPreparation(t *testing.T) {
	for _, installed := range []bool{false, true} {
		t.Run(map[bool]string{false: "preparation", true: "installation-started"}[installed], func(t *testing.T) {
			s := operationServer(t)
			s.Config.InstallerMode = true
			boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
			if err != nil {
				t.Fatal(err)
			}
			plan := addhost.InstallPlan{ID: "reset-test", BootID: strings.TrimSpace(string(boot)), Recipient: "age1test", Revision: strings.Repeat("a", 40)}
			if err := os.MkdirAll(s.installPath(plan.ID), 0700); err != nil {
				t.Fatal(err)
			}
			if err := statefile.Write(filepath.Join(s.installPath(plan.ID), "plan.json"), plan); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(s.Config.DataDirPath("onboarding-installer", "reservation"), []byte(plan.ID), 0600); err != nil {
				t.Fatal(err)
			}
			if installed {
				if err := s.Store.Create(jobs.Job{ID: "install-before-reset", Kind: "host-install", State: "failed", Args: []string{plan.ID}}); err != nil {
					t.Fatal(err)
				}
			}
			body, _ := json.Marshal(plan)
			w := httptest.NewRecorder()
			s.resetInstallerPlan(w, httptest.NewRequest("POST", "/api/v1/installer/reset-plan", strings.NewReader(string(body))))
			if installed {
				if w.Code != 409 {
					t.Fatal(w.Code, w.Body.String())
				}
				if _, err := s.loadInstall(plan.ID); err != nil {
					t.Fatal("lost installation plan", err)
				}
				return
			}
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			if _, err := s.loadInstall(plan.ID); !os.IsNotExist(err) {
				t.Fatal("old plan remains", err)
			}
			files, _ := filepath.Glob(s.Config.DataDirPath("onboarding-installer", "archive", "*", "plan.json"))
			if len(files) != 1 {
				t.Fatal(files)
			}
		})
	}
}
