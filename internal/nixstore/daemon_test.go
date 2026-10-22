package nixstore

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"github.com/nix-community/go-nix/pkg/nar"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDaemonLeaseAndEmbeddedNARMatchRealStore(t *testing.T) {
	if _, err := os.Stat("/nix/var/nix/daemon-socket/socket"); err != nil {
		t.Skip("real Nix daemon integration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "executable"), []byte("fixture\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	data, err := exec.CommandContext(ctx, "nix-store", "--add", dir).Output()
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSpace(string(data))
	lease, err := (Daemon{}).Lease(ctx, filepath.Base(path)[:32])
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	info, err := (Nix{}).Info(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if lease.Metadata.Path != path || "sha256-"+base64.StdEncoding.EncodeToString(lease.Metadata.NarHash) != info.NarHash || lease.Metadata.NarSize != uint64(info.NarSize) {
		t.Fatal("daemon metadata differs", lease.Metadata, info)
	}
	hash := sha256.New()
	count := &countWriter{Writer: hash}
	if err = nar.DumpPath(count, path); err != nil {
		t.Fatal(err)
	}
	if uint64(count.count) != lease.Metadata.NarSize || "sha256-"+base64.StdEncoding.EncodeToString(hash.Sum(nil)) != info.NarHash {
		t.Fatal("embedded NAR differs from Nix")
	}
	if _, err = (Daemon{}).Lease(ctx, strings.Repeat("0", 32)); err == nil {
		t.Fatal("unknown store path was a hit")
	}
}

type countWriter struct {
	io.Writer
	count int64
}

func (w *countWriter) Write(b []byte) (int, error) {
	n, err := w.Writer.Write(b)
	w.count += int64(n)
	return n, err
}
