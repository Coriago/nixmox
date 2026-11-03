package discovery_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"

	"nixmox/internal/api"
	"nixmox/internal/cluster"
	"nixmox/internal/discovery"
	"nixmox/internal/testutil"
)

func TestDiscoveryUDPAndStorage(t *testing.T) {
	// --- Test setup ---
	// ------------------
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	js := testutil.JetStream(t)
	kv, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "DISCOVERY", TTL: discovery.Lifetime})
	if err != nil {
		t.Fatal(err)
	}
	var joined atomic.Bool
	service := &discovery.Service{Observer: uuid.NewString(), Store: func() (cluster.Store[discovery.Record], bool) {
		return cluster.Store[discovery.Record]{KV: kv}, joined.Load()
	}}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan error, 1)
	go func() {
		stopped <- service.Serve(ctx, conn, func(context.Context, []net.IP) (discovery.Advertisement, error) {
			return discovery.Advertisement{}, errors.New("receive-only test")
		})
	}()
	peer, err := net.DialUDP("udp4", nil, conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	advertisement := discovery.Advertisement{
		NodeID: uuid.NewString(), Name: "installer", Endpoints: []string{"http://192.168.77.100:8080"},
		Identity: discovery.Identity{Kind: "node", PublicKeyThumbprint: "sha256:" + strings.Repeat("a", 64)},
	}
	send := func() {
		t.Helper()
		data, err := json.Marshal(advertisement)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = peer.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	wait := func() discovery.Record {
		t.Helper()
		for ctx.Err() == nil {
			records, _, err := service.List(ctx, "", 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(records) == 1 {
				return records[0]
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("discovery packet was not retained")
		return discovery.Record{}
	}

	// --- Usage ---
	// -------------
	send()
	local := wait()
	if _, err := kv.Keys(ctx); !errors.Is(err, jetstream.ErrNoKeysFound) {
		t.Fatalf("unjoined listener wrote shared state: %v", err)
	}
	joined.Store(true)
	send()
	shared := wait()
	if shared.ID != local.ID {
		t.Fatal("observation identity changed across join")
	}
	if _, err = kv.Get(ctx, shared.ID); err != nil {
		t.Fatal(err)
	}
	// Reads remain available without the clustered API service middleware.
	handler := (api.Server{Discovery: service}).Handler()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "/discovery/"+shared.ID, nil))
	var returned discovery.Record
	if err = json.Unmarshal(response.Body.Bytes(), &returned); err != nil {
		t.Fatal(err)
	}

	// --- Assertions ---
	// ------------------
	if response.Code != 200 || returned.Advertisement.NodeID != advertisement.NodeID ||
		returned.ObservedByHostID != service.Observer || returned.Advertisement.Identity != advertisement.Identity {
		t.Fatalf("unexpected discovery response: %d %s", response.Code, response.Body.String())
	}
	cancel()
	select {
	case err = <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("discovery did not stop")
	}
}
