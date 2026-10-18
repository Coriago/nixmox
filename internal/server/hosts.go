package server

import (
	"encoding/json"
	"github.com/go-chi/chi/v5"
	"net/http"
	"nixmox/internal/cluster"
	"nixmox/internal/hostinfo"
	"nixmox/internal/jobs"
	"nixmox/internal/portal"
	"time"
)

func (s *Server) hostRows() []portal.HostRow {
	if s.Cluster != nil && s.View != nil && s.Cluster.State(0).Status == "clustered" {
		var rows []portal.HostRow
		for _, p := range s.View.List() {
			row := portal.HostRow{ConsoleAddress: p.HTTPAddress, Host: p.Info(), Latest: p.ApplyRecord, Online: p.Online, PulledSHA: p.PulledSHA, RemoteSHA: p.RemoteSHA, Active: p.ActiveDeployment, Unavailable: p.StatusUnavailable, SampledAt: p.ObservedAt}
			row.Temporary = p.Temporary
			if !p.Online {
				row.Active = nil
			}
			if p.NodeID == s.Config.Hostname {
				row = s.localHostRow()
			}
			rows = append(rows, row)
		}
		return rows
	}
	return []portal.HostRow{s.localHostRow()}
}

func (s *Server) localHostRow() portal.HostRow {
	h := hostinfo.Get(s.Config)
	row := portal.HostRow{Host: h, Latest: s.latestRecord(h.Hostname), Online: true, SampledAt: time.Now()}
	row.Temporary = s.Config.InstallerMode
	if s.Git != nil {
		state := s.Git.State()
		row.PulledSHA, row.RemoteSHA = state.LastPulledSHA, state.RemoteSHA
	}
	if s.Store != nil {
		var err error
		row.Active, err = jobs.ActiveDeployment(s.Store)
		row.Unavailable = err != nil
	}

	return row
}

func (s *Server) latestRecord(nodeID string) *cluster.Record {
	if s.Records == nil {
		return nil
	}
	r, ok, err := s.Records.Latest(nodeID)
	if err != nil || !ok {
		return nil
	}
	return &r
}

func (s *Server) clusterState() cluster.ClusterState {
	if s.Cluster == nil {
		return cluster.ClusterState{Status: "unclustered"}
	}
	count := 0
	if s.View != nil {
		count = s.View.Count()
	}
	return s.Cluster.State(count)
}

func (s *Server) MountApply(p *portal.Portal) {
	s.Mux.Get("/partials/hosts/{nodeID}/age", func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "nodeID")
		if s.forward(w, r, id) {
			return
		}
		if id != s.Config.Hostname {
			http.NotFound(w, r)
			return
		}
		p.AgeRecipient(w, hostinfo.Get(s.Config))
	})
	s.Mux.Get("/partials/hosts/{nodeID}/sync", func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "nodeID")
		if s.forward(w, r, id) {
			return
		}
		p.SyncPartial(w, s.localHostRow())
	})

	s.Mux.Post("/api/v1/apply", func(w http.ResponseWriter, r *http.Request) {
		s.runApplyKind(w, r, false)
	})

	s.Mux.Post("/api/v1/rollback", func(w http.ResponseWriter, r *http.Request) {
		s.runApplyKind(w, r, true)
	})

	s.Mux.Get("/api/v1/hosts/{nodeID}", func(w http.ResponseWriter, r *http.Request) {
		nodeID := chi.URLParam(r, "nodeID")
		if s.forward(w, r, nodeID) {
			return
		}
		h := hostinfo.Get(s.Config)
		if nodeID != h.Hostname {
			http.Error(w, "unknown host", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"hostname":            h.Hostname,
			"age_recipient":       h.AgeRecipient,
			"age_recipient_error": h.AgeRecipientError,
			"resources":           h.Resources,
			"kernel":              h.Kernel,
			"nixos_version":       h.NixOSVersion,
			"agent_version":       h.AgentVersion,
			"uptime":              h.Uptime,
			"status":              "online",
			"auto_apply":          s.Config.AutoApply,
			"apply_mode":          s.Config.ApplyMode,
			"apply_record":        s.latestRecord(nodeID),
		})
	})

	s.Mux.Get("/api/v1/hosts/{nodeID}/applylog", func(w http.ResponseWriter, r *http.Request) {
		nodeID := chi.URLParam(r, "nodeID")
		var records []cluster.Record
		if s.Records != nil {
			var err error
			records, err = s.Records.History(nodeID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if records == nil {
			records = []cluster.Record{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(records)
	})

	s.Mux.Get("/hosts/{nodeID}", func(w http.ResponseWriter, r *http.Request) {
		nodeID := chi.URLParam(r, "nodeID")
		h := hostinfo.Get(s.Config)
		view := portal.HostDetailView{NodeID: nodeID, Latest: s.latestRecord(nodeID)}
		if nodeID == h.Hostname {
			view.Host = h
		} else if s.View != nil {
			found := false
			for _, p := range s.View.List() {
				if p.NodeID == nodeID {
					view.Host = p.Info()
					view.Latest = p.ApplyRecord
					found = true
					break
				}
			}
			if !found {
				http.Error(w, "unknown host", http.StatusNotFound)
				return
			}
		} else {
			http.Error(w, "unknown host", http.StatusNotFound)
			return
		}
		if s.Records != nil {
			view.History, _ = s.Records.History(nodeID)
		}
		p.HostDetail(w, view)
	})

	s.Mux.Get("/partials/hosts/{nodeID}/joblog", func(w http.ResponseWriter, r *http.Request) {
		nodeID := chi.URLParam(r, "nodeID")
		h := hostinfo.Get(s.Config)
		if nodeID != h.Hostname {
			http.Error(w, "job logs are local to each host", http.StatusNotFound)
			return
		}
		view := portal.HostDetailView{NodeID: nodeID, LocalLogs: true}
		s.fillRecentJobLog(&view, true)
		p.HostJobLogPartial(w, view)
	})

	s.Mux.Get("/partials/hosts/{nodeID}/applylog", func(w http.ResponseWriter, r *http.Request) {
		nodeID := chi.URLParam(r, "nodeID")
		var history []cluster.Record
		if s.Records != nil {
			history, _ = s.Records.History(nodeID)
		}
		p.HostDetailPartial(w, portal.HostDetailView{NodeID: nodeID, History: history})
	})
}

func (s *Server) runApplyKind(w http.ResponseWriter, r *http.Request, rollback bool) {
	if s.Apply == nil {
		http.Error(w, "apply not available", http.StatusServiceUnavailable)
		return
	}
	var j jobs.Job
	var err error
	if rollback {
		j, err = s.Apply.Rollback(r.Context())
	} else {
		j, err = s.Apply.Apply(r.Context())
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"id": j.ID})
}
