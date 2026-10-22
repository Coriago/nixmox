// Package machine serves the restricted machine TLS API, separate from the
// administrative console. TLS key possession alone does not grant membership.
package machine

import (
	"encoding/json"
	"io"
	"net/http"

	"nixmox/internal/domain"
	"nixmox/internal/pki"
)

type Enrollment interface {
	SubmitEnrollment(owner string, request domain.EnrollmentRequest) (any, error)
	Read(owner, id string) (any, error)
	Certificates(owner, id string) (any, error)
}

func New(enrollment Enrollment, extra ...func(*http.ServeMux)) http.Handler {
	mux := http.NewServeMux()
	for _, register := range extra {
		register(mux)
	}
	handle := func(action string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
				respond(w, nil, domain.Err("UNAUTHORIZED", "A machine certificate is required.", 401))
				return
			}
			owner := pki.Fingerprint(r.TLS.PeerCertificates[0].PublicKey)
			if enrollment == nil {
				respond(w, nil, domain.Err("QUORUM_UNAVAILABLE", "This node has no cluster enrollment authority.", 503))
				return
			}
			id := r.PathValue("id")
			if action != "submit" && !domain.SafeID.MatchString(id) {
				respond(w, nil, domain.Err("INVALID_QUERY", "Invalid request ID.", 400))
				return
			}
			var v any
			var err error
			switch action {
			case "submit":
				data, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, 48<<10))
				var request domain.EnrollmentRequest
				if readErr != nil || domain.StrictJSON(data, &request) != nil {
					respond(w, nil, domain.Err("INVALID_PAYLOAD", "Invalid enrollment request.", 422))
					return
				}
				v, err = enrollment.SubmitEnrollment(owner, request)
			case "read":
				v, err = enrollment.Read(owner, id)
			case "certificates":
				v, err = enrollment.Certificates(owner, id)
			}
			respond(w, v, err)
		}
	}
	mux.HandleFunc("POST /api/v1/enrollment/requests", handle("submit"))
	mux.HandleFunc("GET /api/v1/enrollment/requests/{id}", handle("read"))
	mux.HandleFunc("POST /api/v1/enrollment/requests/{id}/certificates", handle("certificates"))
	return mux
}
func respond(w http.ResponseWriter, value any, err error) {
	w.Header().Set("Content-Type", "application/json")
	if err != nil {
		e, ok := err.(*domain.Error)
		if !ok {
			e = domain.Err("UNAVAILABLE", "Machine request unavailable.", 503)
		}
		w.WriteHeader(e.Status)
		json.NewEncoder(w).Encode(map[string]any{"error": e})
		return
	}
	json.NewEncoder(w).Encode(value)
}
