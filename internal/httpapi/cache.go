package httpapi

import (
	"context"
	"net/http"
	"nixmox/internal/domain"
	"time"
)

func (a *Server) cacheRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/cache/", func(w http.ResponseWriter, r *http.Request) {
		cache, err := a.service.Cache()
		if err != nil {
			writeError(w, err, "")
			return
		}
		cache.ServeLocal(w, r)
	})
	for _, path := range []string{"/api/v1/cache/status", "/api/v1/cache/locations"} {
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			cache, err := a.service.Cache()
			if err != nil {
				writeError(w, err, "")
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
			defer cancel()
			q := r.URL.Query()
			var result any
			if r.URL.Path == "/api/v1/cache/status" && len(q) == 0 {
				result, err = cache.Status(ctx)
			} else if r.URL.Path == "/api/v1/cache/locations" && len(q) == 1 && len(q["path"]) == 1 {
				result, err = cache.Locations(ctx, q.Get("path"))
			} else {
				err = domain.Err("INVALID_QUERY", "Cache status takes no query; locations requires one path.", 400)
			}
			respond(w, result, err)
		})
	}
}
