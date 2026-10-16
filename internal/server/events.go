package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/go-chi/chi/v5"
	"net/http"
	"nixmox/internal/jobs"
	"strings"
	"time"
)

type fragmentWriter struct {
	bytes.Buffer
	header http.Header
	status int
}

func (w *fragmentWriter) Header() http.Header    { return w.header }
func (w *fragmentWriter) WriteHeader(status int) { w.status = status }

// events streams only changed HTML fragments. Each browser subscribes to the
// fragments it displays; no framework, full-page reloads or polling requests.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	paths := r.URL.Query()["partial"]
	if len(paths) > 8 {
		http.Error(w, "too many subscriptions", 400)
		return
	}
	for _, path := range paths {
		if !strings.HasPrefix(path, "/partials/") || strings.ContainsAny(path, "?#\r\n") {
			http.Error(w, "invalid fragment", 400)
			return
		}
	}
	log, err := s.openLog(r.URL.Query().Get("log"))
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	var logChunks <-chan jobs.LogChunk
	var logDone <-chan struct{}
	if log != nil {
		defer log.subscription.Close()
		logChunks, logDone = log.subscription.Updates, log.subscription.Done
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	control := http.NewResponseController(w)
	// Events drive freshness. The timer covers telemetry and TTL expiry when
	// no change event is emitted, and keeps disconnected proxies detectable.
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	previous := map[string]string{}
	catchUp := func() error {
		if log == nil {
			return nil
		}
		if err := s.catchUpLog(r.Context(), w, log); err != nil {
			data, _ := json.Marshal("Log stream unavailable; retrying: " + err.Error())
			_, err = fmt.Fprintf(w, "event: log-error\ndata: %s\n\n", data)
			return err
		}
		return nil
	}
	control.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := catchUp(); err != nil {
		return
	}
	for {
		var changed <-chan struct{}
		if s.Updates != nil {
			changed = s.Updates.Watch()
		}
		changes := map[string]string{}
		errors := map[string]string{}

		for path, fragment := range s.renderFragments(r, paths) {
			if fragment.status != 200 {
				errors[path] = strings.TrimSpace(fragment.String())
				delete(previous, path)
				continue
			}
			if previous[path] != fragment.String() {
				changes[path] = fragment.String()
				previous[path] = fragment.String()
			}
		}

		control.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if len(errors) > 0 {
			data, _ := json.Marshal(errors)
			if _, err := fmt.Fprintf(w, "event: fragment-errors\ndata: %s\n\n", data); err != nil {
				return
			}
		}
		if len(changes) > 0 {
			data, _ := json.Marshal(changes)
			if _, err := fmt.Fprintf(w, "event: fragments\ndata: %s\n\n", data); err != nil {
				return
			}
		} else {
			if _, err := fmt.Fprint(w, "event: heartbeat\ndata: {}\n\n"); err != nil {
				return
			}
		}
		if err := control.Flush(); err != nil {
			return
		}
	waiting:
		for {
			select {
			case <-r.Context().Done():
				return
			case <-logDone:
				return
			case chunk := <-logChunks:
				control.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if log.offset < 0 || chunk.Offset > log.offset {
					if err := catchUp(); err != nil {
						return
					}
				} else if chunk.Next > log.offset {
					if chunk.Offset < log.offset {
						chunk.Data = chunk.Data[log.offset-chunk.Offset:]
						chunk.Offset = log.offset
					}
					if err := log.send(w, chunk); err != nil {
						return
					}
				}
				if err := control.Flush(); err != nil {
					return
				}
			case <-tick.C:
				control.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := catchUp(); err != nil {
					return
				}
				break waiting
			case <-changed:
				select {
				case <-r.Context().Done():
					return
				case <-time.After(100 * time.Millisecond):
				}
				break waiting
			}
		}
	}
}

// A slow/offline peer must not hold up unrelated telemetry or task fragments.
// The subscription limit bounds concurrency to eight requests per stream.
func (s *Server) renderFragments(r *http.Request, paths []string) map[string]*fragmentWriter {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	type result struct {
		path   string
		writer *fragmentWriter
	}
	results := make(chan result, len(paths))
	for _, path := range paths {
		go func() {
			fragment := &fragmentWriter{header: make(http.Header), status: 200}
			// Nested router requests must not share the parent chi routing context.
			requestCtx := context.WithValue(ctx, chi.RouteCtxKey, chi.NewRouteContext())
			req, err := http.NewRequestWithContext(requestCtx, "GET", path, nil)
			if err != nil {
				fragment.status = 400
				fragment.WriteString("Invalid fragment")
			} else {
				req.Header = r.Header.Clone()
				req.Header.Del("Accept")
				req.Host = r.Host
				s.Mux.ServeHTTP(fragment, req)
			}
			results <- result{path, fragment}
		}()
	}
	out := make(map[string]*fragmentWriter, len(paths))
	for range paths {
		select {
		case result := <-results:
			out[result.path] = result.writer
		case <-ctx.Done():
			for _, path := range paths {
				if _, ok := out[path]; !ok {
					f := &fragmentWriter{status: 504}
					f.WriteString("Host request timed out; retrying")
					out[path] = f
				}
			}
			return out
		}
	}
	return out
}
