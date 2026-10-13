package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"

	"nixmox/internal/hostinfo"
	"nixmox/internal/jobs"
	"nixmox/internal/portal"
)

func (s *Server) MountHandlers(p *portal.Portal, auth Middleware) {
	s.Mux.Use(auth.Wrap)
	s.Mux.Use(sameOrigin)
	s.mountOperations(p)

	s.Mux.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	s.Mux.Get("/api/v1/hosts", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if s.Cluster != nil && s.Cluster.State(0).Status == "clustered" && s.View != nil {
			json.NewEncoder(w).Encode(s.View.List())
			return
		}
		hosts := []hostinfo.HostInfo{hostinfo.Get(s.Config)}
		json.NewEncoder(w).Encode(hosts)
	})

	s.Mux.Get("/api/v1/host", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(hostinfo.Get(s.Config))
	})

	s.Mux.Get("/api/v1/bootstrap", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if s.Cluster == nil {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":       "unclustered",
				"cluster_name": "",
				"member_count": 0,
			})
			return
		}
		count := 0
		if s.View != nil {
			count = s.View.Count()
		}
		json.NewEncoder(w).Encode(s.Cluster.State(count))
	})

	s.mountJobs(p)

	s.Mux.Get("/", func(w http.ResponseWriter, r *http.Request) {
		p.Index(w, s.hostRows(), s.clusterState())
	})

	s.Mux.Get("/partials/hosts", func(w http.ResponseWriter, r *http.Request) {
		p.HostsPartial(w, s.hostRows())
	})

	s.Mux.Handle("/assets/*", p.AssetsHandler())
}

func readLogTail(job jobs.Job, maxLines int) []string {
	f, err := os.Open(job.LogPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	const maxBytes = 256 << 10
	info, err := f.Stat()
	if err != nil {
		return nil
	}
	truncated := info.Size() > maxBytes
	if truncated {
		if _, err = f.Seek(-maxBytes, io.SeekEnd); err != nil {
			return nil
		}
	}
	logData, _ := io.ReadAll(io.LimitReader(f, maxBytes))
	if truncated {
		if i := bytes.IndexByte(logData, '\n'); i >= 0 {
			logData = logData[i+1:]
		}
	}
	lines := strings.Split(string(logData), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return lines
}

func (s *Server) fillRecentJobLog(view *portal.HostDetailView, local bool) {
	view.LocalLogs = local
	if !local || s.Store == nil {
		return
	}
	list, err := s.Store.List()
	if err != nil {
		return
	}
	for _, j := range list {
		if j.Host == view.NodeID {
			view.RecentJobID = j.ID
			view.RecentLog = readLogTail(j, 200)
			return
		}
	}
}
