package httpapi

import (
	"context"
	"net/http"
	"nixmox/internal/domain"
	"time"
)

func (a *Server) deploymentRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/candidates/{id}", func(w http.ResponseWriter, r *http.Request) {
		reader, err := a.service.Deployments()
		if err != nil {
			writeError(w, err, "")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		v, err := reader.Candidate(ctx, r.PathValue("id"))
		respond(w, v, err)
	})
	mux.HandleFunc("GET /api/v1/tasks/{id}/progress", func(w http.ResponseWriter, r *http.Request) {
		task, err := a.service.Task(r.PathValue("id"))
		if err != nil {
			writeError(w, err, "")
			return
		}
		if task.Progress == nil {
			writeError(w, domain.Err("PROGRESS_UNAVAILABLE", "No structured progress has been observed for this task.", 503), "")
			return
		}
		respond(w, task.Progress, nil)
	})
	mux.HandleFunc("GET /api/v1/tasks/{id}/activities", func(w http.ResponseWriter, r *http.Request) {
		if _, err := a.service.Task(r.PathValue("id")); err != nil {
			writeError(w, err, "")
			return
		}
		reader, err := a.service.Deployments()
		if err != nil {
			writeError(w, err, "")
			return
		}
		q := r.URL.Query()
		for k, v := range q {
			if (k != "limit" && k != "cursor") || len(v) != 1 {
				writeError(w, domain.Err("INVALID_QUERY", "Invalid activity query.", 400), "")
				return
			}
		}
		v, err := reader.Activities(r.Context(), r.PathValue("id"), q.Get("limit"), q.Get("cursor"))
		respond(w, v, err)
	})
}
