//go:build !js

package browser

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPreviewHandler(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"app.wasm", "app.wasm.gz", "wasm_exec.js"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	h, err := Handler(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/app.js", "/web/progress.css", "/web/app.wasm", "/wasm_exec.js"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	for _, encoding := range []string{"gzip", "br, gzip;q=0.5", "gzip;q=0", "identity", "gzip;q=bogus"} {
		r := httptest.NewRequest("GET", "/web/app.wasm", nil)
		r.Header.Set("Accept-Encoding", encoding)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		want := encoding == "gzip" || encoding == "br, gzip;q=0.5"
		if (w.Header().Get("Content-Encoding") == "gzip") != want || w.Header().Get("Content-Type") != "application/wasm" {
			t.Fatal("incorrect asset negotiation", encoding)
		}
	}
	for _, method := range []string{"GET", "POST"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, "/api/v1/commands", nil))
		if w.Code != 503 {
			t.Fatal("API not gated")
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/", strings.NewReader("x")))
	if w.Code != 405 {
		t.Fatal("unexpected write endpoint")
	}
	if _, err := Handler(t.TempDir()); err == nil {
		t.Fatal("missing assets silently accepted")
	}
}
