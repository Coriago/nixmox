package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/nix-community/go-nix/pkg/nar"
	"github.com/nix-community/go-nix/pkg/narinfo"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"github.com/nix-community/go-nix/pkg/nixhash"
	"io"
	"net/http"
	"nixmox/internal/domain"
	"nixmox/internal/nixstore"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

var archivePattern = regexp.MustCompile(`^nar/([0-9abcdfghijklmnpqrsvwxyz]{32})/([0-9a-f]{64})\.nar$`)

func route(path string) (kind, storeHash, archiveHash string) {
	if path == "nix-cache-info" {
		return "info", "", ""
	}
	if strings.HasSuffix(path, ".narinfo") && nixstore.StoreHashPattern.MatchString(strings.TrimSuffix(path, ".narinfo")) {
		return "narinfo", strings.TrimSuffix(path, ".narinfo"), ""
	}
	if match := archivePattern.FindStringSubmatch(path); match != nil {
		return "nar", match[1], match[2]
	}
	return "invalid", "", ""
}
func cacheError(w http.ResponseWriter, err error) {
	var d *domain.Error
	code := http.StatusServiceUnavailable
	if errors.As(err, &d) {
		switch d.Status {
		case 400, 403, 404, 405, 422, 429:
			code = d.Status
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, http.StatusText(code), code)
}
func take(ctx context.Context, slots chan struct{}) error {
	select {
	case slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		return domain.Err("CACHE_BUSY", "Cache concurrency limit reached.", 503)
	}
}
func infoFromMetadata(m nixstore.StoreMetadata, key signature.SecretKey) (*narinfo.NarInfo, error) {
	hash, err := nixhash.NewHashWithEncoding(nixhash.SHA256, m.NarHash, nixhash.NixBase32, true)
	if err != nil {
		return nil, domain.Err("OUTPUT_INVALID", "Invalid store NAR identity.", 409)
	}
	storeHash := filepath.Base(m.Path)[:32]
	n := &narinfo.NarInfo{StorePath: m.Path, URL: "nar/" + storeHash + "/" + hex.EncodeToString(m.NarHash) + ".nar", Compression: "none", NarHash: hash, FileHash: hash, NarSize: m.NarSize, FileSize: m.NarSize, CA: m.CA, References: []string{}, Signatures: []signature.Signature{}}
	if m.Deriver != "" {
		n.Deriver = filepath.Base(m.Deriver)
	}
	for _, ref := range m.References {
		n.References = append(n.References, filepath.Base(ref))
	}
	sort.Strings(n.References)
	for _, text := range m.Signatures {
		sig, err := signature.ParseSignature(text)
		if err == nil {
			n.Signatures = append(n.Signatures, sig)
		}
	}
	signed, err := key.Sign(nil, n.Fingerprint())
	if err != nil {
		return nil, domain.Err("CREDENTIAL_UNAVAILABLE", "Cache signing failed.", 503)
	}
	n.Signatures = append(n.Signatures, signed)
	if n.Check() != nil || len(n.String()) > 256<<10 {
		return nil, domain.Err("OUTPUT_INVALID", "Cache metadata is invalid or exceeds its bound.", 503)
	}
	return n, nil
}

// ServePeer is mounted exclusively on the machine listener after certificate
// authorization. Pending callers also need an explicit closure grant per hash.
func (s *Service) ServePeer(w http.ResponseWriter, r *http.Request, node string, full bool) {
	if r.Method != "GET" && r.Method != "HEAD" {
		cacheError(w, domain.Err("METHOD_NOT_ALLOWED", "Read-only cache.", 405))
		return
	}
	if r.URL.RawQuery != "" {
		cacheError(w, domain.Err("INVALID_QUERY", "Cache routes accept no query parameters.", 400))
		return
	}
	kind, hash, archive := route(strings.TrimPrefix(r.URL.Path, "/store-cache/"))
	if kind == "invalid" {
		cacheError(w, domain.Err("NOT_FOUND", "Cache route not found.", 404))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			s.cancellations.Add(1)
		}
	}()
	if !full && (s.AuthorizePending == nil || !s.AuthorizePending(ctx, node, hash)) {
		cacheError(w, domain.Err("FORBIDDEN", "Pending nodes can read only task-authorized closure paths.", 403))
		return
	}
	key, reason, err := s.signer(ctx)
	if err != nil || reason != "" {
		cacheError(w, domain.Err("CACHE_UNAVAILABLE", "Store serving is offline.", 503))
		return
	}
	if kind == "info" {
		writeBytes(w, r, "text/x-nix-cache-info", []byte("StoreDir: /nix/store\nWantMassQuery: 1\nPriority: 40\n"))
		return
	}
	if err = take(ctx, s.exports); err != nil {
		cacheError(w, err)
		return
	}
	defer func() { <-s.exports }()
	s.active.Add(1)
	defer s.active.Add(-1)
	lease, err := s.Daemon.Lease(ctx, hash)
	if err != nil {
		cacheError(w, err)
		return
	}
	defer lease.Close()
	metadata, err := infoFromMetadata(lease.Metadata, key)
	if err != nil {
		cacheError(w, err)
		return
	}
	if kind == "narinfo" {
		writeBytes(w, r, metadata.ContentType(), []byte(metadata.String()))
		return
	}
	if archive != hex.EncodeToString(lease.Metadata.NarHash) {
		cacheError(w, domain.Err("NOT_FOUND", "Requested archive identity is no longer present.", 404))
		return
	}
	w.Header().Set("Content-Type", "application/x-nix-nar")
	w.Header().Set("Content-Length", strconv.FormatUint(metadata.NarSize, 10))
	w.Header().Set("Cache-Control", "private, max-age=0")
	if r.Method == "HEAD" {
		return
	}
	hashWriter := sha256.New()
	counter := &streamWriter{ctx: ctx, writer: io.MultiWriter(w, hashWriter), response: w, counter: &s.bytesExported, remaining: metadata.NarSize}
	if err = nar.DumpPath(counter, lease.Metadata.Path); err != nil || counter.remaining != 0 || !bytes.Equal(hashWriter.Sum(nil), lease.Metadata.NarHash) {
		s.failures.Add(1)
		panic(http.ErrAbortHandler)
	}
}
func writeBytes(w http.ResponseWriter, r *http.Request, contentType string, data []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	if r.Method != "HEAD" {
		_, _ = w.Write(data)
	}
}

type streamWriter struct {
	ctx       context.Context
	writer    io.Writer
	response  http.ResponseWriter
	counter   *atomic.Int64
	remaining uint64
}

func (w *streamWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if uint64(len(data)) > w.remaining {
		return 0, domain.Err("ARCHIVE_INVALID", "Archive exceeded its declared identity.", 409)
	}
	_ = http.NewResponseController(w.response).SetWriteDeadline(time.Now().Add(15 * time.Second))
	n, err := w.writer.Write(data)
	w.remaining -= uint64(n)
	w.counter.Add(int64(n))
	return n, err
}
