package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nixmox/internal/config"
	"nixmox/internal/jobs"
	"nixmox/internal/portal"
)

func TestServer(t *testing.T) {
	cfg := &config.Config{}
	dir := t.TempDir()
	store, err := jobs.NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	runner := jobs.NewRunner(store)

	s := New(cfg, store, runner)
	p, err := portal.New()
	if err != nil {
		t.Fatalf("failed to create portal: %v", err)
	}
	s.MountHandlers(p, NoopAuth{})

	t.Run("healthz", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/healthz", nil)
		rr := httptest.NewRecorder()
		s.Mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rr.Code)
		}
		var resp map[string]string
		json.NewDecoder(rr.Body).Decode(&resp)
		if resp["status"] != "ok" {
			t.Errorf("expected status ok, got %s", resp["status"])
		}
	})

	t.Run("api_hosts", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/hosts", nil)
		rr := httptest.NewRecorder()
		s.Mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rr.Code)
		}
		var hosts []map[string]interface{}
		json.NewDecoder(rr.Body).Decode(&hosts)
		if len(hosts) != 1 {
			t.Errorf("expected 1 host, got %d", len(hosts))
		}
	})

	t.Run("api_bootstrap", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/bootstrap", nil)
		rr := httptest.NewRecorder()
		s.Mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rr.Code)
		}
		var resp map[string]interface{}
		json.NewDecoder(rr.Body).Decode(&resp)
		if resp["status"] != "unclustered" {
			t.Errorf("expected status unclustered, got %s", resp["status"])
		}
	})

	t.Run("portal_index", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		rr := httptest.NewRecorder()
		s.Mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", rr.Code)
		}
		body := rr.Body.String()
		if !strings.Contains(body, "Hosts") {
			t.Errorf("expected Hosts text, got %s", body)
		}
	})

	t.Run("api_jobs", func(t *testing.T) {
		// POST /api/v1/jobs
		reqBody := []byte(`{"kind":"echo","args":["hello"]}`)
		req := httptest.NewRequest("POST", "/api/v1/jobs", bytes.NewBuffer(reqBody))
		rr := httptest.NewRecorder()
		s.Mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
		}
		var resp map[string]string
		json.NewDecoder(rr.Body).Decode(&resp)
		id := resp["id"]
		if id == "" {
			t.Fatalf("expected id, got empty")
		}

		// Poll GET /api/v1/jobs/{id}
		var jobResp map[string]interface{}
		for i := 0; i < 10; i++ {
			req = httptest.NewRequest("GET", "/api/v1/jobs/"+id, nil)
			rr = httptest.NewRecorder()
			s.Mux.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", rr.Code)
			}
			json.NewDecoder(rr.Body).Decode(&jobResp)
			if jobResp["state"] == "applied" {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}

		if jobResp["state"] != "applied" {
			t.Errorf("expected state applied, got %s", jobResp["state"])
		}

		logs := jobResp["log"].([]interface{})
		found := false
		for _, l := range logs {
			if strings.Contains(l.(string), "hello") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected log to contain hello, got %v", logs)
		}
	})
}
