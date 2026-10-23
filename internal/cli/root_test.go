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
		{"tasks", "cancel", "task_1"}, {"tasks", "retry", "task_1", "--version", "0"},
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

func TestSelectorsSearchAllPages(t *testing.T) {
	host := func(id, name string) domain.Host {
		return domain.Host{Record: domain.Record{ID: domain.ID(id)}, Name: name}
	}
	for _, tc := range []struct {
		name          string
		first, second domain.Host
		want          domain.ID
	}{
		{"later name", host("a", "first"), host("b", "selected"), "b"},
		{"ambiguous name", host("a", "selected"), host("b", "selected"), ""},
		{"ID outranks name", host("a", "selected"), host("selected", "second"), "selected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			list := func(_ context.Context, pages ...domain.PageRequest) (domain.Page[domain.Host], error) {
				calls++
				page := pages[0]
				if page.Limit != domain.PageLimit || page.Filters["scope"] != "kept" {
					t.Fatal("lost selector page settings", page)
				}
				if page.Cursor == "" {
					next := "page-two"
					return domain.Page[domain.Host]{Items: []domain.Host{tc.first}, NextCursor: &next}, nil
				}
				if page.Cursor != "page-two" {
					t.Fatal("unexpected cursor", page.Cursor)
				}
				return domain.Page[domain.Host]{Items: []domain.Host{tc.second}}, nil
			}
			got, err := resolveSelector(context.Background(), "selected", domain.PageRequest{Filters: map[string]string{"scope": "kept"}}, list, func(h domain.Host) (domain.ID, string) { return h.ID, h.Name })
			if calls != 2 || got.ID != tc.want || (err != nil) != (tc.want == "") {
				t.Fatalf("got %s, %v after %d pages", got.ID, err, calls)
			}
		})
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
