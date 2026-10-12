package server

import (
	"context"
	"net/http"
	"nixmox/internal/gitops"
	"time"
)

func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	previewer, ok := s.Git.(interface {
		Preview(context.Context, string) (gitops.Preview, error)
	})
	if !ok {
		http.Error(w, "preview unavailable", 503)
		return
	}
	base := ""
	if latest := s.latestRecord(s.Config.Hostname); latest != nil {
		base = latest.GitSHA
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	p, err := previewer.Preview(ctx, base)
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	respond(w, 200, p)
}
