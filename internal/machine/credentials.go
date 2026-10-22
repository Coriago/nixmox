package machine

import (
	"context"
	"crypto/x509"
	"net/http"
	"nixmox/internal/domain"
	"nixmox/internal/secretio"
	"nixmox/internal/service"
	"time"
)

func CredentialRoutes(importer service.CredentialService, node string, authorize func(context.Context, *x509.Certificate) error) func(*http.ServeMux) {
	return func(mux *http.ServeMux) {
		mux.HandleFunc("POST /api/v1/credentials/import", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if r.TLS == nil || len(r.TLS.PeerCertificates) != 1 {
				respond(w, nil, domain.Err("UNAUTHORIZED", "An authorized full-member certificate is required.", 401))
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
			defer cancel()
			if authorize == nil {
				respond(w, nil, domain.Err("FORBIDDEN", "Credential forwarding is unavailable.", 403))
				return
			}
			if err := authorize(ctx, r.TLS.PeerCertificates[0]); err != nil {
				respond(w, nil, err)
				return
			}
			request, value, err := secretio.Read(w, r)
			if err != nil {
				respond(w, nil, err)
				return
			}
			defer value.Close()
			if request.StorageNodeID != node {
				respond(w, nil, domain.Err("FORBIDDEN", "A machine import may store only on its own node and never forwards again.", 403))
				return
			}
			receipt, err := importer.Import(ctx, request, value)
			respond(w, receipt, err)
		})
	}
}
