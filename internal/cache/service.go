// Package cache serves verified local NARs and streams eligible peer outputs.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"net/http"
	"nixmox/internal/config"
	"nixmox/internal/credentials"
	"nixmox/internal/domain"
	"nixmox/internal/nixstore"
	"nixmox/internal/service"
	"nixmox/internal/state"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type binding struct {
	Version      int64           `json:"version"`
	CredentialID *string         `json:"credential_id"`
	Commands     map[string]bool `json:"commands"`
}
type Service struct {
	Config            config.Cache
	Network           config.Network
	HTTPPort          int
	TrustFile         string
	Identity          func() domain.Identity
	Full              func(context.Context, string) bool
	Vault             *credentials.Vault
	State             state.Store
	Hosts             service.HostReader
	Client            func(context.Context, string) (*http.Client, error)
	Daemon            nixstore.Daemon
	AuthorizePending  func(context.Context, string, string) bool
	exports           chan struct{}
	pulls             chan struct{}
	lookups           chan struct{}
	active            atomic.Int64
	bytesExported     atomic.Int64
	bytesReceived     atomic.Int64
	failures          atomic.Uint64
	lookupCount       atomic.Uint64
	lookupNanoseconds atomic.Uint64
	peerFailureCount  atomic.Uint64
	cancellations     atomic.Uint64
	mu                sync.Mutex
	bindingMu         sync.Mutex
	version           int64
	lastState         string
	peerState         map[string]string
	peerFailures      map[string]int
	Changed           func()
}

func New(c config.Cache, port int) *Service {
	count := c.MaxConcurrentTransfers
	if count < 1 || count > 64 {
		count = 4
	}
	return &Service{Config: c, HTTPPort: port, exports: make(chan struct{}, count), pulls: make(chan struct{}, count), lookups: make(chan struct{}, 8), peerState: map[string]string{}, peerFailures: map[string]int{}, version: 1}
}
func (s *Service) LocalURL() string {
	return "http://127.0.0.1:" + strconv.Itoa(s.HTTPPort) + "/cache/"
}
func (s *Service) SetChanged(fn func()) { s.mu.Lock(); defer s.mu.Unlock(); s.Changed = fn }
func (s *Service) selection(ctx context.Context) (binding, error) {
	b := binding{Version: 1, CredentialID: s.Config.SigningCredentialRef, Commands: map[string]bool{}}
	if s.Config.SigningCredentialRef != nil {
		return b, nil
	}
	var runtime binding
	_, err := s.State.Get(ctx, "cache_binding."+s.Identity().NodeID, &runtime)
	if missing(err) {
		return b, nil
	}
	if err != nil {
		return b, err
	}
	return runtime, nil
}
func missing(err error) bool { var e *domain.Error; return errors.As(err, &e) && e.Code == "NOT_FOUND" }
func (s *Service) signer(ctx context.Context) (signature.SecretKey, string, error) {
	var empty signature.SecretKey
	if !s.Config.Serve {
		return empty, "SERVING_DISABLED", nil
	}
	if s.Full == nil || !s.Full(ctx, s.Identity().NodeID) {
		return empty, "MEMBERSHIP_INELIGIBLE", nil
	}
	b, err := s.selection(ctx)
	if err != nil {
		return empty, "SIGNING_CREDENTIAL_UNAVAILABLE", err
	}
	if b.CredentialID == nil {
		return empty, "SIGNING_CREDENTIAL_REQUIRED", nil
	}
	kind, value, err := s.Vault.Secret(ctx, *b.CredentialID)
	if err != nil {
		return empty, "SIGNING_CREDENTIAL_UNAVAILABLE", err
	}
	defer func() {
		for i := range value {
			value[i] = 0
		}
	}()
	if kind != "nix-cache-signing-key" {
		return empty, "SIGNING_CREDENTIAL_UNAVAILABLE", nil
	}
	check, err := credentials.Validate(kind, value)
	if err != nil {
		return empty, "SIGNING_CREDENTIAL_UNAVAILABLE", err
	}
	c, err := s.Vault.Credential(ctx, *b.CredentialID)
	if err != nil || c.PublicKey == nil || check.PublicKey == nil || *c.PublicKey != *check.PublicKey {
		return empty, "SIGNING_CREDENTIAL_UNAVAILABLE", err
	}
	key, err := signature.LoadSecretKey(strings.TrimSpace(string(value)))
	if err != nil {
		return empty, "SIGNING_CREDENTIAL_UNAVAILABLE", domain.Err("CREDENTIAL_UNAVAILABLE", "Cache signing credential is invalid.", 503)
	}
	found := false
	for _, node := range c.Scope.CacheNodeIDs {
		if node == s.Identity().NodeID {
			found = true
		}
	}
	if !found {
		return empty, "SIGNING_CREDENTIAL_UNAVAILABLE", domain.Err("TRUST_UNAVAILABLE", "Signer public trust has not been published.", 503)
	}
	return key, "", nil
}
func (s *Service) Reconcile(ctx context.Context) error {
	s.bindingMu.Lock()
	defer s.bindingMu.Unlock()
	if s.Full == nil || !s.Full(ctx, s.Identity().NodeID) {
		return nil
	}
	b, err := s.selection(ctx)
	if err != nil {
		return err
	}
	if b.CredentialID == nil {
		return nil
	}
	if err := s.Vault.Bind(ctx, *b.CredentialID, "cache", s.Identity().NodeID); err != nil {
		return err
	}
	return s.Vault.PruneBinding(ctx, b.CredentialID, "cache", s.Identity().NodeID)
}
func (s *Service) Start() func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			round, stop := context.WithTimeout(ctx, 5*time.Second)
			_ = s.Reconcile(round)
			_ = s.syncTrust(round)
			_, _ = s.Status(round)
			stop()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}
func (s *Service) Status(ctx context.Context) (domain.CacheStatus, error) {
	b, selectionErr := s.selection(ctx)
	_, reason, _ := s.signer(ctx)
	status := domain.CacheStatus{NodeID: s.Identity().NodeID, SettingsVersion: b.Version, ObservedAt: time.Now().UTC(), LocalURL: s.LocalURL(), GatewayState: "ready", Serving: reason == "", SigningCredentialRef: b.CredentialID, Declared: s.Config.SigningCredentialRef != nil, ActiveTransfers: s.active.Load(), BytesExported: s.bytesExported.Load(), BytesReceived: s.bytesReceived.Load(), Failures: s.failures.Load(), Peers: []domain.CachePeer{}}
	if !s.Config.Enable {
		status.GatewayState = "disabled"
	}
	if reason != "" {
		status.ServingReason = &reason
	}
	if selectionErr != nil && reason == "" {
		reason = "SIGNING_CREDENTIAL_UNAVAILABLE"
		status.Serving = false
		status.ServingReason = &reason
	}
	marker := status.GatewayState + reason
	if b.CredentialID != nil {
		marker += *b.CredentialID
	}
	s.mu.Lock()
	if marker != s.lastState {
		s.version++
		s.lastState = marker
		if s.Changed != nil {
			s.Changed()
		}
	}
	status.Version = s.version
	for id, state := range s.peerState {
		status.Peers = append(status.Peers, domain.CachePeer{NodeID: id, State: state})
	}
	s.mu.Unlock()
	sort.Slice(status.Peers, func(i, j int) bool { return status.Peers[i].NodeID < status.Peers[j].NodeID })
	return status, nil
}

func (s *Service) markPeer(node, state string, failed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.peerState) >= 256 && s.peerState[node] == "" {
		return
	}
	if s.peerState[node] != state {
		s.peerState[node] = state
		s.version++
	}
	if failed {
		s.peerFailureCount.Add(1)
		s.peerFailures[node] = min(16, s.peerFailures[node]+1)
	}
}

func (s *Service) Metrics() domain.CacheMetrics {
	return domain.CacheMetrics{Lookups: s.lookupCount.Load(), LookupNanoseconds: s.lookupNanoseconds.Load(), PeerFailures: s.peerFailureCount.Load(), Failures: s.failures.Load(), Cancellations: s.cancellations.Load(), Active: s.active.Load(), BytesExported: s.bytesExported.Load(), BytesReceived: s.bytesReceived.Load()}
}
func (s *Service) Operations() []service.Operation {
	run := func(ctx context.Context, e *service.Execution) (any, error) {
		s.bindingMu.Lock()
		defer s.bindingMu.Unlock()
		if e.Command.TargetID != s.Identity().NodeID || !s.Full(ctx, e.Command.TargetID) {
			return nil, domain.Err("MEMBERSHIP_INELIGIBLE", "Cache settings require the selected full member.", 403)
		}
		if s.Config.SigningCredentialRef != nil {
			return nil, domain.Err("DECLARED_RESOURCE", "Nix-declared cache signing selection is read-only.", 409)
		}
		var p struct {
			ID string `json:"credential_id"`
		}
		_ = json.Unmarshal(e.Command.Payload, &p)
		var b binding
		rev, err := s.State.Get(ctx, "cache_binding."+e.Command.TargetID, &b)
		if err != nil && !missing(err) {
			return nil, err
		}
		if missing(err) {
			b = binding{Version: 1, Commands: map[string]bool{}}
		}
		if b.Commands[e.Command.ID] {
			if err := s.Vault.PruneBinding(ctx, b.CredentialID, "cache", e.Command.TargetID); err != nil {
				return nil, err
			}
			return s.Status(ctx)
		}
		if e.Command.ExpectedVersions["settings:cache:"+e.Command.TargetID] != b.Version {
			return nil, domain.Err("VERSION_CONFLICT", "Cache settings changed after review.", 409)
		}
		if err = s.Vault.Bind(ctx, p.ID, "cache", e.Command.TargetID); err != nil {
			return nil, err
		}
		if len(b.Commands) >= domain.MaxRecords {
			return nil, domain.Err("STORAGE_LIMIT", "Cache settings receipt bound reached.", 507)
		}
		b.CredentialID = &p.ID
		b.Version++
		if b.Commands == nil {
			b.Commands = map[string]bool{}
		}
		b.Commands[e.Command.ID] = true
		if _, err = s.State.CAS(ctx, "cache_binding."+e.Command.TargetID, b, rev); err != nil {
			return nil, err
		}
		if err = s.Vault.PruneBinding(ctx, b.CredentialID, "cache", e.Command.TargetID); err != nil {
			return nil, err
		}
		return s.Status(ctx)
	}
	return []service.Operation{{Kind: "cache.credential.set", Scope: "cluster", Timeout: 30 * time.Second, SafeRetry: true, Validate: func(c domain.Command) error {
		var p struct {
			ID string `json:"credential_id"`
		}
		if domain.StrictJSON(c.Payload, &p) != nil || !domain.SafeID.MatchString(p.ID) || len(c.ExpectedVersions) != 1 || c.ExpectedVersions["settings:cache:"+c.TargetID] < 1 {
			return domain.Err("INVALID_PAYLOAD", "Cache credential selection requires an ID and reviewed settings version.", 422)
		}
		return nil
	}, Run: run, Reconcile: run}}
}
