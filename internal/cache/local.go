// Package cache serves Nix artifacts using go-nix's formats and signing APIs.
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/nix-community/go-nix/pkg/nar"
	"github.com/nix-community/go-nix/pkg/narinfo"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"github.com/nix-community/go-nix/pkg/nixbase32"
	"github.com/nix-community/go-nix/pkg/nixhash"
	"github.com/nix-community/go-nix/pkg/storepath"
	"nixmox/internal/host"
)

type Local struct {
	key     signature.SecretKey
	process host.Process
}

func NewLocal(secret string, process host.Process) (*Local, error) {
	key, err := signature.LoadSecretKey(secret)
	if err != nil {
		return nil, err
	}
	return &Local{key: key, process: process}, nil
}
func (l *Local) PublicKey() string { return l.key.ToPublicKey().String() }

// Metadata queries registered store information, never infers closure references
// from directory contents. The Nix daemon remains authoritative for validity.
func (l *Local) Metadata(ctx context.Context, path string) (*narinfo.NarInfo, error) {
	if !storepath.PathRe.MatchString(path) {
		return nil, os.ErrNotExist
	}
	data, err := l.process.Run(ctx, "", nil, "nix", "path-info", "--json", "--json-format", "1", path)
	if err != nil {
		return nil, err
	}
	var paths map[string]struct {
		NarHash    string   `json:"narHash"`
		NarSize    uint64   `json:"narSize"`
		References []string `json:"references"`
		Deriver    string   `json:"deriver"`
	}
	if err = json.Unmarshal(data, &paths); err != nil {
		return nil, err
	}
	info, ok := paths[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	hash, err := nixhash.ParseAny(info.NarHash, nil)
	if err != nil {
		return nil, err
	}
	refs := make([]string, 0, len(info.References))
	for _, ref := range info.References {
		if !storepath.PathRe.MatchString(ref) {
			return nil, errors.New("invalid store reference")
		}
		refs = append(refs, filepath.Base(ref))
	}
	sort.Strings(refs)
	result := &narinfo.NarInfo{StorePath: path, URL: "nar/" + filepath.Base(path) + ".nar", Compression: "none", FileHash: hash, FileSize: info.NarSize, NarHash: hash, NarSize: info.NarSize, References: refs}
	if info.Deriver != "" && info.Deriver != "unknown-deriver" {
		if !storepath.PathRe.MatchString(info.Deriver) {
			return nil, errors.New("invalid deriver path")
		}
		result.Deriver = filepath.Base(info.Deriver)
	}
	if err = result.Check(); err != nil {
		return nil, err
	}
	signed, err := l.key.Sign(nil, result.Fingerprint())
	if err != nil {
		return nil, err
	}
	result.Signatures = []signature.Signature{signed}
	return result, nil
}

// ServeHTTP expects the /store-cache prefix to have been stripped by the router.
func (l *Local) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	if name == "nix-cache-info" {
		w.Header().Set("Content-Type", "text/x-nix-cache-info")
		if r.Method == http.MethodGet {
			io.WriteString(w, "StoreDir: /nix/store\nWantMassQuery: 1\nPriority: 40\n")
		}
		return
	}
	var path string
	isNar := strings.HasPrefix(name, "nar/") && strings.HasSuffix(name, ".nar")
	if isNar {
		path = storepath.StoreDir + "/" + strings.TrimSuffix(strings.TrimPrefix(name, "nar/"), ".nar")
		if !storepath.PathRe.MatchString(path) {
			http.NotFound(w, r)
			return
		}
	} else if strings.HasSuffix(name, ".narinfo") {
		digest := strings.TrimSuffix(name, ".narinfo")
		if len(digest) != nixbase32.EncodedLen(storepath.PathHashSize) || nixbase32.ValidateString(digest) != nil {
			http.NotFound(w, r)
			return
		}
		matches, err := filepath.Glob(storepath.StoreDir + "/" + digest + "-*")
		if err != nil || len(matches) != 1 {
			http.NotFound(w, r)
			return
		}
		path = matches[0]
	} else {
		http.NotFound(w, r)
		return
	}
	if _, err := os.Lstat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	info, err := l.Metadata(r.Context(), path)
	if err != nil {
		http.Error(w, "store metadata unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	if !isNar {
		body := info.String()
		w.Header().Set("Content-Type", info.ContentType())
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if r.Method == http.MethodGet {
			io.WriteString(w, body)
		}
		return
	}
	w.Header().Set("Content-Type", "application/x-nix-nar")
	w.Header().Set("Content-Length", strconv.FormatUint(info.NarSize, 10))
	if r.Method == http.MethodHead {
		return
	}
	// A partial artifact cannot be turned into an HTTP error or replayed in-place.
	// Abort the response; Nix verifies the signed NAR size and hash on download.
	if err = nar.DumpPath(contextWriter{ctx: r.Context(), writer: w}, path); err != nil {
		panic(http.ErrAbortHandler)
	}
}

type contextWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (w contextWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.writer.Write(data)
}
