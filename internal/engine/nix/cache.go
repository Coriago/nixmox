package nix

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nix-community/go-nix/pkg/narinfo"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"github.com/nix-community/go-nix/pkg/nixbase32"
	"github.com/nix-community/go-nix/pkg/nixhash"
	"github.com/nix-community/go-nix/pkg/storepath"
	"nixmox/internal/domain"
)

// LoadCacheKey reads a bounded private key and verifies its public/private halves.
func LoadCacheKey(path string) (signature.SecretKey, error) {
	f, err := os.Open(path)
	if err != nil {
		return signature.SecretKey{}, errors.New("invalid cache signing key")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return signature.SecretKey{}, errors.New("invalid cache signing key")
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) > 4096 {
		return signature.SecretKey{}, errors.New("invalid cache signing key")
	}
	raw := strings.TrimSpace(string(data))
	if strings.ContainsAny(raw, "\r\n\t ") {
		return signature.SecretKey{}, errors.New("invalid cache signing key")
	}
	key, err := signature.LoadSecretKey(raw)
	if err != nil || !ValidCacheKey(key.ToPublicKey().String()) {
		return signature.SecretKey{}, errors.New("invalid cache signing key")
	}
	// Check seed/public-half consistency as well as syntactic key validity.
	proof, err := key.Sign(nil, "nixmox-cache-key-check")
	if err != nil || !key.ToPublicKey().Verify("nixmox-cache-key-check", proof) {
		return signature.SecretKey{}, errors.New("invalid cache signing key")
	}
	return key, nil
}

// ValidCacheKey also rejects names unsafe for public nix.conf settings.
func ValidCacheKey(raw string) bool {
	_, err := signature.ParsePublicKey(raw)
	if err != nil || len(raw) > 1024 || strings.ContainsAny(raw, "\r\n\t ") {
		return false
	}
	name, _, _ := strings.Cut(raw, ":")
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r)) {
			return false
		}
	}
	return true
}

// StoreIndex indexes names, not validity. Metadata is always rechecked with Nix.
// Directory changes trigger a refresh, with periodic reconciliation for missed
// timestamp changes. Requests never run whole-store path-info scans.
type StoreIndex struct {
	Directory string
	mu        sync.Mutex
	modified  time.Time
	checked   time.Time
	paths     map[string]string
}

func (s *StoreIndex) Lookup(ctx context.Context, hash string) (string, error) {
	if !CacheHash(hash, 32) {
		return "", errors.New("invalid store hash")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.Directory
	if dir == "" {
		dir = storepath.StoreDir
	}
	stat, err := os.Stat(dir)
	if err != nil {
		return "", errors.New("Nix store index unavailable")
	}
	if s.paths == nil || !stat.ModTime().Equal(s.modified) || time.Since(s.checked) > 30*time.Second {
		f, err := os.Open(dir)
		if err != nil {
			return "", err
		}
		defer f.Close()
		paths := map[string]string{}
		for {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			names, err := f.Readdirnames(1024)
			if err != nil && err != io.EOF {
				return "", err
			}
			for _, name := range names {
				if validStorePath(storepath.StoreDir + "/" + name) {
					hash := name[:32]
					if prior, exists := paths[hash]; exists && prior != filepath.Join(dir, name) {
						paths[hash] = ""
					} else if !exists {
						paths[hash] = filepath.Join(dir, name)
					}
				}
			}
			if err == io.EOF {
				break
			}
		}
		s.paths, s.modified, s.checked = paths, stat.ModTime(), time.Now()
	}
	path, ok := s.paths[hash]
	if !ok {
		return "", os.ErrNotExist
	}
	if path == "" {
		return "", errors.New("ambiguous store hash")
	}
	if _, err := os.Lstat(path); err != nil {
		return "", err
	}
	return path, nil
}
func CacheHash(value string, length int) bool {
	return len(value) == length && nixbase32.ValidateString(value) == nil
}
func ArchivePath(info *narinfo.NarInfo) string {
	return "nar/" + filepath.Base(info.StorePath)[:32] + "/" + info.NarHash.Format(nixhash.NixBase32, false) + ".nar"
}

// CacheInfo preserves existing signatures; only the selected holder signs anew.
func CacheInfo(output domain.StoreOutput, key signature.SecretKey) (*narinfo.NarInfo, error) {
	hash, err := nixhash.ParseAny(string(output.NARHash), nil)
	if err != nil {
		return nil, err
	}
	info := &narinfo.NarInfo{StorePath: output.StorePath, NarHash: hash, FileHash: hash, NarSize: output.NARSize, FileSize: output.NARSize, Compression: "none", References: []string{}}
	for _, ref := range output.References {
		if !storepath.PathRe.MatchString(ref) {
			return nil, errors.New("invalid store reference")
		}
		info.References = append(info.References, filepath.Base(ref))
	}
	slices.Sort(info.References)
	info.References = slices.Compact(info.References)
	if output.Deriver != nil {
		info.Deriver = filepath.Base(*output.Deriver)
	}
	for _, raw := range output.Signatures {
		sig, err := signature.ParseSignature(raw)
		if err != nil {
			return nil, err
		}
		info.Signatures = append(info.Signatures, sig)
	}
	if !storepath.PathRe.MatchString(info.StorePath) {
		return nil, errors.New("invalid store path")
	}
	info.URL = ArchivePath(info)
	if err := CheckCacheInfo(info, filepath.Base(info.StorePath)[:32]); err != nil {
		return nil, err
	}
	sig, err := key.Sign(nil, info.Fingerprint())
	if err != nil {
		return nil, err
	}
	info.Signatures = append(info.Signatures, sig)
	return info, nil
}

// The v1 peer protocol is uncompressed SHA-256. The signature binds the NAR
// hash/size/references; requiring identical file metadata closes the unsigned
// compression/file-hash fields, and a canonical relative URL prevents proxying.
func CheckCacheInfo(info *narinfo.NarInfo, hash string) error {
	if !storepath.PathRe.MatchString(info.StorePath) || !strings.HasPrefix(filepath.Base(info.StorePath), hash+"-") || info.NarHash == nil || info.FileHash == nil || info.NarHash.Algo() != nixhash.SHA256 || info.NarSize == 0 || info.NarSize > 1<<63-1 || info.FileSize != info.NarSize || info.Compression != "none" || !slices.IsSorted(info.References) {
		return errors.New("invalid cache metadata")
	}
	// go-nix Check normalizes relative paths; require canonical spelling as well.
	for _, ref := range info.References {
		if !storepath.PathRe.MatchString(storepath.StoreDir + "/" + ref) {
			return errors.New("noncanonical cache reference")
		}
	}
	if err := info.Check(); err != nil {
		return err
	}
	if info.URL != ArchivePath(info) {
		return errors.New("noncanonical archive URL")
	}
	return nil
}
