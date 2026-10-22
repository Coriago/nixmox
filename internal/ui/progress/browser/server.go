//go:build !js

package browser

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
	"nixmox/internal/ui/progress"
)

//go:embed style.css
var stylesheet string

// Handler serves only the experiment; no production API, commands or engines.
// assets contains app.wasm and wasm_exec.js from the SAME Go compiler.
func Handler(assets string) (http.Handler, error) {
	for _, name := range []string{"app.wasm", "wasm_exec.js"} {
		if info, err := os.Stat(filepath.Join(assets, name)); err != nil || info.IsDir() {
			return nil, fmt.Errorf("missing preview asset %s in %s", name, assets)
		}
	}
	Register()
	mux := http.NewServeMux()
	for route, name := range map[string]string{"/web/app.wasm": "app.wasm", "/wasm_exec.js": "wasm_exec.js"} {
		mux.HandleFunc("GET "+route, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-cache")
			file := filepath.Join(assets, name)
			if name == "app.wasm" {
				w.Header().Set("Vary", "Accept-Encoding")
				w.Header().Set("Content-Type", "application/wasm")
				if acceptsGzip(r.Header.Get("Accept-Encoding")) {
					if _, err := os.Stat(file + ".gz"); err == nil {
						file += ".gz"
						w.Header().Set("Content-Encoding", "gzip")
					}
				}
			}
			http.ServeFile(w, r, file)
		})
	}
	var css strings.Builder
	css.WriteString(":root{" + progress.CSSVariables() + "}\n")
	for _, t := range progress.Palette() {
		fmt.Fprintf(&css, ".tone-%s{color:var(--%s)}\n", t.Name, t.Name)
	}
	css.WriteString(stylesheet)
	mux.HandleFunc("GET /web/progress.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		fmt.Fprint(w, css.String())
	})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Component experiment only; no operational API", http.StatusServiceUnavailable)
	})
	mux.Handle("/", &app.Handler{Name: "Nixmox progress experiment", Title: "Nixmox · Progress", Description: "One shared Go component, browser and terminal", Styles: []string{"/web/progress.css"}, BackgroundColor: "#0b1117", ThemeColor: "#0b1117"})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !strings.HasPrefix(r.URL.Path, "/api/") {
			http.Error(w, "Read-only component experiment", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		mux.ServeHTTP(w, r)
	}), nil
}
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		encoding, params, err := mime.ParseMediaType(strings.TrimSpace(part))
		if err != nil || encoding != "gzip" {
			continue
		}
		if q, ok := params["q"]; ok {
			value, err := strconv.ParseFloat(q, 64)
			return err == nil && value > 0 && value <= 1
		}
		return true
	}
	return false
}

func Serve(ctx context.Context, port int, assets string) error {
	handler, err := Handler(assets)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			timeout, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if server.Shutdown(timeout) != nil {
				server.Close()
			}
		case <-done:
		}
	}()
	fmt.Fprintf(os.Stderr, "Progress experiment: http://%s/ · mock data only\n", listener.Addr())
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
