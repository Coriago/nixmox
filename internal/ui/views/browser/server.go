//go:build !js

package browser

import (
	_ "embed"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/maxence-charriere/go-app/v11/pkg/app"
)

//go:embed style.css
var stylesheet string

// Handler serves bundled assets on the daemon HTTP listener; APIs are mounted by app.
func Handler(assets string) (http.Handler, error) {
	if assets == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		assets = filepath.Join(filepath.Dir(exe), "../share/nixmox-ui")
	}
	for _, name := range []string{"app.wasm", "wasm_exec.js"} {
		info, err := os.Stat(filepath.Join(assets, name))
		if err != nil || info.IsDir() {
			return nil, fmt.Errorf("missing UI asset %s in %s", name, assets)
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
	mux.Handle("/", &app.Handler{Name: "Nixmox console", Title: "Nixmox · Workspace", Description: "Cluster console", Styles: []string{"/web/console.css"}, BackgroundColor: "#181818", ThemeColor: "#e57000"})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "UI asset server does not accept mutations", http.StatusMethodNotAllowed)
			return
		}
		mux.ServeHTTP(w, r)
	}), nil
}
