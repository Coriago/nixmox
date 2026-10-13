package cluster

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"nixmox/internal/config"
	"nixmox/internal/natsx"
)

type ClusterState struct {
	Status      string `json:"status"` // "unclustered" | "clustered"
	ClusterName string `json:"cluster_name"`
	MemberCount int    `json:"member_count"`
	NATSRoute   string `json:"nats_route"`
}

// Supervisor owns the embedded NATS lifecycle and is the ONLY component
// allowed to reconfigure it. Reconfigure restarts the embedded server
// in-process; the HTTP server, job runner, and cron are never touched —
// in-flight nixos-rebuild jobs survive a reconfigure.
type Supervisor struct {
	cfg       *config.Config
	dataDir   string
	clusterIP string

	// OnReconfigure fires once the JetStream meta-leader is ready after an
	// embedded-server (re)start — async, since election can take seconds and
	// must not block join handshakes. The cluster Runtime arms off this.
	OnReconfigure func(ctx context.Context, emb *natsx.EmbeddedServer)

	transition       sync.Mutex
	generationCancel context.CancelFunc
	mu               sync.RWMutex
	emb              *natsx.EmbeddedServer
	ident            nodeIdentity
	gen              int
	// inClusterMode tracks whether the embedded server runs with a Cluster
	// block. A created-but-alone cluster stays in standalone JetStream mode
	// until the first peer joins (clustered JS requires a real route).
	inClusterMode bool
}

func NewSupervisor(cfg *config.Config, dataDir, clusterIP string) *Supervisor {
	return &Supervisor{cfg: cfg, dataDir: dataDir, clusterIP: clusterIP}
}

// Start boots embedded NATS, restoring cluster identity from node.json when
// present (restart/rejoin path) or standalone otherwise.
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()

	ident, err := loadIdentity(s.dataDir)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("load node identity: %w", err)
	}
	if ident.NodeID == "" {
		ident = nodeIdentity{NodeID: s.cfg.Hostname, Hostname: s.cfg.Hostname}
	}
	s.ident = ident

	spec := natsx.ClusterSpec{}
	if ident.ClusterName != "" && len(ident.Routes) > 0 {
		spec = natsx.ClusterSpec{Name: ident.ClusterName, Routes: ident.Routes}
	}
	err = s.startLocked(spec)
	s.mu.Unlock()
	return err
}

func (s *Supervisor) startLocked(spec natsx.ClusterSpec) error {
	opts, err := natsx.NewOptions(s.ident.NodeID, s.cfg.NATSPort, s.cfg.ClusterPort, s.clusterIP, s.dataDir, spec)
	if err != nil {
		return err
	}
	emb, err := natsx.Start(opts)
	if err != nil {
		return err
	}
	genCtx, genCancel := context.WithCancel(context.Background())
	s.generationCancel = genCancel
	s.emb = emb
	s.inClusterMode = spec.Name != ""
	s.gen++
	gen := s.gen

	// Meta-leader election needs routes up on BOTH sides of a join, so it
	// can't block the join handshake: wait async, then arm the runtime.
	go func() {
		for {
			if err := natsx.WaitReadyContext(genCtx, emb.JS, 90*time.Second); err == nil {
				break
			} else if genCtx.Err() != nil {
				return
			} else {
				log.Printf("cluster: waiting for readiness: %v", err)
			}
		}

		s.mu.RLock()
		current := s.emb == emb && s.gen == gen
		hook := s.OnReconfigure
		s.mu.RUnlock()
		if current && hook != nil {
			hook(genCtx, emb)
		}
	}()
	return nil
}

// Reconfigure swaps the running embedded server for one with the given
// cluster spec. Callers must NOT hold locks that heartbeat/watchers need.
func (s *Supervisor) Reconfigure(ctx context.Context, spec natsx.ClusterSpec) error {
	s.mu.Lock()
	if s.generationCancel != nil {
		s.generationCancel()
	}
	if s.emb != nil {
		s.emb.Stop()
		s.emb = nil
	}
	err := s.startLocked(spec)
	s.mu.Unlock()
	return err
}

func (s *Supervisor) Stop() {
	s.mu.Lock()
	if s.generationCancel != nil {
		s.generationCancel()
	}
	defer s.mu.Unlock()
	if s.emb != nil {
		s.emb.Stop()
		s.emb = nil
	}
}

func (s *Supervisor) Embedded() *natsx.EmbeddedServer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.emb
}

func (s *Supervisor) AutoJoinAllowed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ident.ClusterName == "" && !s.ident.Left
}

func (s *Supervisor) State(memberCount int) ClusterState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st := ClusterState{Status: "unclustered", MemberCount: memberCount}
	if s.ident.ClusterName != "" {
		st.Status = "clustered"
		st.ClusterName = s.ident.ClusterName
		st.NATSRoute = fmt.Sprintf("nats://%s:%d", s.clusterIP, s.cfg.ClusterPort)
	}
	return st
}

// CreateCluster turns a standalone node into a single-node cluster.
// Cluster names are immutable while joined: create-once, no renames.
// The embedded server stays in standalone JetStream mode until the first
// peer joins — clustered JetStream cannot start without a real route.
func (s *Supervisor) CreateCluster(ctx context.Context, name string) error {
	s.transition.Lock()
	defer s.transition.Unlock()
	if name == "" {
		return fmt.Errorf("cluster name is required")
	}
	s.mu.RLock()
	existing := s.ident.ClusterName
	s.mu.RUnlock()
	if existing != "" {
		return fmt.Errorf("already in cluster %q (renames not supported)", existing)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.ident
	next.ClusterName = name
	next.Left = false
	if err := saveIdentity(s.dataDir, next); err != nil {
		return err
	}
	s.ident = next
	log.Printf("cluster: created %q (standalone until first peer joins)", name)
	return nil
}

// JoinCluster joins this node to the cluster reachable at seedHTTPAddr
// (host:port of the seed's HTTP API). The CLI and portal both funnel here
// via the server's /api/v1/cluster/join/initiate handler — one code path.
func (s *Supervisor) JoinCluster(ctx context.Context, seedHTTPAddr string) error {
	s.transition.Lock()
	defer s.transition.Unlock()
	s.mu.RLock()
	existing := s.ident.ClusterName
	s.mu.RUnlock()
	if existing != "" {
		return fmt.Errorf("already in cluster %q", existing)
	}

	boot, err := FetchBootstrap(ctx, seedHTTPAddr)
	if err != nil {
		return fmt.Errorf("contact seed: %w", err)
	}
	if boot.Status != "clustered" {
		return fmt.Errorf("seed is not clustered (status %q) — create a cluster there first", boot.Status)
	}

	resp, err := RequestJoin(ctx, seedHTTPAddr, JoinRequest{
		NodeID:            s.cfg.Hostname,
		Hostname:          s.cfg.Hostname,
		ClusterAddr:       fmt.Sprintf("nats://%s:%d", s.clusterIP, s.cfg.ClusterPort),
		TargetClusterName: boot.ClusterName,
	})
	if err != nil {
		return err
	}

	routes := dedupe(append([]string{boot.NATSRoute}, resp.KnownPeers...))

	s.mu.Lock()
	s.ident.ClusterName = resp.ClusterName
	s.ident.Left = false
	s.ident.Routes = routes
	if err := saveIdentity(s.dataDir, s.ident); err != nil {
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()

	if err := s.Reconfigure(ctx, natsx.ClusterSpec{Name: resp.ClusterName, Routes: routes}); err != nil {
		return err
	}
	log.Printf("cluster: joined %q via %s", resp.ClusterName, seedHTTPAddr)
	return nil
}

// AcceptJoin is the PEER side: validate + record an inbound join. On the
// first inbound join this node transitions from standalone to clustered
// JetStream (reconfiguring with the joiner as its first route); the HTTP
// server keeps serving throughout. Returns what the joiner needs.
func (s *Supervisor) AcceptJoin(req JoinRequest) (JoinResponse, error) {
	s.transition.Lock()
	defer s.transition.Unlock()
	s.mu.RLock()
	name := s.ident.ClusterName
	routes := append([]string(nil), s.ident.Routes...)
	clustered := s.inClusterMode
	s.mu.RUnlock()

	if name == "" {
		return JoinResponse{}, ErrNotClustered
	}
	if req.TargetClusterName != name {
		return JoinResponse{}, ErrClusterNameMismatch
	}
	if req.NodeID == s.cfg.Hostname {
		return JoinResponse{}, fmt.Errorf("hostname collision: %s", req.NodeID)
	}
	if req.ClusterAddr == "" {
		return JoinResponse{}, fmt.Errorf("join request missing cluster_addr")
	}

	{
		s.mu.Lock()
		s.ident.Routes = dedupe(append(s.ident.Routes, req.ClusterAddr))
		if err := saveIdentity(s.dataDir, s.ident); err != nil {
			s.mu.Unlock()
			return JoinResponse{}, err
		}
		spec := natsx.ClusterSpec{Name: name, Routes: s.ident.Routes}
		s.mu.Unlock()

		if !clustered {
			if err := s.Reconfigure(context.Background(), spec); err != nil {
				return JoinResponse{}, fmt.Errorf("cluster-mode transition: %w", err)
			}
		}
		log.Printf("cluster: accepted peer %s", req.NodeID)
	}

	self := fmt.Sprintf("nats://%s:%d", s.clusterIP, s.cfg.ClusterPort)
	peers := append([]string{self}, routes...)
	return JoinResponse{ClusterName: name, KnownPeers: dedupe(peers)}, nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
