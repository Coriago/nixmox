package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"nixmox/internal/cluster"
	"nixmox/internal/hostinfo"
	"nixmox/internal/jobs"
	"nixmox/internal/portal"
	"nixmox/internal/power"
)

func sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, e := url.Parse(origin)
				if e != nil || u.Host != r.Host {
					http.Error(w, "cross-origin mutation forbidden", 403)
					return
				}
			}
			if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
				http.Error(w, "cross-site mutation forbidden", 403)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		}
		next.ServeHTTP(w, r)
	})
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func decode(r *http.Request, v any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("expected one JSON object")
	}
	return nil
}

func (s *Server) peer(id string) (cluster.HostPayload, bool) {
	if s.View != nil {
		for _, p := range s.View.List() {
			if p.NodeID == id {
				return p, true
			}
		}
	}
	return cluster.HostPayload{}, false
}

// Remote actions are resolved only through the cluster inventory, never an
// arbitrary URL supplied by a request. Forwarding is limited to one hop.
func (s *Server) forward(w http.ResponseWriter, r *http.Request, id string) bool {
	if id == s.Config.Hostname {
		return false
	}
	p, ok := s.peer(id)
	if !ok || !p.Online || p.HTTPAddress == "" {
		http.Error(w, "host unavailable", 503)
		return true
	}
	if r.Header.Get("X-Nixmox-Forwarded") != "" {
		http.Error(w, "peer identity mismatch", 409)
		return true
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, "http://"+p.HTTPAddress+r.URL.RequestURI(), r.Body)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return true
	}
	req.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	req.Header.Set("X-Nixmox-Forwarded", "1")
	resp, err := s.Client.Do(req)
	if err != nil {
		http.Error(w, err.Error(), 502)
		return true
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, io.LimitReader(resp.Body, 4<<20))
	return true
}
func (s *Server) mountOperations(p *portal.Portal) {
	s.Mux.Get("/api/v1/events", s.events)
	s.Mux.Get("/partials/logs", func(w http.ResponseWriter, r *http.Request) {
		if s.Logs != nil {
			io.WriteString(w, html.EscapeString(s.Logs.String()))
		}
	})
	s.Mux.Get("/partials/rollout", func(w http.ResponseWriter, r *http.Request) {
		s.rolloutMu.Lock()
		defer s.rolloutMu.Unlock()
		b, _ := json.MarshalIndent(s.rollout, "", "  ")
		io.WriteString(w, html.EscapeString(string(b)))
	})
	s.Mux.Get("/api/v1/discovery", func(w http.ResponseWriter, r *http.Request) {
		if s.Discoveries == nil {
			respond(w, 200, []any{})
			return
		}
		respond(w, 200, s.Discoveries.List())
	})
	s.Mux.Get("/api/v1/logs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if s.Logs != nil {
			io.WriteString(w, s.Logs.String())
		}
	})
	s.Mux.Get("/manage", func(w http.ResponseWriter, r *http.Request) { p.Manage(w) })
	s.Mux.Get("/partials/hosts/{nodeID}/resources", func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "nodeID")
		h := hostinfo.Get(s.Config)
		if id != h.Hostname {
			peer, ok := s.peer(id)
			if !ok {
				http.NotFound(w, r)
				return
			}
			h = peer.Info()
		}
		p.Resources(w, h)
	})
	s.Mux.Post("/api/v1/hosts/{nodeID}/actions/{action}", s.hostAction)
	s.Mux.Get("/api/v1/hosts/{nodeID}/variables", s.variables)
	s.Mux.Put("/api/v1/hosts/{nodeID}/variables", s.variables)
	s.Mux.Post("/api/v1/provision", s.provision)
	s.Mux.Post("/api/v1/cluster/forget", s.forgetHost)
	s.Mux.Post("/api/v1/rollout", s.startRollout)
	s.Mux.Get("/api/v1/rollout", func(w http.ResponseWriter, r *http.Request) {
		s.rolloutMu.Lock()
		defer s.rolloutMu.Unlock()
		respond(w, 200, s.rollout)
	})
	s.Mux.Get("/api/v1/gitops/preview", s.preview)
}
func (s *Server) hostAction(w http.ResponseWriter, r *http.Request) {
	id, action := chi.URLParam(r, "nodeID"), chi.URLParam(r, "action")
	switch action {
	case "apply", "rollback", "shutdown", "reboot", "start", "leave":
	default:
		http.Error(w, "unknown action", 400)
		return
	}
	if action == "start" {
		if !s.Config.EnablePower {
			http.Error(w, "power control disabled", 403)
			return
		}
		peer, ok := s.peer(id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		var mac string
		for _, i := range peer.Resources.Interfaces {
			if i.MAC != "" && i.Name != "lo" {
				mac = i.MAC
				break
			}
		}
		if err := power.Wake(mac); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		respond(w, 202, map[string]string{"status": "magic packet sent; firmware Wake-on-LAN must be enabled"})
		return
	}
	if s.forward(w, r, id) {
		return
	}
	if action == "apply" || action == "rollback" {
		s.runApplyKind(w, r, action == "rollback")
		return
	}
	if r.FormValue("confirm") != id {
		http.Error(w, "confirm must match target hostname", 400)
		return
	}
	if action == "leave" {
		leaver, ok := s.Cluster.(interface{ Leave(context.Context) error })
		if !ok {
			http.Error(w, "leave unavailable", 503)
			return
		}
		peers := []cluster.HostPayload{}
		if s.View != nil {
			peers = s.View.List()
		}
		route := s.clusterState().NATSRoute
		if preparer, ok := s.Cluster.(interface {
			PrepareLeave(context.Context, int) error
		}); ok {
			if err := preparer.PrepareLeave(r.Context(), len(peers)-1); err != nil {
				http.Error(w, err.Error(), 409)
				return
			}
		}
		if err := leaver.Leave(r.Context()); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		warnings := []string{}
		for _, peer := range peers {
			if peer.NodeID != id {
				if err := s.requestPeer(r.Context(), peer, "POST", "/api/v1/cluster/forget", map[string]string{"node_id": id, "route": route}, nil); err != nil {
					warnings = append(warnings, peer.NodeID+": "+err.Error())
				}
			}
		}
		if s.View != nil {
			s.View.Clear()
		}
		respond(w, 200, map[string]any{"status": "unclustered", "warnings": warnings})
		return
	}
	if !s.Config.EnablePower {
		http.Error(w, "power control disabled", 403)
		return
	}
	flag := "-h"
	if action == "reboot" {
		flag = "-r"
	}
	// One minute permits the HTTP reply and job record to reach disk first.
	j, err := s.Runner.Submit(jobs.Job{Kind: action, Host: id, Args: []string{flag, "+1", "Scheduled by nixmox"}})
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	respond(w, 202, j)
}

var targetPattern = regexp.MustCompile(`^(root@)?[a-zA-Z0-9][a-zA-Z0-9.:-]*$`)

func (s *Server) provision(w http.ResponseWriter, r *http.Request) {
	if !s.Config.EnableProvision {
		http.Error(w, "provisioning disabled", 403)
		return
	}
	var req struct {
		Target  string `json:"target"`
		Flake   string `json:"flake"`
		Confirm string `json:"confirm"`
	}
	if err := decode(r, &req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if !targetPattern.MatchString(req.Target) || req.Confirm != req.Target || !strings.Contains(req.Flake, "#") || strings.HasPrefix(req.Flake, "-") {
		http.Error(w, "valid SSH target, flake#host and exact target confirmation required", 400)
		return
	}
	j, err := s.Runner.Submit(jobs.Job{Kind: "provision", Host: req.Target, Args: []string{"--flake", req.Flake, "--target-host", req.Target}})
	if err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	respond(w, 202, j)
}

// requestPeer is also used by rollout orchestration; it preserves status errors.
func (s *Server) requestPeer(ctx context.Context, p cluster.HostPayload, method, path string, body any, out any) error {
	var b bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&b).Encode(body); err != nil {
			return err
		}
	}
	if _, _, err := net.SplitHostPort(p.HTTPAddress); err != nil {
		return fmt.Errorf("host %s has no reachable HTTP address", p.NodeID)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+p.HTTPAddress+path, &b)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: %s", resp.Status, string(b))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}
