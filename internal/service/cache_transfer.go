package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/nix-community/go-nix/pkg/narinfo"
	"github.com/nix-community/go-nix/pkg/nixhash"
	"github.com/nix-community/go-nix/pkg/storepath"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"nixmox/internal/domain"
	"nixmox/internal/engine"
	nixengine "nixmox/internal/engine/nix"
)

func cacheUnavailable() error {
	return &domain.Error{Code: "CACHE_UNAVAILABLE", Message: "Cache unavailable; another substituter or local build may be used", Retryable: true}
}
func (c *Cache) acquireExport() (func(), error) {
	select {
	case c.exports <- struct{}{}:
		return sync.OnceFunc(func() { <-c.exports }), nil
	default:
		return nil, cacheUnavailable()
	}
}
func (c *Cache) localInfo(ctx context.Context, hash string) (*narinfo.NarInfo, error) {
	key, reason := c.signingKey()
	if reason != "" {
		return nil, cacheUnavailable()
	}
	path, err := c.index.Lookup(ctx, hash)
	if errors.Is(err, os.ErrNotExist) {
		return nil, notFound()
	}
	if err != nil {
		return nil, cacheUnavailable()
	}
	output, err := c.store.Lookup(ctx, path)
	if err != nil {
		return nil, cacheUnavailable()
	}
	info, err := nixengine.CacheInfo(output, key)
	if err != nil {
		return nil, cacheUnavailable()
	}
	return info, nil
}
func (c *Cache) endpoints(peer domain.CachePeer) []string {
	endpoints := slices.Clone(peer.Endpoints)
	preferences := c.cluster.config.Network.PreferredInterfaces
	rank := func(e domain.Endpoint) int {
		p := slices.Index(preferences, e.Interface)
		if p < 0 {
			return len(preferences)
		}
		return p
	}
	slices.SortStableFunc(endpoints, func(a, b domain.Endpoint) int { return rank(a) - rank(b) })
	urls := []string{}
	for _, endpoint := range endpoints {
		if endpoint.Service != "api" || endpoint.Transport != "http" || !endpoint.Verified {
			continue
		}
		host, port, err := net.SplitHostPort(endpoint.Address)
		ip := net.ParseIP(host)
		// Advertised management IPs only: no DNS rebinding, credentials, proxies or
		// caller-controlled destinations. Loopback would address the wrong machine.
		if err != nil || ip == nil || !ip.IsGlobalUnicast() || ip.IsLoopback() || port == "" {
			continue
		}
		if len(preferences) > 0 && !c.cluster.config.Network.AllowFallback && endpoint.Interface != preferences[0] {
			continue
		}
		urls = append(urls, "http://"+net.JoinHostPort(ip.String(), port)+"/store-cache/")
	}
	return urls
}

type cacheSource struct {
	info *narinfo.NarInfo
	base string
	node domain.ID
}

func (c *Cache) sources(ctx context.Context, hash string) ([]cacheSource, error) {
	if !nixengine.CacheHash(hash, 32) {
		return nil, invalid("Invalid store hash")
	}
	if !c.cluster.config.Cache.Enable {
		return nil, cacheUnavailable()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	started := time.Now()
	defer func() { c.latency.Record(ctx, time.Since(started).Seconds()) }()
	peers, err := c.peersSnapshot()
	result := []cacheSource{}
	unavailable := err != nil || len(peers) == 0
	for _, peer := range peers {
		if !peer.Available || peer.PublicKey == nil || !nixengine.ValidCacheKey(*peer.PublicKey) || peer.ObservedAt == nil || time.Since(*peer.ObservedAt) > domain.HeartbeatOffline {
			unavailable = true
			continue
		}
		endpoints := c.endpoints(peer)
		if len(endpoints) == 0 {
			unavailable = true
		}
		for _, base := range endpoints {
			info, err := c.http.Info(ctx, base, hash, *peer.PublicKey)
			if err == nil {
				result = append(result, cacheSource{info, base, peer.NodeID})
				break
			}
			var detail *domain.Error
			if !errors.As(err, &detail) || detail.Code != "NOT_FOUND" {
				unavailable = true
			}
			if ctx.Err() != nil {
				break
			}
		}
	}
	if len(result) > 0 {
		return result, nil
	}
	if unavailable {
		c.failures.Add(ctx, 1)
		return nil, cacheUnavailable()
	}
	return nil, notFound()
}
func (c *Cache) Info(ctx context.Context, hash string, serve bool) (*narinfo.NarInfo, error) {
	if !nixengine.CacheHash(hash, 32) {
		return nil, invalid("Invalid store hash")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if serve {
		release, err := c.acquireExport()
		if err != nil {
			return nil, err
		}
		defer release()
		return c.localInfo(ctx, hash)
	}
	sources, err := c.sources(ctx, hash)
	if err != nil {
		return nil, err
	}
	info := *sources[0].info
	info.URL = nixengine.ArchivePath(&info)
	return &info, nil
}

// Archive prepares validity/pins or peer response headers before HTTP commits
// its body. Once WriteTo starts, errors abort the response, never splice peers.
func (c *Cache) Archive(ctx context.Context, hash, narHash string, serve, head bool) (*narinfo.NarInfo, engine.StoreExport, error) {
	if !nixengine.CacheHash(hash, 32) || !nixengine.CacheHash(narHash, 52) {
		return nil, nil, invalid("Invalid archive identity")
	}
	if serve {
		return c.localArchive(ctx, hash, narHash, head)
	}
	sources, err := c.sources(ctx, hash)
	if err != nil {
		return nil, nil, err
	}
	var expected *narinfo.NarInfo
	attempts := 0
	for _, source := range sources {
		info := source.info
		if info.NarHash.Format(nixhash.NixBase32, false) != narHash {
			continue
		}
		if expected == nil {
			expected = info
		} else if info.Fingerprint() != expected.Fingerprint() {
			continue
		}
		if attempts == 4 {
			break
		}
		attempts++
		stream, err := c.http.Open(ctx, source.base, info, head)
		if err != nil {
			continue
		}
		if head {
			return info, nil, nil
		}
		return info, &cacheStream{StoreExport: stream, cache: c, ctx: ctx, release: func() {}, direction: "pull", size: info.FileSize, digest: info.NarHash.Digest()}, nil
	}
	return nil, nil, cacheUnavailable()
}

func (c *Cache) localArchive(ctx context.Context, hash, narHash string, head bool) (*narinfo.NarInfo, engine.StoreExport, error) {
	release, err := c.acquireExport()
	if err != nil {
		return nil, nil, err
	}
	prepareCtx, cancel := context.WithCancel(ctx)
	timer := time.AfterFunc(5*time.Second, cancel)
	releaseSlot := release
	release = func() { timer.Stop(); cancel(); releaseSlot() }
	info, err := c.localInfo(prepareCtx, hash)
	if err == nil && info.NarHash.Format(nixhash.NixBase32, false) != narHash {
		err = notFound()
	}
	if err != nil {
		release()
		return nil, nil, err
	}
	if head {
		release()
		return info, nil, nil
	}
	output := domain.StoreOutput{StorePath: info.StorePath, NARHash: domain.Digest(info.NarHash.String()), NARSize: info.NarSize}
	stream, err := c.store.Open(prepareCtx, output)
	if err == nil && !timer.Stop() {
		stream.Close()
		err = context.DeadlineExceeded
	}
	if err != nil {
		release()
		return nil, nil, cacheUnavailable()
	}
	return info, &cacheStream{StoreExport: stream, cache: c, ctx: ctx, release: release, direction: "export", size: info.FileSize, digest: info.NarHash.Digest()}, nil
}

type cacheStream struct {
	engine.StoreExport
	cache     *Cache
	ctx       context.Context
	release   func()
	direction string
	size      uint64
	digest    []byte
	once      sync.Once
}

func (s *cacheStream) Close() error {
	var err error
	s.once.Do(func() { err = s.StoreExport.Close(); s.release() })
	return err
}
func (s *cacheStream) WriteTo(w io.Writer) (int64, error) {
	hash := sha256.New()
	n, err := s.StoreExport.WriteTo(io.MultiWriter(w, hash))
	if err == nil && (uint64(n) != s.size || !bytes.Equal(hash.Sum(nil), s.digest)) {
		err = io.ErrUnexpectedEOF
	}
	attrs := metric.WithAttributes(attribute.String("direction", s.direction))
	s.cache.bytes.Add(s.ctx, n, attrs)
	if err != nil {
		s.cache.failures.Add(s.ctx, 1, attrs)
	}
	return n, err
}
func (c *Cache) Locate(ctx context.Context, path string) ([]domain.CacheLocation, error) {
	if !storepath.PathRe.MatchString(path) {
		return nil, invalid("A canonical store path is required")
	}
	sources, err := c.sources(ctx, filepath.Base(path)[:32])
	if err != nil {
		return nil, err
	}
	items := []domain.CacheLocation{}
	for _, source := range sources {
		info := source.info
		if info.StorePath != path {
			continue
		}
		signatures := []string{}
		for _, sig := range info.Signatures {
			signatures = append(signatures, sig.String())
		}
		items = append(items, domain.CacheLocation{NodeID: source.node, URL: source.base + info.URL, StorePath: info.StorePath, NARHash: domain.Digest(info.NarHash.String()), FileHash: domain.Digest(info.FileHash.String()), NARSize: info.NarSize, FileSize: info.FileSize, Compression: info.Compression, Signatures: signatures})
	}
	if len(items) == 0 {
		return nil, notFound()
	}
	return items, nil
}
