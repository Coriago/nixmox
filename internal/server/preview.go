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
	if s.Records != nil {
		history, err := s.Records.History(s.Config.Hostname)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		for _, record := range history {
			if record.Status == "success" && record.Kind != "rollback" && record.Mode != "dry-activate" {
				base = record.GitSHA
				break
			}
		}
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
