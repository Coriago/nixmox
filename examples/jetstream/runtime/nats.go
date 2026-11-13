// Package runtime contains low-level services usable before controllers exist.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
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
	ID               string
	Directory        string
	primary          *server.Server
	bootstrap        *server.Server
	client           *nats.Conn
	jetstream        jetstream.JetStream
	primaryOptions   *server.Options
	bootstrapOptions *server.Options
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
	return m.startServer(ctx, &m.primary, &m.primaryOptions, options)
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
	return m.startServer(ctx, &m.bootstrap, &m.bootstrapOptions, options)
}

func (m *NATSManager) serverOptions(kind string) (*server.Options, error) {
	options := &server.Options{}
	options.ServerName = m.ID + "-" + kind + "-" + nuid.Next()
	options.Host, options.Port = "127.0.0.1", -1
	options.JetStream, options.StoreDir = true, filepath.Join(m.Directory, kind)
	options.Cluster = server.ClusterOpts{Name: "example", Host: "127.0.0.1", Port: -1}
	options.NoSigs = true
	previous := m.primaryOptions
	if kind == "bootstrap" {
		previous = m.bootstrapOptions
	}
	if previous != nil {
		options.Tags = append(options.Tags, previous.Tags...)
	}
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

func (m *NATSManager) JetStream() jetstream.JetStream { return m.jetstream }

// StopPrimary shuts down immediately, without lame-duck client draining.
func (m *NATSManager) StopPrimary(ctx context.Context) error { return m.stopServer(ctx, m.primary) }

// StopBootstrap shuts down immediately, without lame-duck client draining.
func (m *NATSManager) StopBootstrap(ctx context.Context) error { return m.stopServer(ctx, m.bootstrap) }

// GracefulStopPrimary drains clients using lame-duck mode, then shuts down.
func (m *NATSManager) GracefulStopPrimary(ctx context.Context) error {
	return m.gracefulStopServer(ctx, m.primary)
}

// GracefulStopBootstrap drains clients using lame-duck mode, then shuts down.
func (m *NATSManager) GracefulStopBootstrap(ctx context.Context) error {
	return m.gracefulStopServer(ctx, m.bootstrap)
}

func (m *NATSManager) startServer(ctx context.Context, instance **server.Server, saved **server.Options, options *server.Options) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	existing := *instance
	if existing != nil && existing.Running() {
		return nil
	}
	s, err := server.NewServer(options.Clone())
	if err != nil {
		return err
	}
	*instance = s
	*saved = options.Clone()
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
// Storage is retained. Context is checked before starting; once started,
// shutdown is awaited to completion, even if the context is cancelled.
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

// Like stopServer, this retains storage and does not evacuate replicas or remove
// metadata membership. Context cancellation does not interrupt an active drain.
func (m *NATSManager) gracefulStopServer(ctx context.Context, instance *server.Server) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if instance == nil || !instance.Running() {
		return nil
	}
	instance.LameDuckShutdown()
	instance.WaitForShutdown()
	return nil
}

// RemovePrimary gracefully stops primary, removes its metadata peer, then deletes
// its data directory. The caller must evacuate first and provide administrative
// access through a surviving server. Errors leave the local instance tracked for retry.
func (m *NATSManager) RemovePrimary(ctx context.Context) error {
	return m.removeServer(ctx, &m.primary, &m.primaryOptions)
}

// RemoveBootstrap performs the same removal sequence for bootstrap.
func (m *NATSManager) RemoveBootstrap(ctx context.Context) error {
	return m.removeServer(ctx, &m.bootstrap, &m.bootstrapOptions)
}

func (m *NATSManager) removeServer(ctx context.Context, instance **server.Server, options **server.Options) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if *instance == nil {
		return nil
	}
	if *options == nil || (*options).StoreDir == "" {
		return errors.New("server data directory is unknown")
	}
	if m.client == nil || m.client.IsClosed() {
		return errors.New("client connection is not open")
	}
	if err := m.gracefulStopServer(ctx, *instance); err != nil {
		return err
	}
	if err := m.RemovePeer(ctx, (*instance).Name()); err != nil {
		return err
	}
	if err := os.RemoveAll((*options).StoreDir); err != nil {
		return err
	}
	*instance, *options = nil, nil
	return nil
}

// SetPrimaryTags replaces the running primary's advertised tags. Only streams
// whose placement requires these tags are restricted by them.
func (m *NATSManager) SetPrimaryTags(ctx context.Context, tags []string) error {
	return m.setServerTags(ctx, m.primary, &m.primaryOptions, tags)
}

// SetBootstrapTags replaces the running bootstrap's advertised tags.
func (m *NATSManager) SetBootstrapTags(ctx context.Context, tags []string) error {
	return m.setServerTags(ctx, m.bootstrap, &m.bootstrapOptions, tags)
}

// Reload only the tags, retaining the server's original configuration. A successful
// reload is not proof that placement changes or replica migration have completed.
func (m *NATSManager) setServerTags(ctx context.Context, instance *server.Server, saved **server.Options, tags []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if instance == nil || !instance.Running() || *saved == nil {
		return errors.New("server is not running")
	}
	options := (*saved).Clone()
	options.Tags = append(options.Tags[:0:0], tags...)
	if err := instance.ReloadOptions(options.Clone()); err != nil {
		return err
	}
	*saved = options
	return nil
}

// RemovePeer submits a metadata removal request through the client connection.
// Administrative access must be configured separately; an in-process connection
// does not grant system-account privileges.
// For retirement, stop the target first and connect to a survivor.
// Removing a live target disables its JetStream; it does not stop its process.
// This request neither evacuates data nor deletes local storage.
func (m *NATSManager) RemovePeer(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("server name is required")
	}
	if m.client == nil || m.client.IsClosed() {
		return errors.New("client connection is not open")
	}
	data, err := json.Marshal(server.JSApiMetaServerRemoveRequest{Server: name})
	if err != nil {
		return err
	}
	msg, err := m.client.RequestWithContext(ctx, server.JSApiRemoveServer, data)
	if err != nil {
		return err
	}
	var response server.JSApiMetaServerRemoveResponse
	if err := json.Unmarshal(msg.Data, &response); err != nil {
		return err
	}
	if response.Error != nil {
		// Removal may have succeeded before a lost reply or local cleanup failure.
		if response.Error.ErrCode == uint16(server.JSClusterServerNotMemberErr) {
			return nil
		}
		return response.Error
	}
	if !response.Success {
		return errors.New("peer removal did not succeed")
	}
	return nil
}

// Connect uses the running local primary in-process, or endpoints otherwise.
// Call again to switch transports; an in-process connection does not fail over to TCP.
func (m *NATSManager) Connect(ctx context.Context, endpoints []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	options := []nats.Option{nats.Timeout(2 * time.Second)}
	address := strings.Join(endpoints, ",")
	if m.primary != nil && m.primary.Running() {
		options = append(options, nats.InProcessServer(m.primary))
		address = m.primary.ClientURL()
	} else if len(endpoints) == 0 {
		return errors.New("endpoints are required when primary is not running")
	}
	nc, err := nats.Connect(address, options...)
	if err != nil {
		return err
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return err
	}
	m.Disconnect()
	m.client, m.jetstream = nc, js
	return nil
}

func (m *NATSManager) Disconnect() {
	if m.client != nil {
		m.client.Close()
		m.client = nil
	}
	m.jetstream = nil
}

