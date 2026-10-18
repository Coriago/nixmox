package server

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nixmox/internal/messaging"
	"nixmox/internal/natsx"
)

type installerTestCluster struct {
	ClusterAPI
	hub     *natsx.EmbeddedServer
	address string
}

func (c installerTestCluster) ConnectInstaller(string) error {
	return c.hub.ConnectInstaller(c.address)
}
func (c installerTestCluster) DisconnectInstaller(context.Context, string, string) error {
	return c.hub.DisconnectInstaller(c.address)
}

// Exercise the real leaf bridge and durable bus. No installer HTTP server exists.
func installerBus(t *testing.T, s *Server, handler http.Handler) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	opts, _ := natsx.NewOptions("coordinator", 0, 0, "127.0.0.1", t.TempDir(), natsx.ClusterSpec{})
	hub, err := natsx.Start(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hub.Stop)
	opts, _ = natsx.NewOptions("temporary", 0, 0, "127.0.0.1", t.TempDir(), natsx.ClusterSpec{})
	opts.JetStream = false
	opts.StoreDir = ""
	opts.LeafNode.Host = "127.0.0.1"
	opts.LeafNode.Port = -1
	leaf, err := natsx.Start(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(leaf.Stop)
	info, _ := leaf.Srv.Varz(nil)
	s.Cluster = installerTestCluster{s.Cluster, hub, fmt.Sprintf("127.0.0.1:%d", info.LeafNode.Port)}
	hubBus, err := messaging.New(ctx, s.Config.Hostname, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.SetBus(hubBus)
	hubBus.Rearm(ctx, hub)
	t.Cleanup(hubBus.Stop)
	if err := s.Cluster.(installerTestCluster).ConnectInstaller(""); err != nil {
		t.Fatal(err)
	}
	if err := natsx.WaitReady(leaf.JS, 10*time.Second); err != nil {
		t.Fatal(err)
	}
	for _, node := range []string{"installer", "newhost"} {
		bus, err := messaging.New(ctx, node, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		bus.Handle = func(ctx context.Context, req messaging.Request) messaging.Response {
			if req.Path == "/api/v1/host" && req.Target == "installer" {
				return messaging.Response{Status: 200, Body: []byte(`{}`)}
			}
			r := httptest.NewRequest(req.Method, req.Path, bytes.NewReader(req.Body)).WithContext(ctx)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			return messaging.Response{Status: w.Code, Body: w.Body.Bytes()}
		}
		bus.Rearm(ctx, leaf)
		t.Cleanup(bus.Stop)
	}
	return "127.0.0.1:1" // Deliberately no reachable HTTP server.
}

func TestInstallerHTTPControlRemoved(t *testing.T) {
	s := operationServer(t)
	s.Config.InstallerMode = true
	for _, method := range []string{"GET", "POST"} {
		for _, path := range []string{"info", "reserve", "pull", "install", "jobs/test", "reboot", "reset-plan"} {
			if w := call(s, method, "/api/v1/installer/"+path, `{}`); w.Code != 404 {
				t.Fatalf("HTTP control still exposed: %s %s: %d", method, path, w.Code)
			}
		}
	}
}
