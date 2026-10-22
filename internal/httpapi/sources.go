package httpapi

import (
	"context"
	"net/http"
	"nixmox/internal/domain"
	"time"
)

func (a *Server) sourceRoutes(mux *http.ServeMux) {
	for _, route := range []string{"/repositories", "/repositories/{id}", "/repositories/{id}/refs", "/repositories/{id}/diff", "/artifacts/{id}"} {
		mux.HandleFunc("GET /api/v1"+route, a.sourceRead)
	}
}
func (a *Server) sourceRead(w http.ResponseWriter, r *http.Request) {
	sources, err := a.service.Sources()
	if err != nil {
		writeError(w, err, "")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	q := r.URL.Query()
	allowed := map[string]bool{}
	switch r.Pattern {
	case "GET /api/v1/repositories":
		allowed = map[string]bool{"limit": true, "cursor": true}
	case "GET /api/v1/repositories/{id}/diff":
		allowed = map[string]bool{"from": true, "to": true}
	}
	for k, v := range q {
		if !allowed[k] || len(v) != 1 {
			writeError(w, domain.Err("INVALID_QUERY", "Invalid source query.", 400), "")
			return
		}
	}
	var result any
	switch r.Pattern {
	case "GET /api/v1/repositories":
		result, err = sources.Repositories(ctx, q.Get("limit"), q.Get("cursor"))
	case "GET /api/v1/repositories/{id}":
		result, err = sources.Repository(ctx, r.PathValue("id"))
	case "GET /api/v1/repositories/{id}/refs":
		var repo domain.Repository
		repo, err = sources.Repository(ctx, r.PathValue("id"))
		result = struct {
			Version int64              `json:"version"`
			Items   []domain.SourceRef `json:"items"`
		}{repo.Version, repo.Refs}
	case "GET /api/v1/repositories/{id}/diff":
		result, err = sources.Diff(ctx, r.PathValue("id"), q.Get("from"), q.Get("to"))
	case "GET /api/v1/artifacts/{id}":
		result, err = sources.Artifact(ctx, r.PathValue("id"))
	}
	respond(w, result, err)
}
