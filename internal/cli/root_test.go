package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nixmox/internal/domain"
)

func TestCobraValidationAndHelp(t *testing.T) {
	for _, args := range [][]string{
		{"unknown"}, {"hosts", "show"}, {"network", "check", "--wait", "--detach"},
		{"serve", "--http-port=bad"}, {"identity", "show", "--endpoint=not-an-origin"},
	} {
		var out, diagnostics bytes.Buffer
		if code := Execute(context.Background(), args, &out, &diagnostics); code != 2 {
			t.Fatalf("%v exit=%d: %s", args, code, diagnostics.String())
		}
	}
	var out, diagnostics bytes.Buffer
	if code := Execute(context.Background(), []string{"serve", "--help"}, &out, &diagnostics); code != 0 || !strings.Contains(out.String(), "--http-port") {
		t.Fatalf("help: exit=%d %s", code, diagnostics.String())
	}
}

func TestCLIEndpointPrecedenceAndJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/api/v1/identity" {
			t.Errorf("CLI bypassed expected API route: %s %s", r.Method, r.URL.Path)
		}
		json.NewEncoder(w).Encode(domain.Identity{SchemaVersion: 1, NodeID: "n_api"})
	}))
	defer server.Close()
	t.Setenv("NIXMOX_ENDPOINT", "http://127.0.0.1:1")
	var out, diagnostics bytes.Buffer
	args := []string{"--endpoint", server.URL, "--json", "identity", "show"}
	if code := Execute(context.Background(), args, &out, &diagnostics); code != 0 {
		t.Fatalf("exit=%d %s", code, diagnostics.String())
	}
	var identity domain.Identity
	if err := json.Unmarshal(out.Bytes(), &identity); err != nil || identity.NodeID != "n_api" {
		t.Fatalf("invalid JSON: %s %v", out.String(), err)
	}
}
