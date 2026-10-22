package httpapi

import (
	"context"
	"net/http"
	"nixmox/internal/domain"
	"time"
)

func (a *Server) configurationRoutes(mux *http.ServeMux) {
	for _, route := range []string{"/configurations", "/configurations/{id}", "/repositories/{id}/templates", "/repositories/{id}/templates/{template}", "/drafts/{id}", "/drafts/{id}/diff"} {
		mux.HandleFunc("GET /api/v1"+route, a.configurationRead)
	}
}
func (a *Server) configurationRead(w http.ResponseWriter, r *http.Request) {
	configurations, err := a.service.Configurations()
	if err != nil {
		writeError(w, err, "")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	q := r.URL.Query()
	allowed := map[string]bool{}
	switch r.Pattern {
	case "GET /api/v1/configurations":
		allowed = map[string]bool{"repository_id": true, "ref_id": true, "commit": true, "limit": true, "cursor": true}
	case "GET /api/v1/repositories/{id}/templates":
		allowed = map[string]bool{"ref_id": true, "commit": true}
	case "GET /api/v1/repositories/{id}/templates/{template}":
		allowed = map[string]bool{"commit": true}
	}
	for k, v := range q {
		if !allowed[k] || len(v) != 1 {
			writeError(w, domain.Err("INVALID_QUERY", "Invalid configuration query.", 400), "")
			return
		}
	}
	var result any
	switch r.Pattern {
	case "GET /api/v1/configurations":
		result, err = configurations.Configurations(ctx, q.Get("repository_id"), q.Get("ref_id"), q.Get("commit"), q.Get("limit"), q.Get("cursor"))
	case "GET /api/v1/configurations/{id}":
		result, err = configurations.Configuration(ctx, r.PathValue("id"))
	case "GET /api/v1/repositories/{id}/templates":
		result, err = configurations.Templates(ctx, r.PathValue("id"), q.Get("ref_id"), q.Get("commit"))
	case "GET /api/v1/repositories/{id}/templates/{template}":
		result, err = configurations.Template(ctx, r.PathValue("id"), r.PathValue("template"), q.Get("commit"))
	case "GET /api/v1/drafts/{id}":
		result, err = configurations.Draft(ctx, r.PathValue("id"))
	case "GET /api/v1/drafts/{id}/diff":
		result, err = configurations.DraftDiff(ctx, r.PathValue("id"))
	}
	respond(w, result, err)
}
