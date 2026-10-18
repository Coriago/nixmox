// Package natsx wraps the embedded NATS server + JetStream lifecycle used by
// the cluster supervisor: option construction, start/stop, meta-leader
// readiness, and KV bucket helpers.
package natsx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

const InstallerPort = 7422

type EmbeddedServer struct {
	options server.Options
	Srv     *server.Server
	NC      *nats.Conn
	JS      jetstream.JetStream
	Sys     *nats.Conn
}

// NewOptions builds server.Options. ClientPort listens on 127.0.0.1 (local
// agent only); the cluster port listens on clusterHost for peer routes.
// Standalone and clustered modes use separate store subdirs: a store that
// initialized standalone JetStream wedges the clustered RAFT meta group when
// reused, and standalone state is disposable anyway.
func NewOptions(serverName string, clientPort, clusterPort int, clusterHost, dataDir string, spec ClusterSpec) (server.Options, error) {
	// NATS interprets zero as its fixed default; callers use zero for an ephemeral port.
	if clientPort == 0 {
		clientPort = -1
	}
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
		// Keep the existing default JetStream API reachable across temporary
		// leaf links without sharing $SYS or extending the Raft group.
		JsAccDefaultDomain: map[string]string{"$G": ""},
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
	// System-account credentials are ephemeral and only used on loopback.
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return nil, err
	}
	password := hex.EncodeToString(secret[:])
	sys := server.NewAccount("$SYS")
	opts.Accounts = append(opts.Accounts, sys)
	opts.SystemAccount = "$SYS"
	opts.Users = []*server.User{{Username: "nixmox-system", Password: password, Account: sys}, {Username: "nixmox-local"}}
	opts.NoAuthUser = "nixmox-local"
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
	system, err := nats.Connect(srv.ClientURL(), nats.UserInfo("nixmox-system", password))
	if err != nil {
		nc.Close()
		srv.Shutdown()
		return nil, err
	}
	return &EmbeddedServer{Srv: srv, NC: nc, JS: js, Sys: system, options: opts}, nil
}

// WaitReady polls until JetStream answers on this node — leader or follower.
// (JetStreamIsLeader alone never becomes true on followers, so it cannot
// gate readiness.) Meta-leader election takes ~4–9s on fresh clusters.
func WaitReady(js jetstream.JetStream, timeout time.Duration) error {
	return WaitReadyContext(context.Background(), js, timeout)
}
func WaitReadyContext(parent context.Context, js jetstream.JetStream, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	for {
		attempt, stop := context.WithTimeout(ctx, 2*time.Second)
		_, err := js.AccountInfo(attempt)
		stop()
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("jetstream readiness: %w", ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (e *EmbeddedServer) Stop() {
	if e.Sys != nil {
		e.Sys.Close()
	}
	if e.NC != nil {
		e.NC.Close()
	}
	if e.Srv != nil {
		e.Srv.Shutdown()
		e.Srv.WaitForShutdown()
	}
}

// ConnectInstaller extends this server's account to a selected temporary node.
// The installer has no JetStream store; commands remain durable in the hub.
func (e *EmbeddedServer) ConnectInstaller(address string) error {
	u, err := url.Parse("nats://" + address)
	if err != nil || u.Hostname() == "" || u.Port() == "" {
		return fmt.Errorf("invalid installer NATS address")
	}
	for _, remote := range e.options.LeafNode.Remotes {
		if len(remote.URLs) == 1 && remote.URLs[0].String() == u.String() {
			return nil
		}
	}
	opts := e.options
	opts.LeafNode.Remotes = append(append([]*server.RemoteLeafOpts{}, opts.LeafNode.Remotes...), &server.RemoteLeafOpts{URLs: []*url.URL{u}, Hub: true})
	if err := e.Srv.ReloadOptions(&opts); err != nil {
		return err
	}
	e.options = opts
	return nil
}

type stdLogger struct{ l *log.Logger }

func (e *EmbeddedServer) DisconnectInstaller(address string) error {
	opts := e.options
	opts.LeafNode.Remotes = nil
	for _, remote := range e.options.LeafNode.Remotes {
		if len(remote.URLs) != 1 || remote.URLs[0].Host != address {
			opts.LeafNode.Remotes = append(opts.LeafNode.Remotes, remote)
		}
	}
	if err := e.Srv.ReloadOptions(&opts); err != nil {
		return err
	}
	e.options = opts
	return nil
}

func newStdLogger() *stdLogger { return &stdLogger{l: log.New(os.Stderr, "[nats] ", 0)} }

func (s *stdLogger) Noticef(f string, a ...any) { s.l.Printf("NOTICE "+f, a...) }
func (s *stdLogger) Warnf(f string, a ...any)   { s.l.Printf("WARN "+f, a...) }
func (s *stdLogger) Errorf(f string, a ...any)  { s.l.Printf("ERROR "+f, a...) }
func (s *stdLogger) Debugf(f string, a ...any)  { s.l.Printf("DEBUG "+f, a...) }
func (s *stdLogger) Tracef(f string, a ...any)  { s.l.Printf("TRACE "+f, a...) }
func (s *stdLogger) Fatalf(f string, a ...any)  { s.l.Printf("FATAL "+f, a...) }
