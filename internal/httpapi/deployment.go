package httpapi

import (
	"context"
	"net/http"
	"nixmox/internal/domain"
	"time"
)

func (a *Server) deploymentRoutes(mux *http.ServeMux) {
	for _, path := range []string{"GET /api/v1/diffs/{id}", "GET /api/v1/diffs/{id}/changes", "GET /api/v1/deployments/{id}", "GET /api/v1/hosts/{id}/generations"} {
		mux.HandleFunc(path, a.deploymentRead)
	}
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

func (a *Server) deploymentRead(w http.ResponseWriter, r *http.Request) {
	reader, err := a.service.Deployments()
	if err != nil {
		writeError(w, err, "")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	q := r.URL.Query()
	for k, v := range q {
		if (k != "limit" && k != "cursor" && k != "kind" && k != "q") || len(v) != 1 {
			writeError(w, domain.Err("INVALID_QUERY", "Invalid deployment query.", 400), "")
			return
		}
	}
	var result any
	switch r.Pattern {
	case "GET /api/v1/diffs/{id}":
		result, err = reader.Diff(ctx, r.PathValue("id"))
	case "GET /api/v1/diffs/{id}/changes":
		result, err = reader.DiffChanges(ctx, r.PathValue("id"), q.Get("kind"), q.Get("q"), q.Get("limit"), q.Get("cursor"))
	case "GET /api/v1/deployments/{id}":
		result, err = reader.Deployment(ctx, r.PathValue("id"))
	case "GET /api/v1/hosts/{id}/generations":
		result, err = reader.Generations(ctx, r.PathValue("id"), q.Get("limit"), q.Get("cursor"))
	}
	respond(w, result, err)
}
