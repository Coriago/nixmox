package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
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
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	control := http.NewResponseController(w)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	previous := map[string]string{}
	for {
		changes := map[string]string{}
		for _, path := range paths {
			req, err := http.NewRequestWithContext(r.Context(), "GET", path, nil)
			if err != nil {
				continue
			}
			req.Header = r.Header.Clone()
			req.Header.Del("Accept")
			req.Host = r.Host
			fragment := &fragmentWriter{header: make(http.Header), status: 200}
			s.Mux.ServeHTTP(fragment, req)
			if fragment.status == 200 && previous[path] != fragment.String() {
				changes[path] = fragment.String()
				previous[path] = fragment.String()
			}
		}
		control.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if len(changes) > 0 {
			data, _ := json.Marshal(changes)
			if _, err := fmt.Fprintf(w, "event: fragments\ndata: %s\n\n", data); err != nil {
				return
			}
		} else {
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
		}
		if err := control.Flush(); err != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
	}
}
