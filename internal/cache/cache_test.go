package cache_test

import (
	"context"
	"crypto/sha256"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nix-community/go-nix/pkg/narinfo"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"
	"github.com/nix-community/go-nix/pkg/storepath"
	"nixmox/internal/cache"
	"nixmox/internal/host"
)

func TestSignedCache(t *testing.T) {
	// --- Test setup ---
	// ------------------
	path := os.Getenv("NIXMOX_TEST_STORE_PATH")
	if path == "" {
		t.Skip("run nix develop -c just test-cache for real Nix cache validation")
	}
	if !storepath.PathRe.MatchString(path) {
		t.Fatal("invalid test store path")
	}
	secret, public, err := signature.GenerateKeypair("nixmox-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	local, err := cache.NewLocal(secret.String(), host.Process{})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(local)
	defer server.Close()
	missing := httptest.NewServer(http.NotFoundHandler())
	defer missing.Close()
	gateway := cache.NewGateway()
	defer gateway.Close()
	if err = gateway.ReplacePeers([]cache.Peer{{NodeID: "missing", StoreURL: missing.URL}, {NodeID: "present", StoreURL: server.URL}}); err != nil {
		t.Fatal(err)
	}
	proxy := httptest.NewServer(gateway)
	defer proxy.Close()
	client := &http.Client{Timeout: 10 * time.Second}

	// --- Usage ---
	// -------------
	response, err := client.Get(proxy.URL + "/" + filepath.Base(path)[:32] + ".narinfo")
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := narinfo.Parse(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	response, err = client.Get(proxy.URL + "/" + metadata.URL)
	if err != nil {
		t.Fatal(err)
	}
	hasher := sha256.New()
	size, err := io.Copy(hasher, response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}

	// --- Assertions ---
	// ------------------
	if metadata.StorePath != path || len(metadata.Signatures) != 1 || !public.Verify(metadata.Fingerprint(), metadata.Signatures[0]) {
		t.Fatal("cache signature did not verify")
	}
	if uint64(size) != metadata.NarSize || string(hasher.Sum(nil)) != string(metadata.NarHash.Digest()) {
		t.Fatal("go-nix NAR differs from registered Nix hash/size")
	}
	// Use a separate local store: this verifies the real Nix substituter protocol
	// without deleting an existing path or changing the host's Nix configuration.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	destination := t.TempDir()
	command := exec.CommandContext(ctx, "nix", "copy", "--from", proxy.URL, "--to", "local?root="+destination, "--extra-trusted-public-keys", public.String(), path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Nix cache import: %v\n%s", err, output)
	}
	if _, err = os.Lstat(filepath.Join(destination, strings.TrimPrefix(path, "/"))); err != nil {
		t.Fatal(err)
	}
}
