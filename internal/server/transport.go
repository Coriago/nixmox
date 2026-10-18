package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"nixmox/internal/cluster"
	"strings"
	"time"

	"nixmox/internal/messaging"
)

type deliveredCommand struct{}

// Transport preserves the HTTP API but all production mutations, including
// local ones, enter the same durable NATS consumer before reaching handlers.
func (s *Server) transport(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/installer/") && r.Context().Value(deliveredCommand{}) == nil {
			http.NotFound(w, r)
			return
		}
		if s.Bus == nil || r.Context().Value(deliveredCommand{}) != nil || r.Method == "GET" || r.Method == "HEAD" {
			next.ServeHTTP(w, r)
			return
		}
		target := s.Config.Hostname
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) >= 4 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "hosts" && !strings.HasSuffix(r.URL.Path, "/actions/start") {
			target = parts[3]
			if target != s.Config.Hostname {
				if _, ok := s.peer(target); !ok {
					http.Error(w, "unknown host", 404)
					return
				}
			}
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, (512<<10)+1))
		if err != nil || len(body) > 512<<10 {
			http.Error(w, "command body exceeds 512 KiB", 413)
			return
		}
		id := r.Header.Get("Idempotency-Key")
		if id == "" {
			id = messaging.NewID()
		}
		w.Header().Set("X-Nixmox-Command-ID", id)
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		req := messaging.Request{ID: id, Target: target, Method: r.Method, Path: r.URL.RequestURI(), Body: body, ContentType: r.Header.Get("Content-Type")}
		var res messaging.Response
		if target != s.Config.Hostname && strings.HasSuffix(r.URL.Path, "/actions/leave") {
			peer, _ := s.peer(target)
			res, err = s.departureCommand(ctx, req, peer)
		} else {
			res, err = s.Bus.Command(ctx, req)
		}
		if err != nil {
			w.Header().Set("X-Nixmox-Command-State", "unknown")
			http.Error(w, err.Error(), 503)
			return
		}
		w.Header().Set("X-Nixmox-Command-State", "completed")
		writeTransportResponse(w, res)
	})
}
func writeTransportResponse(w http.ResponseWriter, res messaging.Response) {
	for key, values := range res.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(res.Status)
	w.Write(res.Body)
}

// Only the in-process NATS consumer can mark a request as delivered. A caller
// supplied HTTP header cannot bypass durable dispatch or target validation.
func (s *Server) handleMessage(ctx context.Context, req messaging.Request) messaging.Response {
	if req.Target != s.Config.Hostname || !strings.HasPrefix(req.Path, "/") || strings.HasPrefix(req.Path, "//") || strings.HasPrefix(req.Path, "/api/v1/events") {
		return messaging.Response{Status: 400, Body: []byte("invalid message destination")}
	}
	ctx = context.WithValue(ctx, deliveredCommand{}, true)
	r, err := http.NewRequestWithContext(ctx, req.Method, req.Path, bytes.NewReader(req.Body))
	if err != nil {
		return messaging.Response{Status: 400, Body: []byte(err.Error())}
	}
	r.Header.Set("Content-Type", req.ContentType)
	limit := 512 << 10
	if req.Method == "GET" {
		limit = 64 << 20
	}
	w := &messageWriter{header: make(http.Header), status: 200, limit: limit}
	s.Mux.ServeHTTP(w, r)
	if w.exceeded {
		return messaging.Response{Status: 502, Body: []byte("host response exceeds transport limit")}
	}
	return messaging.Response{Status: w.status, Header: w.Header(), Body: w.body.Bytes()}
}

// Leave deliberately destroys the route carrying its reply. The command still
// executes through NATS; only its persisted result is recovered over peer HTTP.
func (s *Server) departureCommand(ctx context.Context, req messaging.Request, peer cluster.HostPayload) (messaging.Response, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		response messaging.Response
		err      error
	}
	done := make(chan result, 1)
	go func() { res, err := s.Bus.Command(ctx, req); done <- result{res, err} }()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return messaging.Response{}, ctx.Err()
		case r := <-done:
			return r.response, r.err
		case <-tick.C:
			readCtx, stop := context.WithTimeout(ctx, time.Second)
			request, err := http.NewRequestWithContext(readCtx, "GET", "http://"+peer.HTTPAddress+"/api/v1/hosts/"+req.Target+"/commands/"+req.ID, nil)
			if err == nil {
				response, err := s.Client.Do(request)
				if err == nil {
					var receipt messaging.Receipt
					err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&receipt)
					response.Body.Close()
					if err == nil && response.StatusCode == 200 && receipt.ID == req.ID && receipt.Target == req.Target && receipt.Method == req.Method && receipt.Path == req.Path && receipt.State == "completed" && receipt.Matches(req) {
						stop()
						return receipt.Response, nil
					}
				}
			}
			stop()
		}
	}
}

type messageWriter struct {
	header                http.Header
	body                  bytes.Buffer
	status, limit         int
	wroteHeader, exceeded bool
}

func (w *messageWriter) Header() http.Header { return w.header }
func (w *messageWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
}
func (w *messageWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(200)
	}
	if len(p) > w.limit-w.body.Len() {
		w.exceeded = true
		return 0, fmt.Errorf("response exceeds transport limit")
	}
	return w.body.Write(p)
}
