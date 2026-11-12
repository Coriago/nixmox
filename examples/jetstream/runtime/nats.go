// Package runtime contains low-level services usable before controllers exist.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nuid"
)

// Callers serialize conflicting calls on this manager, including status reads.
// No controller cache, dispatcher, KV record writes or replication policy lives here.
type NATSManager struct {
	ID        string
	Directory string
	primary   *server.Server
	bootstrap *server.Server
	client    *nats.Conn
	system    *nats.Conn
	jetstream jetstream.JetStream
}

type ServerStatus struct {
	Name          string
	Running       bool
	MetadataReady bool
	Endpoints     []string
	RouteURL      string
}
type NATSStatus struct{ Primary, Bootstrap ServerStatus }

func (m *NATSManager) StartPrimary(ctx context.Context, routes []string) error {
	options, err := m.serverOptions("primary")
	if err != nil {
		return err
	}
	if len(routes) == 0 {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return err
		}
		options.Cluster.Port = listener.Addr().(*net.TCPAddr).Port
		address := listener.Addr().String()
		if err := listener.Close(); err != nil {
			return err
		}
		routes = []string{"nats-route://" + address}
	}
	for _, route := range routes {
		u, err := url.Parse(route)
		if err != nil {
			return err
		}
		options.Routes = append(options.Routes, u)
	}
	return m.startServer(ctx, &m.primary, options)
}

func (m *NATSManager) StartBootstrap(ctx context.Context) error {
	primary := m.primary
	if primary == nil || !primary.Running() {
		return errors.New("bootstrap requires local primary")
	}
	options, err := m.serverOptions("bootstrap")
	if err != nil {
		return err
	}
	options.Routes = []*url.URL{{Scheme: "nats-route", Host: primary.ClusterAddr().String()}}
	return m.startServer(ctx, &m.bootstrap, options)
}

func (m *NATSManager) serverOptions(kind string) (*server.Options, error) {
	options := &server.Options{}
	// Fixed credentials are ONLY for this loopback example, not production auth.
	if err := options.ProcessConfigString(`accounts {
 APP { jetstream: enabled, users: [{user: app, password: example}] }
 SYS { users: [{user: sys, password: example}] }
 }
 system_account: SYS`); err != nil {
		return nil, err
	}
	options.ServerName = m.ID + "-" + kind + "-" + nuid.Next()
	options.Host, options.Port = "127.0.0.1", -1
	options.JetStream, options.StoreDir = true, filepath.Join(m.Directory, kind)
	options.Cluster = server.ClusterOpts{Name: "example", Host: "127.0.0.1", Port: -1}
	options.NoSigs = true
	return options, nil
}

func (m *NATSManager) GetStatus() NATSStatus {
	return NATSStatus{Primary: m.serverStatus(m.primary), Bootstrap: m.serverStatus(m.bootstrap)}
}

func (m *NATSManager) serverStatus(instance *server.Server) ServerStatus {
	if instance == nil {
		return ServerStatus{}
	}
	status := ServerStatus{Name: instance.Name(), Running: instance.Running()}
	if !status.Running {
		return status
	}
	status.Endpoints = []string{instance.ClientURL()}
	if address := instance.ClusterAddr(); address != nil {
		status.RouteURL = "nats-route://" + address.String()
	}
	if info, err := instance.Jsz(nil); err == nil && info.Meta != nil {
		status.MetadataReady = info.Meta.Leader != ""
	}
	return status
}

func (m *NATSManager) WatchStatus(ctx context.Context, changed func()) error {
	// Observe local process/readiness changes; signal the caller, never write KV.
	// Stub: no polling loop or controller-owned status cache in the manager.
	return errors.ErrUnsupported
}

func (m *NATSManager) JetStream() jetstream.JetStream { return m.jetstream }

func (m *NATSManager) StopPrimary(ctx context.Context) error { return m.stopServer(ctx, m.primary) }

func (m *NATSManager) StopBootstrap(ctx context.Context) error { return m.stopServer(ctx, m.bootstrap) }

func (m *NATSManager) startServer(ctx context.Context, instance **server.Server, options *server.Options) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	existing := *instance
	if existing != nil && existing.Running() {
		return nil
	}
	s, err := server.NewServer(options)
	if err != nil {
		return err
	}
	*instance = s
	go s.Start()
	deadline := time.Now().Add(5 * time.Second)
	for !s.ReadyForConnections(50 * time.Millisecond) {
		if ctx.Err() != nil || time.Now().After(deadline) {
			s.Shutdown()
			s.WaitForShutdown()
			return fmt.Errorf("start %s: timeout or cancellation: %v", options.ServerName, ctx.Err())
		}
	}
	return nil
}

// stopServer only stops the process. Eviction, metadata removal and shutdown
// authorization must be handled by the caller before invoking this method.
// Storage is retained.
func (m *NATSManager) stopServer(ctx context.Context, instance *server.Server) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if instance == nil || !instance.Running() {
		return nil
	}
	instance.Shutdown()
	instance.WaitForShutdown()
	return nil
}

func (m *NATSManager) Connect(ctx context.Context, endpoints []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	nc, err := nats.Connect(strings.Join(endpoints, ","), nats.UserInfo("app", "example"), nats.Timeout(2*time.Second))
	if err != nil {
		return err
	}
	sys, err := nats.Connect(strings.Join(endpoints, ","), nats.UserInfo("sys", "example"), nats.Timeout(2*time.Second))
	if err != nil {
		nc.Close()
		return err
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		sys.Close()
		return err
	}
	m.client, m.system, m.jetstream = nc, sys, js
	return nil
}

func (m *NATSManager) Disconnect() {
	if m.client != nil {
		m.client.Close()
		m.client = nil
	}
	if m.system != nil {
		m.system.Close()
		m.system = nil
	}
	m.jetstream = nil
}

// Close is local process teardown, not an eviction/demotion request.
// Call only after controller executions and status observation have stopped.
func (m *NATSManager) Close() {
	m.Disconnect()
	for _, instance := range []*server.Server{m.bootstrap, m.primary} {
		if instance != nil {
			instance.Shutdown()
			instance.WaitForShutdown()
		}
	}
	m.primary, m.bootstrap = nil, nil
}
