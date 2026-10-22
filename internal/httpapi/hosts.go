package httpapi

import (
	"context"
	"net/http"
	"nixmox/internal/domain"
	"time"
)

func (a *Server) hostRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/hosts", a.hostRead)
	mux.HandleFunc("GET /api/v1/hosts/{id}", a.hostRead)
}
func (a *Server) hostRead(w http.ResponseWriter, r *http.Request) {
	reader, err := a.service.Hosts()
	if err != nil {
		writeError(w, err, "")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	q := r.URL.Query()
	for k, v := range q {
		if r.PathValue("id") != "" || (k != "limit" && k != "cursor") || len(v) != 1 {
			writeError(w, domain.Err("INVALID_QUERY", "Invalid host query.", 400), "")
			return
		}
	}
	var result any
	if r.PathValue("id") == "" {
		result, err = reader.Hosts(ctx, q.Get("limit"), q.Get("cursor"))
	} else {
		result, err = reader.Host(ctx, r.PathValue("id"))
	}
	respond(w, result, err)
}
