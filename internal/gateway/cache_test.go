package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"nixmox/internal/service"
)

func TestCacheRoutesUseConnectionIdentity(t *testing.T) {
	handler := New(service.New())
	for _, test := range []struct {
		method, path, remote string
		want                 int
	}{
		{"GET", "/cache/nix-cache-info", "192.0.2.10:54321", http.StatusForbidden},
		{"GET", "/cache/nix-cache-info", "127.0.0.1:54321", http.StatusServiceUnavailable},
		{"HEAD", "/store-cache/nix-cache-info", "192.0.2.10:54321", http.StatusServiceUnavailable},
		{"POST", "/store-cache/nix-cache-info", "127.0.0.1:54321", http.StatusMethodNotAllowed},
		{"GET", "/cache/nix-cache-info?url=http://untrusted", "127.0.0.1:54321", http.StatusBadRequest},
	} {
		request := httptest.NewRequest(test.method, test.path, nil)
		request.RemoteAddr = test.remote
		request.Header.Set("X-Forwarded-For", "127.0.0.1")
		request.Header.Set("Forwarded", "for=127.0.0.1")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Fatalf("%s %s: got %d want %d", test.method, test.path, response.Code, test.want)
		}
		if test.method == "HEAD" && response.Body.Len() != 0 {
			t.Fatal("HEAD returned bytes")
		}
	}
}
