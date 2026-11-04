package daemon

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/cluster"
	"nixmox/internal/domain"
)

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
	r.mu.RLock()
	if len(r.servers) > 0 {
		peer := r.servers[0]
		r.mu.RUnlock()
		return peer, waitPeer(ctx, peer, expected)
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
	key, err := os.ReadFile(filepath.Join(r.config.StateDir, "node.key"))
	if err != nil {
		return nil, err
	}
	defer clear(key)
	tlsConfig, err := cluster.TLSConfig([]byte(saved.Certificate), key, []byte(saved.Cluster.CA.Certificate))
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
	return peer, waitPeer(ctx, peer, expected)
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
