package daemon

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/jsm.go"
	jsapi "github.com/nats-io/jsm.go/api"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"golang.org/x/crypto/ssh"
	"nixmox/internal/cluster"
	"nixmox/internal/domain"
)

// The management certificate reaches only native peer removal. Ordinary APP
// certificates cannot administer metadata, and role revocation disconnects SYS.
func TestScopedManagement(t *testing.T) {
	// --- Test setup ---
	// ------------------
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "test")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(root, "host.key")
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := Open(ctx, Config{StateDir: filepath.Join(root, "state"), Name: "manager", SSHHostKey: keyPath, AdvertiseURL: "http://127.0.0.1:8080", NATSHost: "127.0.0.1", NATSPort: -1, RoutePort: -1, BootstrapPort: -1, BootstrapRoutePort: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err = r.CreateCluster(ctx, "management"); err != nil {
		t.Fatal(err)
	}

	// --- Usage ---
	// -------------
	system, err := r.systemConnection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer system.Close()
	admin, err := jsm.New(system, jsm.WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	managementResult := admin.MetaPeerRemove("not-a-cluster-peer", "")
	ordinary, err := jsm.New(r.client, jsm.WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ordinaryResult := ordinary.MetaPeerRemove("not-a-cluster-peer", "")
	denied := make(chan error, 1)
	system.SetErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
		select {
		case denied <- err:
		default:
		}
	})
	if err = system.Publish("$SYS.REQ.SERVER.PING", nil); err != nil {
		t.Fatal(err)
	}
	if err = system.Flush(); err != nil {
		t.Fatal(err)
	}

	// --- Assertions ---
	// ------------------
	var native jsapi.ApiError
	if !errors.As(managementResult, &native) || native.ErrCode != uint16(server.JSClusterServerNotMemberErr) {
		t.Fatalf("management did not reach native metadata API: %v", managementResult)
	}
	if !errors.Is(ordinaryResult, nats.ErrNoResponders) && !errors.Is(ordinaryResult, nats.ErrTimeout) && !errors.Is(ordinaryResult, context.DeadlineExceeded) {
		t.Fatalf("ordinary certificate unexpectedly reached management API: %v", ordinaryResult)
	}
	select {
	case err = <-denied:
		if !strings.Contains(strings.ToLower(err.Error()), "permissions violation") {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("management permissions were not scoped")
	}
	revoked := make(chan struct{}, 1)
	system.SetDisconnectErrHandler(func(*nats.Conn, error) {
		select {
		case revoked <- struct{}{}:
		default:
		}
	})
	// Exercise the same native account reload used by membership authorization,
	// without performing a forbidden demotion of the last stable host.
	for i, peer := range r.servers {
		options := r.serverOptions[i].Clone()
		if err = cluster.ConfigureAccounts(options, []domain.Member{{NodeID: r.node.State.NodeID, Status: "pending"}}); err != nil {
			t.Fatal(err)
		}
		if err = peer.ReloadOptions(options); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case <-revoked:
	case <-ctx.Done():
		t.Fatal("management connection survived authorization revocation")
	}
}
