// Package natsx wraps the embedded NATS server + JetStream lifecycle used by
// the cluster supervisor: option construction, start/stop, meta-leader
// readiness, and KV bucket helpers.
package natsx

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// ClusterSpec describes the NATS cluster identity for one reconfigure.
// Name == "" means standalone mode (no Cluster block at all).
type ClusterSpec struct {
	Name   string
	Routes []string
}

type EmbeddedServer struct {
	Srv *server.Server
	NC  *nats.Conn
	JS  jetstream.JetStream
}

// NewOptions builds server.Options. ClientPort listens on 127.0.0.1 (local
// agent only); the cluster port listens on clusterHost for peer routes.
// Standalone and clustered modes use separate store subdirs: a store that
// initialized standalone JetStream wedges the clustered RAFT meta group when
// reused, and standalone state is disposable anyway.
func NewOptions(serverName string, clientPort, clusterPort int, clusterHost, dataDir string, spec ClusterSpec) (server.Options, error) {
	storeSub := "nats-standalone"
	if spec.Name != "" {
		storeSub = "nats-clustered"
	}
	opts := server.Options{
		ServerName: serverName,
		Host:       "127.0.0.1",
		Port:       clientPort,
		JetStream:  true,
		StoreDir:   filepath.Join(dataDir, storeSub),
		NoSigs:     true,
	}
	if spec.Name != "" {
		opts.Cluster = server.ClusterOpts{
			Name: spec.Name,
			Host: clusterHost,
			Port: clusterPort,
		}
		routes := make([]*url.URL, 0, len(spec.Routes))
		for _, raw := range spec.Routes {
			u, err := url.Parse(raw)
			if err != nil {
				return server.Options{}, fmt.Errorf("route %q: %w", raw, err)
			}
			routes = append(routes, u)
		}
		// Clustered JetStream refuses to start with an empty routes list, and
		// self-routes stall election ("Waiting for routing to be established").
		// Cluster mode therefore requires at least one real peer route; a
		// created-but-alone cluster keeps running standalone JetStream instead.
		if len(routes) == 0 {
			return server.Options{}, fmt.Errorf("cluster mode requires at least one route")
		}
		opts.Routes = routes
	}
	return opts, nil
}

func Start(opts server.Options) (*EmbeddedServer, error) {
	srv, err := server.NewServer(&opts)
	if err != nil {
		return nil, fmt.Errorf("new nats server: %w", err)
	}
	if os.Getenv("NIXMOX_NATS_DEBUG") != "" {
		srv.SetLogger(newStdLogger(), true, false)
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		srv.Shutdown()
		return nil, fmt.Errorf("nats server not ready within 10s")
	}

	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		srv.Shutdown()
		return nil, fmt.Errorf("in-process connect: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		srv.Shutdown()
		return nil, fmt.Errorf("jetstream context: %w", err)
	}
	return &EmbeddedServer{Srv: srv, NC: nc, JS: js}, nil
}

// WaitReady polls until JetStream answers on this node — leader or follower.
// (JetStreamIsLeader alone never becomes true on followers, so it cannot
// gate readiness.) Meta-leader election takes ~4–9s on fresh clusters.
func WaitReady(js jetstream.JetStream, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, err := js.AccountInfo(ctx)
		cancel()
		if err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("jetstream not ready within %s", timeout)
}

func (e *EmbeddedServer) Stop() {
	if e.NC != nil {
		e.NC.Close()
	}
	if e.Srv != nil {
		e.Srv.Shutdown()
		e.Srv.WaitForShutdown()
	}
}

type stdLogger struct{ l *log.Logger }

func newStdLogger() *stdLogger { return &stdLogger{l: log.New(os.Stderr, "[nats] ", 0)} }

func (s *stdLogger) Noticef(f string, a ...any) { s.l.Printf("NOTICE "+f, a...) }
func (s *stdLogger) Warnf(f string, a ...any)   { s.l.Printf("WARN "+f, a...) }
func (s *stdLogger) Errorf(f string, a ...any)  { s.l.Printf("ERROR "+f, a...) }
func (s *stdLogger) Debugf(f string, a ...any)  { s.l.Printf("DEBUG "+f, a...) }
func (s *stdLogger) Tracef(f string, a ...any)  { s.l.Printf("TRACE "+f, a...) }
func (s *stdLogger) Fatalf(f string, a ...any)  { s.l.Printf("FATAL "+f, a...) }
