package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nixmox/internal/cluster"
	"nixmox/internal/config"
	"nixmox/internal/jobs"
	"nixmox/internal/logbuf"
	"nixmox/internal/portal"
)

func operationServer(t *testing.T) *Server {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.DataDir = t.TempDir()
	cfg.Hostname = "local"
	store, err := jobs.NewFileStore(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	s := New(&cfg, store, jobs.NewRunner(store))
	s.View = cluster.NewView()
	s.Logs = logbuf.New(1024)
	p, err := portal.New()
	if err != nil {
		t.Fatal(err)
	}
	p.Hostname = cfg.Hostname
	s.MountHandlers(p, NoopAuth{})
	s.MountApply(p)
	s.MountGitOps(p)
	s.MountCluster(p)
	t.Cleanup(s.Runner.Stop)
	return s
}
func call(s *Server, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Mux.ServeHTTP(w, req)
	return w
}
func TestVariablesValidationAndPersistence(t *testing.T) {
	s := operationServer(t)
	path := "/api/v1/hosts/local/variables"
	if w := call(s, "PUT", path, `{"max_fan_speed":40.5}`); w.Code != 403 {
		t.Fatal(w.Code)
	}
	s.Config.AllowConfigWrite = true
	for _, body := range []string{`null`, `{"../escape":1}`, `{"bad":[]}`, `{"ok":null}`, `{} {}`} {
		if w := call(s, "PUT", path, body); w.Code != 400 {
			t.Fatalf("%s: %d", body, w.Code)
		}
	}
	if w := call(s, "PUT", path, `{"max_fan_speed":40.5,"quiet":true}`); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w := call(s, "GET", path, ""); !strings.Contains(w.Body.String(), "40.5") {
		t.Fatal(w.Body.String())
	}
}
func TestMutationsRejectCrossOrigin(t *testing.T) {
	s := operationServer(t)
	req := httptest.NewRequest("POST", "/api/v1/jobs", strings.NewReader(`{"kind":"echo"}`))
	req.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	s.Mux.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}
func TestPowerAndProvisionSafety(t *testing.T) {
	s := operationServer(t)
	if w := call(s, "POST", "/api/v1/provision", `{}`); w.Code != 403 {
		t.Fatal(w.Code)
	}
	s.Config.EnableProvision = true
	for _, body := range []string{`{"target":"-x","confirm":"-x","flake":"x#y"}`, `{"target":"root@host","confirm":"different","flake":"x#y"}`, `{"target":"root@host","confirm":"root@host","flake":"--help#x"}`} {
		if w := call(s, "POST", "/api/v1/provision", body); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	req := httptest.NewRequest("POST", "/api/v1/hosts/local/actions/shutdown", strings.NewReader("confirm=local"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	s.Mux.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := call(s, "POST", "/api/v1/hosts/local/actions/reboot", ""); w.Code != 400 {
		t.Fatal(w.Code)
	}
	list, _ := s.Store.List()
	if len(list) != 0 {
		t.Fatal("unsafe requests created jobs")
	}
}
func TestRemoteActionsNeverApplyLocally(t *testing.T) {
	s := operationServer(t)
	var calls atomic.Int32
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/api/v1/hosts/remote/actions/apply" {
			t.Errorf("wrong path %s", r.URL.Path)
		}
		if r.Header.Get("X-Nixmox-Forwarded") != "1" {
			t.Error("no loop guard")
		}
		fmt.Fprint(w, `{"id":"remote-job"}`)
	}))
	defer peer.Close()
	s.View.Upsert(cluster.HostPayload{NodeID: "remote", HTTPAddress: strings.TrimPrefix(peer.URL, "http://")})
	if w := call(s, "POST", "/api/v1/hosts/remote/actions/apply", ""); w.Code != 200 || !strings.Contains(w.Body.String(), "remote-job") {
		t.Fatal(w.Code, w.Body.String())
	}
	s.View.MarkOffline("remote")
	if w := call(s, "POST", "/api/v1/hosts/remote/actions/apply", ""); w.Code != 503 {
		t.Fatal(w.Code)
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	list, _ := s.Store.List()
	if len(list) != 0 {
		t.Fatal("remote operation created local task")
	}
}
func TestStreamFragmentsAndCancellation(t *testing.T) {
	s := operationServer(t)
	s.Logs.Write([]byte("<script>not HTML</script>"))
	srv := httptest.NewServer(s.Mux)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/v1/events?partial=/partials/logs", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal(resp.Header)
	}
	scanner := bufio.NewScanner(resp.Body)
	found := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			var data map[string]string
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(data["/partials/logs"], "<script>") {
				t.Fatal("unescaped log")
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatal("missing initial event")
	}
	cancel()
}
func TestPortalPagesAndAssets(t *testing.T) {
	s := operationServer(t)
	for _, path := range []string{"/", "/hosts/local", "/manage", "/gitops", "/jobs", "/assets/console.js", "/assets/style.css", "/partials/hosts/local/resources"} {
		w := call(s, "GET", path, "")
		if w.Code != 200 {
			t.Errorf("%s: %d %s", path, w.Code, w.Body.String())
		}
		if w.Body.Len() == 0 {
			t.Errorf("empty %s", path)
		}
	}
	if w := call(s, "GET", "/assets/templates/base.html", ""); w.Code != 404 {
		t.Error("template source exposed")
	}
}

// rollout fake finishes immediately and records order without running rebuild.
type rolloutApply struct {
	store   jobs.Store
	counter int
	fail    bool
}

func (a *rolloutApply) Apply(context.Context) (jobs.Job, error) {
	a.counter++
	j := jobs.Job{ID: fmt.Sprintf("roll-%d", a.counter), State: "applied", Host: "local"}
	if a.fail {
		j.State = "failed"
	}
	return j, a.store.Create(j)
}
func (a *rolloutApply) Rollback(ctx context.Context) (jobs.Job, error) { return a.Apply(ctx) }
func TestRolloutFailureStopsSubsequentHosts(t *testing.T) {
	s := operationServer(t)
	a := &rolloutApply{store: s.Store, fail: true}
	s.Apply = a
	s.rollout = Rollout{State: "running"}
	s.runRollout([]cluster.HostPayload{{NodeID: "local"}, {NodeID: "never-called"}}, "serial", 1)
	if s.rollout.State != "failed" || a.counter != 1 {
		t.Fatalf("%+v calls=%d", s.rollout, a.counter)
	}
}
func TestDecodeLimits(t *testing.T) {
	s := operationServer(t)
	s.Config.AllowConfigWrite = true
	body := `{"large":"` + strings.Repeat("x", (1<<20)+1) + `"}`
	if w := call(s, "PUT", "/api/v1/hosts/local/variables", body); w.Code != 400 {
		t.Fatal(w.Code)
	}
	w := call(s, "GET", "/api/v1/events?partial=/api/v1/logs", "")
	_, _ = io.Copy(io.Discard, w.Body)
	if w.Code != 400 {
		t.Fatal(w.Code)
	}
}
