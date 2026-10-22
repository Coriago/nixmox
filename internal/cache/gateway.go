package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/nix-community/go-nix/pkg/narinfo"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"github.com/nix-community/go-nix/pkg/nixhash"
	"io"
	"net"
	"net/http"
	"nixmox/internal/domain"
	"nixmox/internal/network"
	"nixmox/internal/nixstore"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type hit struct {
	node, address string
	metadata      *narinfo.NarInfo
	err           error
}

func (s *Service) trustedKeys(ctx context.Context) ([]signature.PublicKey, error) {
	page, err := s.Vault.Credentials(ctx, "500", "")
	if err != nil {
		return nil, err
	}
	if page.NextCursor != nil {
		return nil, domain.Err("STORAGE_LIMIT", "Cache trust catalog exceeds its bound.", 503)
	}
	keys := []signature.PublicKey{}
	for _, credential := range page.Items {
		if credential.Kind != "nix-cache-signing-key" || credential.State != "available" || credential.PublicKey == nil {
			continue
		}
		trusted := false
		for _, node := range credential.Scope.CacheNodeIDs {
			if s.Full(ctx, node) {
				trusted = true
				break
			}
		}
		if !trusted {
			continue
		}
		key, err := signature.ParsePublicKey(*credential.PublicKey)
		if err != nil {
			return nil, domain.Err("TRUST_UNAVAILABLE", "Cluster signing trust is invalid.", 503)
		}
		keys = append(keys, key)
	}
	return keys, nil
}
func validateMetadata(data []byte, hash string, keys []signature.PublicKey) (*narinfo.NarInfo, error) {
	if len(data) > 256<<10 {
		return nil, domain.Err("CACHE_UNAVAILABLE", "Peer metadata exceeds its bound.", 503)
	}
	n, err := narinfo.Parse(bytes.NewReader(data))
	if err != nil || n.Check() != nil || !nixstore.PathPattern.MatchString(n.StorePath) || !strings.HasPrefix(filepath.Base(n.StorePath), hash+"-") || n.Compression != "none" || n.NarHash == nil || n.FileHash == nil || n.NarHash.Algo() != nixhash.SHA256 || n.FileHash.Algo() != nixhash.SHA256 || n.NarSize == 0 || n.NarSize > 1<<50 || n.FileSize != n.NarSize || !bytes.Equal(n.NarHash.Digest(), n.FileHash.Digest()) || len(n.References) > 20000 {
		return nil, domain.Err("CACHE_UNAVAILABLE", "Peer metadata does not describe a bounded uncompressed NAR.", 503)
	}
	expected := "nar/" + hash + "/" + hex.EncodeToString(n.NarHash.Digest()) + ".nar"
	if n.URL != expected {
		return nil, domain.Err("CACHE_UNAVAILABLE", "Peer archive URL does not bind the expected content identity.", 503)
	}
	for _, sig := range n.Signatures {
		for _, key := range keys {
			if key.Verify(n.Fingerprint(), sig) {
				return n, nil
			}
		}
	}
	return nil, domain.Err("UNTRUSTED_OUTPUT", "Peer output has no authorized Nix signature.", 503)
}
func (s *Service) peerLookup(ctx context.Context, host domain.Host, hash string, keys []signature.PublicKey) hit {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result := hit{node: host.ID, err: domain.Err("CACHE_UNAVAILABLE", "Peer cache is unavailable.", 503)}
	if s.Client == nil {
		return result
	}
	client, err := s.Client(ctx, host.ID)
	if err != nil {
		return result
	}
	defer client.CloseIdleConnections()
	for _, address := range network.MachineAddresses(host.Endpoints, s.Network) {
		req, err := http.NewRequestWithContext(ctx, "GET", "https://"+address+"/store-cache/"+hash+".narinfo", nil)
		if err != nil {
			return result
		}
		response, err := client.Do(req)
		if err != nil {
			continue
		}
		if response.StatusCode == 404 {
			response.Body.Close()
			result.err = domain.Err("NOT_FOUND", "Peer does not retain this output.", 404)
			return result
		}
		if response.StatusCode != 200 {
			response.Body.Close()
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, (256<<10)+1))
		response.Body.Close()
		if readErr != nil {
			continue
		}
		metadata, err := validateMetadata(data, hash, keys)
		if err != nil {
			continue
		}
		result.address = address
		result.metadata = metadata
		result.err = nil
		return result
	}
	return result
}
func (s *Service) lookupAll(ctx context.Context, hash string) ([]hit, error) {
	start := time.Now()
	defer func() { s.lookupCount.Add(1); s.lookupNanoseconds.Add(uint64(time.Since(start))) }()
	if err := take(ctx, s.lookups); err != nil {
		return nil, err
	}
	defer func() { <-s.lookups }()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	keys, err := s.trustedKeys(ctx)
	if err != nil {
		return nil, err
	}
	page, err := s.Hosts.Hosts(ctx, "500", "")
	if err != nil {
		return nil, err
	}
	if page.NextCursor != nil || len(page.Items) > 256 {
		return nil, domain.Err("CACHE_UNAVAILABLE", "Peer lookup exceeds its membership bound.", 503)
	}
	jobs := make(chan domain.Host, len(page.Items))
	results := make(chan hit, len(page.Items))
	count := 0
	s.mu.Lock()
	sort.SliceStable(page.Items, func(i, j int) bool { return s.peerFailures[page.Items[i].ID] < s.peerFailures[page.Items[j].ID] })
	s.mu.Unlock()
	for _, host := range page.Items {
		if host.ID != s.Identity().NodeID && host.Membership == "full" {
			jobs <- host
			count++
		}
	}
	close(jobs)
	var workers sync.WaitGroup
	for i := 0; i < min(4, count); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for host := range jobs {
				results <- s.peerLookup(ctx, host, hash, keys)
			}
		}()
	}
	workers.Wait()
	close(results)
	hits := []hit{}
	for result := range results {
		peerState := "reachable"
		failed := result.err != nil && !missing(result.err)
		if failed {
			peerState = "unavailable"
		}
		s.markPeer(result.node, peerState, failed)
		hits = append(hits, result)
	}
	s.mu.Lock()
	sort.Slice(hits, func(i, j int) bool {
		if s.peerFailures[hits[i].node] != s.peerFailures[hits[j].node] {
			return s.peerFailures[hits[i].node] < s.peerFailures[hits[j].node]
		}
		return hits[i].node < hits[j].node
	})
	s.mu.Unlock()
	return hits, nil
}
func (s *Service) lookup(ctx context.Context, hash string) ([]hit, error) {
	results, err := s.lookupAll(ctx, hash)
	if err != nil {
		return nil, err
	}
	hits := []hit{}
	unavailable := false
	for _, result := range results {
		if result.err == nil {
			hits = append(hits, result)
		} else if !missing(result.err) {
			unavailable = true
		}
	}
	if len(hits) > 0 {
		return hits, nil
	}
	if unavailable {
		return nil, domain.Err("CACHE_UNAVAILABLE", "Eligible peers are unavailable; absence is not a verified cache miss.", 503)
	}
	return nil, domain.Err("NOT_FOUND", "No eligible peer currently retains the output.", 404)
}
func (s *Service) ServeLocal(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		cacheError(w, domain.Err("FORBIDDEN", "The local gateway accepts loopback connections only.", 403))
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		cacheError(w, domain.Err("METHOD_NOT_ALLOWED", "Read-only gateway.", 405))
		return
	}
	if !s.Config.Enable {
		cacheError(w, domain.Err("CACHE_UNAVAILABLE", "Local gateway is disabled.", 503))
		return
	}
	if r.URL.RawQuery != "" {
		cacheError(w, domain.Err("INVALID_QUERY", "Cache routes accept no query parameters.", 400))
		return
	}
	kind, hash, archive := route(strings.TrimPrefix(r.URL.Path, "/cache/"))
	if kind == "invalid" {
		cacheError(w, domain.Err("NOT_FOUND", "Cache route not found.", 404))
		return
	}
	if kind == "info" {
		writeBytes(w, r, "text/x-nix-cache-info", []byte("StoreDir: /nix/store\nWantMassQuery: 1\nPriority: 40\n"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			s.cancellations.Add(1)
		}
	}()
	if err = take(ctx, s.pulls); err != nil {
		cacheError(w, err)
		return
	}
	defer func() { <-s.pulls }()
	s.active.Add(1)
	defer s.active.Add(-1)
	hits, err := s.lookup(ctx, hash)
	if err != nil {
		cacheError(w, err)
		return
	}
	if kind == "narinfo" {
		writeBytes(w, r, hits[0].metadata.ContentType(), []byte(hits[0].metadata.String()))
		return
	}
	var expected *narinfo.NarInfo
	for _, hit := range hits {
		if hex.EncodeToString(hit.metadata.NarHash.Digest()) == archive {
			expected = hit.metadata
			break
		}
	}
	if expected == nil {
		cacheError(w, domain.Err("NOT_FOUND", "No peer retains the requested archive identity.", 404))
		return
	}
	if r.Method == "HEAD" {
		w.Header().Set("Content-Type", "application/x-nix-nar")
		w.Header().Set("Content-Length", strconv.FormatUint(expected.NarSize, 10))
		return
	}
	for attempt, hit := range hits {
		if attempt >= 3 {
			break
		}
		n := hit.metadata
		if n.NarSize != expected.NarSize || n.FileSize != expected.FileSize || n.Compression != expected.Compression || !bytes.Equal(n.NarHash.Digest(), expected.NarHash.Digest()) || !bytes.Equal(n.FileHash.Digest(), expected.FileHash.Digest()) {
			continue
		}
		client, err := s.Client(ctx, hit.node)
		if err != nil {
			continue
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", "https://"+hit.address+"/store-cache/"+n.URL, nil)
		response, err := client.Do(req)
		if err != nil {
			s.markPeer(hit.node, "unavailable", true)
			client.CloseIdleConnections()
			continue
		}
		if response.StatusCode != 200 || response.ContentLength != int64(n.NarSize) || response.Header.Get("Content-Encoding") != "" {
			s.markPeer(hit.node, "unavailable", true)
			response.Body.Close()
			client.CloseIdleConnections()
			continue
		}
		// Buffer only the first chunk. Failure before response bytes are sent can
		// choose another peer; failure afterwards aborts this complete response.
		buffer := make([]byte, 64<<10)
		read, readErr := response.Body.Read(buffer)
		if read == 0 && (readErr != nil) {
			s.markPeer(hit.node, "unavailable", true)
			response.Body.Close()
			client.CloseIdleConnections()
			continue
		}
		w.Header().Set("Content-Type", "application/x-nix-nar")
		w.Header().Set("Content-Length", strconv.FormatUint(n.NarSize, 10))
		w.Header().Set("X-Nixmox-Source-Node", hit.node)
		hashWriter := sha256.New()
		writer := &streamWriter{ctx: ctx, writer: io.MultiWriter(w, hashWriter), response: w, counter: &s.bytesReceived, remaining: n.NarSize}
		_, writeErr := writer.Write(buffer[:read])
		if writeErr == nil && readErr == nil {
			_, writeErr = io.CopyBuffer(writer, response.Body, buffer)
		} else if readErr != nil && !errors.Is(readErr, io.EOF) {
			writeErr = readErr
		}
		response.Body.Close()
		client.CloseIdleConnections()
		if writeErr != nil || writer.remaining != 0 || !bytes.Equal(hashWriter.Sum(nil), n.NarHash.Digest()) {
			s.failures.Add(1)
			s.markPeer(hit.node, "unavailable", true)
			panic(http.ErrAbortHandler)
		}
		s.mu.Lock()
		delete(s.peerFailures, hit.node)
		s.mu.Unlock()
		return
	}
	s.failures.Add(1)
	cacheError(w, domain.Err("ARTIFACT_UNAVAILABLE", "Exact archive transfer exhausted its eligible peers.", 503))
}
func (s *Service) Locations(ctx context.Context, path string) ([]domain.CacheLocation, error) {
	if !nixstore.PathPattern.MatchString(path) {
		return nil, domain.Err("INVALID_PATH", "Locate requires a full store output path.", 422)
	}
	hits, err := s.lookupAll(ctx, filepath.Base(path)[:32])
	if err != nil {
		return nil, err
	}
	rows := []domain.CacheLocation{}
	local := domain.CacheLocation{NodeID: s.Identity().NodeID, State: "unavailable", OutputPath: path}
	lease, localErr := s.Daemon.Lease(ctx, filepath.Base(path)[:32])
	if localErr == nil {
		if lease.Metadata.Path == path {
			hash, _ := nixhash.NewHashWithEncoding(nixhash.SHA256, lease.Metadata.NarHash, nixhash.SRI, true)
			value, size := hash.String(), lease.Metadata.NarSize
			local.State, local.NarHash, local.NarSize = "available", &value, &size
		} else {
			local.State = "missing"
		}
		lease.Close()
	} else if missing(localErr) {
		local.State = "missing"
	}
	rows = append(rows, local)
	for _, hit := range hits {
		if hit.err != nil {
			state := "unavailable"
			if missing(hit.err) {
				state = "missing"
			}
			rows = append(rows, domain.CacheLocation{NodeID: hit.node, State: state, OutputPath: path})
			continue
		}
		if hit.metadata.StorePath != path {
			continue
		}
		hash := hit.metadata.NarHash.Format(nixhash.SRI, true)
		size := hit.metadata.NarSize
		rows = append(rows, domain.CacheLocation{NodeID: hit.node, State: "available", OutputPath: path, NarHash: &hash, NarSize: &size})
	}
	return rows, nil
}
