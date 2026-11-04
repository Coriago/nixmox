package daemon

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"nixmox/internal/cache"
	"nixmox/internal/cluster"
	"nixmox/internal/credentials"
	"nixmox/internal/domain"
	"nixmox/internal/identity"
)

// startup is private recovery material, not operator configuration or a
// cluster-join seed. PeerRoutes records prepared server intent; shared MEMBERS
// remains authoritative. InitialCredentials keeps the legacy JSON key for recovery.
type startup struct {
	PeerRoutes         []string             `json:"peerRoutes,omitempty"`
	Cluster            domain.Cluster       `json:"cluster"`
	Member             domain.Member        `json:"member"`
	Certificate        string               `json:"certificate"`
	NATSEndpoints      []string             `json:"natsEndpoints,omitempty"`
	InitialCredentials []credentials.Record `json:"seedCredentials,omitempty"`
}

func (r *Runtime) saveStartup(saved startup) error {
	data, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	if err = identity.WritePrivate(filepath.Join(r.config.StateDir, "node.crt"), []byte(saved.Certificate)); err != nil {
		return err
	}
	if err = identity.WritePrivate(filepath.Join(r.config.StateDir, "cluster-ca.crt"), []byte(saved.Cluster.CA.Certificate)); err != nil {
		return err
	}
	return identity.WritePrivate(filepath.Join(r.config.StateDir, "cluster.json"), data)
}

func (r *Runtime) startCluster(saved startup) (err error) {
	if saved.Member.Status == "pending" || saved.Member.Status == "full" || len(saved.PeerRoutes) > 0 {
		return r.startMember(saved)
	}
	if saved.Member.Status != "full-stable" {
		return errors.New("unsupported startup membership role")
	}
	return r.startBootstrap(saved)
}

// startMember restores an enrolled member without seeding shared state.
// Prepared peers start routes first, using only read-limited local authorization
// until authoritative membership can be read from the restored quorum.
func (r *Runtime) startMember(saved startup) error {
	ctx, cancel := context.WithTimeout(r.lifetime, 30*time.Second)
	defer cancel()
	tlsConfig, err := r.nodeTLS(saved)
	if err != nil {
		return err
	}
	endpoints := saved.NATSEndpoints
	if len(saved.PeerRoutes) > 0 {
		if saved.Member.Status != "full" && saved.Member.Status != "full-stable" {
			return errors.New("saved membership cannot start a peer")
		}
		r.authorization.Lock()
		peer, startErr := r.startPeerServer(saved, []domain.Member{{NodeID: saved.Member.NodeID, Status: "pending"}})
		r.authorization.Unlock()
		if startErr != nil {
			return startErr
		}
		if err = waitPeer(ctx, peer, 2); err != nil {
			return err
		}
		endpoint, err := localClientURL(peer)
		if err != nil {
			return err
		}
		endpoints = []string{endpoint}
	}
	nc, err := cluster.Connect(endpoints, tlsConfig, r.node.State.NodeID)
	if err != nil {
		return err
	}
	success := false
	defer func() {
		if !success {
			nc.Close()
		}
	}()
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}
	members, err := cluster.OpenStore[domain.Member](ctx, js, "MEMBERS")
	if err != nil {
		return err
	}
	current, err := members.Get(ctx, r.node.State.NodeID)
	if err != nil {
		return err
	}
	if current.Value.ClusterID != saved.Cluster.ID || (current.Value.Status != "pending" && current.Value.Status != "full" && !(current.Value.Status == "full-stable" && len(saved.PeerRoutes) > 0)) {
		return errors.New("pending startup differs from authoritative membership")
	}
	saved.Member = current.Value
	saved.Member.Environment = r.config.Environment
	if len(saved.PeerRoutes) > 0 {
		if saved.Member.Status == "pending" {
			return errors.New("pending member cannot restore a server")
		}
		if err = r.refreshAuthorization(ctx, members); err != nil {
			return err
		}
		if _, err = r.startPeer(ctx, saved, members); err != nil {
			return err
		}
	}
	if err = r.activate(ctx, saved, nc, js); err != nil {
		return err
	}
	success = true
	return nil
}

// activate is the common commit point for creator, client and peer startup.
// Only the transport/provisioning paths differ; service and node-state ownership
// must remain identical for manual enrollment and future seeded enrollment.
func (r *Runtime) activate(ctx context.Context, saved startup, nc *nats.Conn, js jetstream.JetStream) error {
	svc, err := r.wireServices(ctx, js, nc, saved.Member)
	if err != nil {
		return err
	}
	gateway := svc.Cache.(*cache.Gateway)
	success := false
	defer func() {
		if !success {
			gateway.Close()
		}
	}()
	if err = r.saveStartup(saved); err != nil {
		return err
	}
	r.mu.Lock()
	r.node.State.ClusterID = saved.Cluster.ID
	r.node.State.Status = saved.Member.Status
	err = r.node.Save()
	if err == nil {
		r.client, r.services, r.gateway, r.status = nc, svc, gateway, "online"
	}
	r.mu.Unlock()
	if err != nil {
		return err
	}
	if err = r.refreshAuthorization(ctx, svc.Members); err != nil {
		return err
	}
	if err = r.startObservations(nc, saved.Member.NodeID); err != nil {
		return err
	}
	if err = r.runServices(svc); err != nil {
		return err
	}
	success = true
	return nil
}

func (r *Runtime) peerOptions(saved startup, tlsConfig *tls.Config, routes []*url.URL, port int, members []domain.Member) (server.Options, error) {
	options := server.Options{ServerName: r.node.State.NodeID, Host: r.config.NATSHost, Port: r.config.NATSPort, JetStream: true, StoreDir: filepath.Join(r.config.StateDir, "nats", "main"), TLSConfig: tlsConfig, TLSVerify: true, TLSMap: true, Routes: routes, Cluster: server.ClusterOpts{Name: saved.Cluster.ID, Host: r.config.NATSHost, Port: port, TLSConfig: tlsConfig.Clone()}}
	err := cluster.ConfigureAccounts(&options, members)
	return options, err
}

func localClientURL(s *server.Server) (string, error) {
	endpoint, err := url.Parse(s.ClientURL())
	if err != nil {
		return "", err
	}
	if endpoint.Hostname() == "0.0.0.0" || endpoint.Hostname() == "::" {
		endpoint.Host = net.JoinHostPort("127.0.0.1", endpoint.Port())
	}
	return endpoint.String(), nil
}

// startPeer joins the existing cluster; it never seeds membership, credentials,
// buckets, or another bootstrap server. Both live promotion and cold restore use it.
func (r *Runtime) startPeer(ctx context.Context, saved startup, store cluster.Store[domain.Member]) (*server.Server, error) {
	r.authorization.Lock()
	defer r.authorization.Unlock()
	records, err := store.All(ctx)
	if err != nil {
		return nil, err
	}
	members := make([]domain.Member, 0, len(records))
	stable := 0
	for _, record := range records {
		members = append(members, record.Value)
		if record.Value.Status == "full-stable" || record.Key == saved.Member.NodeID {
			stable++
		}
	}
	plan, err := cluster.Plan(stable)
	if err != nil {
		return nil, err
	}
	expected := stable
	if plan.Bootstrap {
		expected++
	}
	peer, err := r.startPeerServer(saved, members)
	if err != nil {
		return nil, err
	}
	return peer, waitPeer(ctx, peer, expected)
}

// startPeerServer requires the authorization lock. Starting routes before KV
// reads permits a cold restart when every stable peer is offline.
func (r *Runtime) startPeerServer(saved startup, members []domain.Member) (*server.Server, error) {
	r.mu.RLock()
	if len(r.servers) > 0 {
		peer := r.servers[0]
		r.mu.RUnlock()
		return peer, nil
	}
	r.mu.RUnlock()
	routes := make([]*url.URL, 0, len(saved.PeerRoutes))
	for _, raw := range saved.PeerRoutes {
		route, err := url.Parse(raw)
		if err != nil || route.Scheme != "nats-route" || route.Hostname() == "" {
			return nil, errors.New("invalid peer route")
		}
		routes = append(routes, route)
	}
	if len(routes) == 0 {
		return nil, errors.New("peer requires existing cluster routes")
	}
	tlsConfig, err := r.nodeTLS(saved)
	if err != nil {
		return nil, err
	}
	ports, err := routePorts(r.config.NATSHost, r.config.RoutePort)
	if err != nil {
		return nil, err
	}
	options, err := r.peerOptions(saved, tlsConfig, routes, ports[0], members)
	if err != nil {
		return nil, err
	}
	peer, err := cluster.StartServer(options)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.servers = append(r.servers, peer)
	r.serverOptions = append(r.serverOptions, options.Clone())
	r.mu.Unlock()
	r.authorizedMembers = ""
	return peer, nil
}

func waitPeer(ctx context.Context, peer *server.Server, expected int) error {
	for {
		info, err := peer.Jsz(&server.JSzOptions{})
		if err != nil {
			return err
		}
		if info.Meta != nil && info.Meta.Leader != "" && info.Meta.Size >= expected && peer.NumRoutes() > 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func replicateStreams(ctx context.Context, js jetstream.JetStream, replicas int) error {
	deadline, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	names := js.StreamNames(deadline)
	for name := range names.Name() {
		stream, err := js.Stream(deadline, name)
		if err != nil {
			return err
		}
		info := stream.CachedInfo()
		config := info.Config
		if config.Replicas != replicas {
			config.Replicas = replicas
			if _, err = js.UpdateStream(deadline, config); err != nil {
				return err
			}
		}
		if err = cluster.WaitStreamReady(deadline, stream, replicas); err != nil {
			return err
		}
	}
	return names.Err()
}

// Every embedded server needs independent account instances and an explicitly
// JetStream-enabled application account, including in clustered mode.
func configureAccounts(options *server.Options, nodeID string) error {
	return cluster.ConfigureAccounts(options, []domain.Member{{NodeID: nodeID, Status: "full-stable"}})
}

// Negative route ports are for disposable integration servers. Reserve them
// together so the main and bootstrap peers cannot select the same port.
func routePorts(host string, ports ...int) ([]int, error) {
	var reserved []net.Listener
	defer func() {
		for _, listener := range reserved {
			listener.Close()
		}
	}()
	for i, port := range ports {
		if port == 0 {
			return nil, errors.New("cluster route port must be configured")
		}
		if port < 0 {
			listener, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
			if err != nil {
				return nil, err
			}
			reserved = append(reserved, listener)
			ports[i] = listener.Addr().(*net.TCPAddr).Port
		}
	}
	return ports, nil
}

func strconvPort(port int) string { return fmt.Sprintf("%d", port) }
