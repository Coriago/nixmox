//go:build !js

package browser

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreviewIsIsolatedAndLocalAssets(t *testing.T) {
	if _, err := Handler(t.TempDir()); err == nil {
		t.Fatal("missing assets accepted")
	}
	dir := t.TempDir()
	for _, name := range []string{"app.wasm", "wasm_exec.js"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	h, err := Handler(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method, path string
		status       int
		contains     string
	}{
		{"GET", "/", 200, "Nixmox"},
		{"GET", "/web/console.css", 200, ".workspace"},
		{"GET", "/web/app.wasm", 200, "fixture"},
		{"GET", "/api/v1/hosts", 503, "no operational API"},
		{"POST", "/api/v1/commands", 405, "Read-only"},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(test.method, test.path, nil))
		if w.Code != test.status || !strings.Contains(w.Body.String(), test.contains) {
			t.Fatalf("%s %s: %d %s", test.method, test.path, w.Code, w.Body)
		}
		if w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("missing asset protections")
		}
		if test.path == "/web/app.wasm" && w.Header().Get("Content-Type") != "application/wasm" {
			t.Fatal("incorrect WASM MIME")
		}
	}
}
