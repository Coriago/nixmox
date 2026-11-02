// Package testutil supplies a disposable, mutually authenticated JetStream server
// for high-level integration examples. It never binds outside loopback.
package testutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/cluster"
	"nixmox/internal/identity"
)

func JetStream(t *testing.T) jetstream.JetStream {
	t.Helper()
	root := t.TempDir()
	node, err := identity.Open(filepath.Join(root, "identity"), "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { node.Close() })
	ca, err := identity.NewAuthority("test", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	csr, err := node.CSR()
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := identity.Thumbprint(node.Key.Public())
	if err != nil {
		t.Fatal(err)
	}
	cert, err := ca.Issue(csr, node.State.NodeID, fingerprint, []string{"127.0.0.1"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	key, err := os.ReadFile(filepath.Join(root, "identity", "node.key"))
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig, err := cluster.TLSConfig(cert, key, ca.PEM)
	if err != nil {
		t.Fatal(err)
	}
	s, err := cluster.StartServer(server.Options{ServerName: "integration", Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: filepath.Join(root, "nats"), TLSConfig: tlsConfig, TLSMap: true, TLSVerify: true, Users: []*server.User{{Username: "CN=" + node.State.NodeID}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Shutdown(); s.WaitForShutdown() })
	nc, err := cluster.Connect([]string{s.ClientURL()}, tlsConfig, node.State.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err = js.AccountInfo(ctx); err != nil {
		t.Fatal(err)
	}
	return js
}
