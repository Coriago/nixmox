package identity

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalKeysPersistAndFailClosed(t *testing.T) {
	root := t.TempDir()
	first, err := Open(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(context.Background(), root)
	if err != nil || second.Identity != first.Identity || second.DaemonKey() != first.DaemonKey() {
		t.Fatalf("identity replaced: %v", err)
	}
	if string(first.Daemon.Leaf.RawSubjectPublicKeyInfo) == string(first.Broker.Leaf.RawSubjectPublicKeyInfo) {
		t.Fatal("daemon and broker share a private key")
	}
	path := filepath.Join(root, "pki/daemon/key.pem")
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(context.Background(), root); err == nil {
		t.Fatal("lost identity key silently replaced")
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("failed recovery generated a replacement key")
	}
}
