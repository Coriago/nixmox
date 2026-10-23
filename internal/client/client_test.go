package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"nixmox/internal/domain"
)

func TestUncertainSubmissionReconcilesOriginalReceipt(t *testing.T) {
	for _, accepted := range []bool{true, false} {
		t.Run(map[bool]string{true: "accepted", false: "unknown"}[accepted], func(t *testing.T) {
			var posts, lookups atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					posts.Add(1)
					var command domain.Command
					if err := json.NewDecoder(r.Body).Decode(&command); err != nil || command.ID != "stable" {
						t.Error("original command identity changed")
					}
					connection, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					connection.Close() // Side effect may have happened; reply is lost.
					return
				}
				lookups.Add(1)
				if r.URL.Path != "/api/v1/commands/stable" {
					t.Error("queried a different receipt")
				}
				if !accepted {
					w.WriteHeader(http.StatusNotFound)
					json.NewEncoder(w).Encode(domain.ErrorResponse{Error: &domain.Error{Code: "NOT_FOUND", Message: "No receipt"}})
					return
				}
				id := domain.ID("task")
				json.NewEncoder(w).Encode(domain.Receipt{CommandID: "stable", Admission: "accepted", TaskID: &id})
			}))
			defer server.Close()
			api, err := New(server.URL, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := api.Submit(context.Background(), domain.Command{ID: "stable", Type: "network.check", TargetID: "node", Payload: json.RawMessage("{}")})
			if accepted && (err != nil || receipt.CommandID != "stable") {
				t.Fatalf("receipt reconciliation: %+v %v", receipt, err)
			}
			var unknown *UnknownSubmission
			if !accepted && (!errors.As(err, &unknown) || unknown.CommandID != "stable") {
				t.Fatalf("unknown outcome lost identity: %v", err)
			}
			if posts.Load() != 1 || lookups.Load() != 1 {
				t.Fatalf("unexpected replay: posts=%d lookups=%d", posts.Load(), lookups.Load())
			}
		})
	}
}

func TestRejectedSubmissionDoesNotRetry(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(domain.ErrorResponse{Error: &domain.Error{Code: "IDEMPOTENCY_CONFLICT", Message: "Different request"}})
	}))
	defer server.Close()
	api, _ := New(server.URL, server.Client())
	_, err := api.Submit(context.Background(), domain.Command{ID: "stable", Payload: json.RawMessage("{}")})
	var failure *APIError
	if !errors.As(err, &failure) || failure.StatusCode != http.StatusConflict || calls.Load() != 1 {
		t.Fatalf("rejection retried or masked: %v calls=%d", err, calls.Load())
	}
}
