package httpapi

import (
	"context"
	"net/http"
	"nixmox/internal/secretio"
	"time"
)

func (a *Server) credentialRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/credentials", a.credentialRead)
	mux.HandleFunc("GET /api/v1/credentials/{id}", a.credentialRead)
	// No body is logged or copied into a durable command by this ingress.
	mux.HandleFunc("POST /api/v1/credentials/import", func(w http.ResponseWriter, r *http.Request) {
		reader, err := a.service.Credentials()
		if err != nil {
			writeError(w, err, "")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		request, value, err := secretio.Read(w, r)
		if err != nil {
			writeError(w, err, "")
			return
		}
		defer value.Close()
		receipt, err := reader.Import(ctx, request, value)
		respond(w, receipt, err)
	})
}
func (a *Server) credentialRead(w http.ResponseWriter, r *http.Request) {
	reader, err := a.service.Credentials()
	if err != nil {
		writeError(w, err, "")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var result any
	if id := r.PathValue("id"); id != "" {
		result, err = reader.Credential(ctx, id)
	} else {
		q := r.URL.Query()
		result, err = reader.Credentials(ctx, q.Get("limit"), q.Get("cursor"))
	}
	respond(w, result, err)
}
