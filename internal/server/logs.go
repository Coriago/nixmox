package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"nixmox/internal/jobs"
	"nixmox/internal/messaging"
)

// Bounded byte reads provide initial tail and replay after a live-stream gap.
func (s *Server) logChunk(w http.ResponseWriter, r *http.Request) {
	if s.jobTarget(w, r) {
		return
	}
	j, err := s.Store.Get(chi.URLParam(r, "id"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	offset, err := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
	if err != nil || offset < -1 {
		http.Error(w, "invalid log offset", 400)
		return
	}
	f, err := os.Open(j.LogPath)
	if os.IsNotExist(err) {
		respond(w, 200, jobs.LogChunk{Reset: offset != 0})
		return
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	reset := offset < 0 || offset > stat.Size()
	if reset {
		offset = max(int64(0), stat.Size()-(32<<10))
	}
	data := make([]byte, min(int64(32<<10), stat.Size()-offset))
	n, err := f.ReadAt(data, offset)
	if err != nil && err != io.EOF {
		http.Error(w, err.Error(), 500)
		return
	}
	respond(w, 200, jobs.LogChunk{Offset: offset, Next: offset + int64(n), Data: data[:n], Reset: reset})
}

type liveLog struct {
	path         string
	offset       int64
	subscription messaging.LogSubscription
}

func (s *Server) openLog(path string) (*liveLog, error) {
	if path == "" {
		return nil, nil
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 7 || parts[0] != "api" || parts[1] != "v1" || parts[2] != "hosts" || parts[4] != "jobs" || parts[6] != "log" || parts[3] == "" || parts[5] == "" || strings.ContainsAny(path, "?#\r\n") {
		return nil, fmt.Errorf("invalid log subscription")
	}
	if s.Bus == nil {
		return nil, fmt.Errorf("NATS transport unavailable")
	}
	sub, err := s.Bus.SubscribeLog(parts[3], parts[5])
	if err != nil {
		return nil, err
	}
	return &liveLog{path: path, offset: -1, subscription: sub}, nil
}
func (s *Server) catchUpLog(ctx context.Context, w io.Writer, log *liveLog) error {
	parts := strings.Split(strings.Trim(log.path, "/"), "/")
	for range 16 {
		readCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		res, err := s.Bus.Query(readCtx, messaging.Request{Target: parts[3], Method: "GET", Path: log.path + "/chunk?offset=" + strconv.FormatInt(log.offset, 10)})
		cancel()
		if err != nil {
			return err
		}
		if res.Status != 200 {
			return fmt.Errorf("log unavailable: %s", res.Body)
		}
		var chunk jobs.LogChunk
		if err = json.Unmarshal(res.Body, &chunk); err != nil {
			return err
		}
		if err = log.send(w, chunk); err != nil {
			return err
		}
		if len(chunk.Data) < 32<<10 {
			return nil
		}
	}
	return nil
}
func (log *liveLog) send(w io.Writer, chunk jobs.LogChunk) error {
	if !chunk.Reset && chunk.Next <= log.offset {
		return nil
	}
	data, err := json.Marshal(chunk)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintf(w, "event: log\ndata: %s\n\n", data); err != nil {
		return err
	}
	log.offset = chunk.Next
	return nil
}
