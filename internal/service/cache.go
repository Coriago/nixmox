package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	"nixmox/internal/domain"
	"nixmox/internal/engine"
	nixengine "nixmox/internal/engine/nix"
)

// Cache holds bounded metadata and streams only. Archives are never staged here.
type Cache struct {
	cluster         *ClusterNode
	store           engine.StoreCache
	index           *nixengine.StoreIndex
	http            *nixengine.CacheHTTP
	exports         chan struct{}
	mu              sync.RWMutex
	peers           []domain.CachePeer
	peerError       error
	peerScope       string
	version         domain.Version
	snapshot        string
	bytes, failures metric.Int64Counter
	latency         metric.Float64Histogram
}

func newCache(c *ClusterNode) (*Cache, error) {
	n := c.config.Cache.MaxConcurrentTransfers

	cacheNix := c.nix
	cacheNix.PinDirectory = filepath.Join(c.config.DataDir, "cache-roots")
	// Construction precedes HTTP serving, under the application data-directory
	// lock. Only orphaned request pins live here; generation/task pins are separate.
	if err := os.RemoveAll(cacheNix.PinDirectory); err != nil {
		return nil, err
	}
	cache := &Cache{cluster: c, store: nixengine.NARExporter{Nix: cacheNix}, index: &nixengine.StoreIndex{}, exports: make(chan struct{}, n), peers: []domain.CachePeer{}, peerError: errors.New("peer discovery pending"), http: nixengine.NewCacheHTTP(n)}
	meter := otel.Meter("nixmox/cache")
	cache.bytes, _ = meter.Int64Counter("nixmox.cache.bytes")
	cache.failures, _ = meter.Int64Counter("nixmox.cache.failures")
	cache.latency, _ = meter.Float64Histogram("nixmox.cache.lookup.duration", metric.WithUnit("s"))
	return cache, nil
}
func (c *Cache) signingKey() (signature.SecretKey, string) {
	config := c.cluster.config
	if !config.Cache.Serve {
		return signature.SecretKey{}, "SERVING_DISABLED"
	}
	c.cluster.mu.Lock()
	eligible := c.cluster.keys.Trust != nil && (c.cluster.creator() || c.cluster.forced())
	c.cluster.mu.Unlock()
	if !eligible {
		return signature.SecretKey{}, "MEMBERSHIP_INELIGIBLE"
	}
	ref := config.Cache.SigningCredentialRef
	if ref == nil {
		return signature.SecretKey{}, "SIGNING_CREDENTIAL_REQUIRED"
	}
	credential, ok := config.Credentials[*ref]
	if !ok || credential.Kind != "nix-cache-signing-key" {
		return signature.SecretKey{}, "SIGNING_CREDENTIAL_UNAVAILABLE"
	}
	key, err := nixengine.LoadCacheKey(credential.SecretFile)
	if err != nil {
		return signature.SecretKey{}, "SIGNING_CREDENTIAL_UNAVAILABLE"
	}
	return key, ""
}
func (c *Cache) Advertisement() *domain.CachePeer {
	key, reason := c.signingKey()
	c.cluster.mu.Lock()
	host := c.cluster.host
	c.cluster.mu.Unlock()
	now := time.Now().UTC()
	peer := &domain.CachePeer{NodeID: host.ID, Endpoints: host.Endpoints, Available: reason == "", ObservedAt: &now}
	if reason == "" {
		value := key.ToPublicKey().String()
		peer.PublicKey = &value
	}
	return peer
}
func (c *Cache) Status(context.Context) domain.CacheStatus {
	_, reason := c.signingKey()
	peers, peerError := c.peersSnapshot()
	c.mu.Lock()
	defer c.mu.Unlock()
	state := "ready"
	if !c.cluster.config.Cache.Enable {
		state = "disabled"
	} else if peerError != nil {
		state = "unavailable"
	}
	active := len(c.exports) + c.http.Active()
	binding, _ := json.Marshal([]any{state, reason, peers, active})
	if string(binding) != c.snapshot {
		c.version++
		c.snapshot = string(binding)
	}
	result := domain.CacheStatus{NodeID: c.cluster.host.ID, Version: c.version, ObservedAt: time.Now().UTC(), LocalURL: c.localURL(), GatewayState: state, Serving: reason == "", ActiveTransfers: active, Peers: peers}
	if reason != "" {
		result.ServingReason = &reason
	}
	return result
}
func (c *Cache) membership() string {
	c.cluster.mu.Lock()
	defer c.cluster.mu.Unlock()
	trust := c.cluster.keys.Trust
	if trust == nil {
		return ""
	}
	return fmt.Sprintf("%s:%d", trust.Cluster.ID, trust.Issuance.MembershipEpoch)
}
func (c *Cache) peersSnapshot() ([]domain.CachePeer, error) {
	scope := c.membership()
	c.mu.RLock()
	defer c.mu.RUnlock()
	if scope == "" || scope != c.peerScope {
		return []domain.CachePeer{}, cacheUnavailable()
	}
	return slices.Clone(c.peers), c.peerError
}
func (c *Cache) Run(ctx context.Context) error {
	defer c.http.Close()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		scope := c.membership()
		peers, err := c.cluster.cachePeers(ctx)
		if scope != c.membership() {
			peers = nil
			err = cacheUnavailable()
		}
		if peers == nil {
			peers = []domain.CachePeer{}
		}
		c.mu.Lock()
		c.peers, c.peerError, c.peerScope = peers, err, scope
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func (c *ClusterNode) cachePeers(ctx context.Context) ([]domain.CachePeer, error) {
	c.mu.Lock()
	if c.keys.Trust == nil {
		c.mu.Unlock()
		return nil, domain.Unavailable("cluster cache peers")
	}
	issuer := c.keys.Trust.Cluster.IssuerNodeID
	c.mu.Unlock()
	hosts := c.Hosts()
	var incomplete error
	if c.leaf() {
		data, err := c.call(ctx, "", "read", ReadRequest{Collection: "hosts", Page: domain.PageRequest{Limit: domain.PageLimit}}, true)
		if err != nil {
			return nil, err
		}
		var page domain.Page[domain.Host]
		if err = json.Unmarshal(data, &page); err != nil {
			return nil, err
		}
		hosts = page.Items
		if page.NextCursor != nil {
			incomplete = cacheUnavailable()
		}
	}
	peers := []domain.CachePeer{}
	for _, host := range hosts {
		if host.ID == c.host.ID || host.Membership == domain.Removed || (host.Membership != domain.Full && host.ID != issuer) || host.Cache == nil {
			continue
		}
		peer := *host.Cache
		peer.NodeID = host.ID
		peer.Endpoints = host.Endpoints
		peer.Available = peer.Available && host.Reachability == domain.Online && host.LastSeen != nil && time.Since(*host.LastSeen) < domain.HeartbeatOffline
		if len(peers) == 64 {
			incomplete = cacheUnavailable()
			break
		}
		peers = append(peers, peer)
	}
	slices.SortFunc(peers, func(a, b domain.CachePeer) int { return strings.Compare(string(a.NodeID), string(b.NodeID)) })
	return peers, incomplete
}
func (c *Cache) localURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/cache/", c.cluster.config.HTTP.Port)
}

// These options apply only to Nixmox's own commands, never host Nix configuration.
func (c *Cache) nixOptions() map[string]string {
	if !c.cluster.config.Cache.Enable {
		return nil
	}
	peers, _ := c.peersSnapshot()
	keys := []string{}
	for _, peer := range peers {
		if peer.PublicKey != nil && nixengine.ValidCacheKey(*peer.PublicKey) {
			keys = append(keys, *peer.PublicKey)
		}
	}
	slices.Sort(keys)
	keys = slices.Compact(keys)
	return map[string]string{
		"extra-substituters":        c.localURL(),
		"extra-trusted-public-keys": strings.Join(keys, " "),
		"fallback":                  "true",
	}
}
