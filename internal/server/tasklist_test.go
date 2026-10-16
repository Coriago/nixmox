package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"nixmox/internal/cluster"
	"nixmox/internal/jobs"
	"strings"
	"testing"
	"time"
)

func TestGlobalAndHostTaskScopes(t *testing.T) {
	s := operationServer(t)
	s.Store.Create(jobs.Job{ID: "local-job", Host: "local", Kind: "echo", State: "applied", CreatedAt: time.Now()})
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/hosts/remote/jobs" {
			t.Errorf("unexpected route %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode([]jobs.Job{{ID: "remote-job", Host: "remote", Kind: "echo", State: "applied", CreatedAt: time.Now().Add(-time.Hour)}})
	}))
	defer peer.Close()
	s.View.Upsert(cluster.HostPayload{NodeID: "remote", HTTPAddress: strings.TrimPrefix(peer.URL, "http://")})
	s.View.Upsert(cluster.HostPayload{NodeID: "offline"})
	s.View.MarkOffline("offline")
	all := call(s, "GET", "/partials/jobs", "")
	for _, want := range []string{"local-job", "remote-job", "offline: offline"} {
		if !strings.Contains(all.Body.String(), want) {
			t.Fatal(want, all.Body.String())
		}
	}
	local := call(s, "GET", "/partials/hosts/local/jobs", "")
	if !strings.Contains(local.Body.String(), "local-job") || strings.Contains(local.Body.String(), "remote-job") {
		t.Fatal(local.Body.String())
	}
	host := call(s, "GET", "/hosts/local", "")
	if !strings.Contains(host.Body.String(), `data-live="/partials/hosts/local/jobs"`) {
		t.Fatal(host.Body.String())
	}
}
func TestMediaAdmission(t *testing.T) {
	s := operationServer(t)
	for _, path := range []string{"/api/v1/media/build", "/api/v1/media/write"} {
		if w := call(s, "POST", path, `{}`); w.Code != 403 {
			t.Fatal(path, w.Code)
		}
	}
	s.Config.EnableMedia = true
	for _, body := range []string{`{}`, `{"device":"/dev/sdb","confirm":"/dev/sdc"}`, `{"device":"/dev/sdb","unknown":true}`} {
		if w := call(s, "POST", "/api/v1/media/write", body); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := call(s, "POST", "/api/v1/media/build", `{"ssh_key":"not a public key"}`); w.Code != 400 {
		t.Fatal(w.Code, w.Body.String())
	}
	list, _ := s.Store.List()
	if len(list) != 0 {
		t.Fatal("invalid requests queued destructive work", list)
	}
}

func TestFailedReplacementImageIsNotOffered(t *testing.T) {
	s := operationServer(t)
	if err := s.Store.Create(jobs.Job{ID: "bad-image", Kind: "installer-build", Host: "local", State: "failed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.preparedImage(); err == nil || !strings.Contains(err.Error(), "Latest installer build is failed") {
		t.Fatal(err)
	}
}
