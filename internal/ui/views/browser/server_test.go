//go:build !js

package browser_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"nixmox/internal/api"
	"nixmox/internal/ui/views/browser"
)

func TestConsoleAssets(t *testing.T) {
	// --- Test setup ---
	// ------------------
	assets := t.TempDir()
	for _, name := range []string{"app.wasm", "wasm_exec.js"} {
		if err := os.WriteFile(filepath.Join(assets, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ui, err := browser.Handler(assets)
	if err != nil {
		t.Fatal(err)
	}
	server := (api.Server{UI: ui}).Handler()

	// --- Usage ---
	// -------------
	// The same daemon router serves the shell, styles and WASM without masking
	// missing API routes. This checks integration, not Go-app's own internals.
	for path, status := range map[string]int{
		"/": 200, "/app.css": 200, "/app.js": 200, "/web/console.css": 200,
		"/web/app.wasm": 200, "/wasm_exec.js": 200, "/web/icon.png": 200,
		"/web/icon-large.png": 200, "/web/icon.svg": 200, "/missing-api": 404,
	} {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		// --- Assertions ---
		// ------------------
		if response.Code != status {
			t.Errorf("%s: got HTTP %d, want %d", path, response.Code, status)
		}
	}
}
