//go:build !js

package browser

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/maxence-charriere/go-app/v11/pkg/app"
)

//go:embed style.css
var stylesheet string

// Handler is an isolated, read-only preview; deliberately no operational API.
func Handler(assets string) (http.Handler, error) {
	for _, name := range []string{"app.wasm", "wasm_exec.js"} {
		info, err := os.Stat(filepath.Join(assets, name))
		if err != nil || info.IsDir() {
			return nil, fmt.Errorf("missing preview asset %s in %s", name, assets)
		}
	}
	Register()
	mux := http.NewServeMux()
	for route, name := range map[string]string{"/web/app.wasm": "app.wasm", "/wasm_exec.js": "wasm_exec.js"} {
		mux.HandleFunc("GET "+route, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-cache")
			if name == "app.wasm" {
				w.Header().Set("Content-Type", "application/wasm")
			}
			http.ServeFile(w, r, filepath.Join(assets, name))
		})
	}
	mux.HandleFunc("GET /web/console.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		fmt.Fprint(w, styles)
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Mock preview: no operational API", http.StatusServiceUnavailable)
	})
	mux.Handle("/", &app.Handler{Name: "Nixmox mock console", Title: "Nixmox · Workspace", Description: "Mock cluster console · no host operations", Styles: []string{"/web/console.css"}, BackgroundColor: "#181818", ThemeColor: "#e57000"})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Read-only mock preview", http.StatusMethodNotAllowed)
			return
		}
		mux.ServeHTTP(w, r)
	}), nil
}
func Serve(ctx context.Context, port int, assets string) error {
	h, err := Handler(assets)
	if err != nil {
		return err
	}
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	s := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if s.Shutdown(shutdown) != nil {
				s.Close()
			}
		case <-done:
		}
	}()
	fmt.Fprintf(os.Stderr, "Mock console: http://%s/ · no real host operations\n", l.Addr())
	err = s.Serve(l)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
