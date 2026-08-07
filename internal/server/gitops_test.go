package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nixmox/internal/config"
	"nixmox/internal/gitops"
	"nixmox/internal/jobs"
	"nixmox/internal/portal"
)

type fakeGitOps struct {
	state     gitops.State
	ensureSHA string
	ensureErr error
}

func (f *fakeGitOps) State() gitops.State { return f.state }
func (f *fakeGitOps) Configure(url, branch string) error {
	if branch == "" {
		branch = "main"
	}
	f.state.RepoURL = url
	f.state.Branch = branch
	return nil
}
func (f *fakeGitOps) Ensure(ctx context.Context) (string, error) {
	if f.ensureErr != nil {
		return "", f.ensureErr
	}
	f.state.LastPulledSHA = f.ensureSHA
	return f.ensureSHA, nil
}

func newGitOpsTestServer(t *testing.T, cfg *config.Config, fake *fakeGitOps) *Server {
	t.Helper()
	store, err := jobs.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	s := New(cfg, store, jobs.NewRunner(store))
	s.SetGitOps(fake)
	p, err := portal.New()
	if err != nil {
		t.Fatalf("portal.New: %v", err)
	}
	s.MountHandlers(p, NoopAuth{})
	s.MountGitOps(p)
	return s
}

func TestGitOpsEndpoints(t *testing.T) {
	fake := &fakeGitOps{ensureSHA: "abc123", state: gitops.State{RepoURL: "file:///repo", Branch: "main"}}

	t.Run("get status", func(t *testing.T) {
		s := newGitOpsTestServer(t, &config.Config{}, fake)
		rr := httptest.NewRecorder()
		s.Mux.ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/gitops", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rr.Code)
		}
		var resp map[string]interface{}
		json.NewDecoder(rr.Body).Decode(&resp)
		if resp["repo_url"] != "file:///repo" || resp["branch"] != "main" {
			t.Errorf("unexpected status: %v", resp)
		}
		if resp["update_available"] != false {
			t.Errorf("unexpected update_available: %v", resp)
		}
	})

	t.Run("put rejected when writes disabled", func(t *testing.T) {
		s := newGitOpsTestServer(t, &config.Config{AllowConfigWrite: false}, fake)
		body := bytes.NewBufferString(`{"repo_url":"file:///other"}`)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("PUT", "/api/v1/gitops", body)
		req.Header.Set("Content-Type", "application/json")
		s.Mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Errorf("expected 405, got %d", rr.Code)
		}
	})

	t.Run("put allowed when writes enabled", func(t *testing.T) {
		s := newGitOpsTestServer(t, &config.Config{AllowConfigWrite: true}, fake)
		body := bytes.NewBufferString(`{"repo_url":"file:///other","branch":"dev"}`)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("PUT", "/api/v1/gitops", body)
		req.Header.Set("Content-Type", "application/json")
		s.Mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
		if fake.state.RepoURL != "file:///other" || fake.state.Branch != "dev" {
			t.Errorf("configure not applied: %+v", fake.state)
		}
	})

	t.Run("pull returns new sha", func(t *testing.T) {
		s := newGitOpsTestServer(t, &config.Config{}, fake)
		rr := httptest.NewRecorder()
		s.Mux.ServeHTTP(rr, httptest.NewRequest("POST", "/api/v1/gitops/pull", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rr.Code)
		}
		var resp map[string]string
		json.NewDecoder(rr.Body).Decode(&resp)
		if resp["last_pulled_sha"] != "abc123" {
			t.Errorf("expected abc123, got %v", resp)
		}
	})

	t.Run("portal page renders", func(t *testing.T) {
		fresh := &fakeGitOps{state: gitops.State{RepoURL: "file:///repo", Branch: "main"}}
		s := newGitOpsTestServer(t, &config.Config{}, fresh)
		rr := httptest.NewRecorder()
		s.Mux.ServeHTTP(rr, httptest.NewRequest("GET", "/gitops", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rr.Code)
		}
		if !strings.Contains(rr.Body.String(), "file:///repo") {
			t.Errorf("portal page missing repo URL")
		}
	})
}
