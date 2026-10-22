// Package broker embeds NATS and enforces certificate grants at its listeners.
package broker

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"path/filepath"
	"sync"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"nixmox/internal/domain"
	"nixmox/internal/pki"
)

type LeafGrant struct {
	NodeID          string
	Tasks           []string
	Acknowledgments []string
}
type Config struct {
	NodeID, ClusterID, DataDir      string
	ClientPort, RoutePort, LeafPort int
	Keys                            pki.Keys
	Roots                           *x509.CertPool
	FullBrokers                     map[string]bool
	Leaves                          map[string]LeafGrant
	Routes                          []*url.URL
	ControlPlane                    bool
	Provisional                     bool
	RemoteLeaf                      *url.URL
}
type Broker struct {
	Server *server.Server
	Client *nats.Conn
	config Config
	auth   *authentication
}

type authentication struct {
	mu      sync.RWMutex
	config  Config
	account *server.Account
}

func (a *authentication) Check(c server.ClientAuthentication) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	s := c.GetTLSConnectionState()
	if s == nil || len(s.VerifiedChains) == 0 || len(s.PeerCertificates) == 0 {
		return false
	}
	key := pki.Fingerprint(s.PeerCertificates[0].PublicKey)
	var publish, subscribe []string
	switch c.Kind() {
	case server.CLIENT:
		if key != pki.Fingerprint(a.config.Keys.Daemon.Leaf.PublicKey) {
			return false
		}
		prefix := "nx.local." + a.config.NodeID + ".>"
		if a.config.ClusterID != "" {
			prefix = "nx." + a.config.ClusterID + ".>"
		}
		publish = []string{prefix}
		subscribe = []string{prefix, "_INBOX." + a.config.NodeID + ".>"}
		if a.config.ControlPlane {
			publish = append(publish, "$JS.API.>", "$KV.nx_state.>", "$O.nx_git.>", "$JS.ACK.>")
		}
	case server.LEAF:
		grant, ok := a.config.Leaves[key]
		if !ok {
			return false
		}
		publish, subscribe = leafSubjects(a.config.ClusterID, grant)
	default:
		return false
	}
	if len(publish) == 0 || len(subscribe) == 0 {
		return false
	}
	c.RegisterUser(&server.User{Username: key, Account: a.account, ConnectionDeadline: s.PeerCertificates[0].NotAfter, Permissions: &server.Permissions{Publish: &server.SubjectPermission{Allow: publish}, Subscribe: &server.SubjectPermission{Allow: subscribe}}})
	return true
}

func Start(c Config) (*Broker, error) {
	c.FullBrokers = maps.Clone(c.FullBrokers)
	c.Leaves = maps.Clone(c.Leaves)
	for key, g := range c.Leaves {
		g.Tasks = append([]string(nil), g.Tasks...)
		g.Acknowledgments = append([]string(nil), g.Acknowledgments...)
		c.Leaves[key] = g
	}
	if !domain.SafeID.MatchString(c.NodeID) || (c.ClusterID != "" && !domain.SafeID.MatchString(c.ClusterID)) || c.Keys.Daemon.Leaf == nil || c.Keys.Broker.Leaf == nil {
		return nil, errors.New("invalid broker identity")
	}
	if c.ControlPlane && c.ClusterID == "" {
		return nil, errors.New("cluster identity required for durable broker")
	}
	if err := validateGrants(c); err != nil {
		return nil, err
	}
	account := server.NewAccount("NX")
	system := server.NewAccount("SYS")
	auth := &authentication{config: c, account: account}
	clientRoots := pki.Pool(c.Keys.Daemon.Leaf)
	if c.Roots != nil {
		clientRoots = c.Roots
	}
	clientTLS := pki.MutualServer(c.Keys.Broker, clientRoots, pki.OnlyKey(c.Keys.Daemon.Leaf))
	roots := c.Roots
	if roots == nil {
		roots = pki.Pool(c.Keys.Broker.Leaf)
	}
	routeAllowed := func(cert *x509.Certificate) bool {
		auth.mu.RLock()
		defer auth.mu.RUnlock()
		return auth.config.FullBrokers[pki.Fingerprint(cert.PublicKey)]
	}
	routeTLS := pki.MutualServer(c.Keys.Broker, roots, routeAllowed)
	routeTLS.RootCAs = roots
	routeTLS.ServerName = "broker.nixmox"
	leafTLS := pki.MutualServer(c.Keys.Broker, roots, func(cert *x509.Certificate) bool {
		auth.mu.RLock()
		defer auth.mu.RUnlock()
		_, ok := auth.config.Leaves[pki.Fingerprint(cert.PublicKey)]
		return ok
	})
	cluster := c.ClusterID
	if cluster == "" || !c.ControlPlane {
		cluster = "unjoined_" + c.NodeID
	}
	opts := &server.Options{
		ServerName: c.NodeID, Host: "127.0.0.1", Port: c.ClientPort, NoSigs: true, NoLog: true,
		Accounts: []*server.Account{account, system}, SystemAccount: "SYS", CustomClientAuthentication: auth,
		TLSConfig: clientTLS, TLSHandshakeFirst: true, TLSTimeout: 3, AuthTimeout: 3,
		MaxConn: 256, MaxSubs: 2048, MaxPayload: domain.MaxCommandBytes, MaxPending: 8 << 20, MaxControlLine: 4096,
		PingInterval: 10 * time.Second, MaxPingsOut: 2, WriteDeadline: 5 * time.Second,
		Cluster:  server.ClusterOpts{Name: cluster, Host: "127.0.0.1", Port: c.RoutePort, TLSConfig: routeTLS, TLSTimeout: 3, NoAdvertise: true, PoolSize: 1, PingInterval: 10 * time.Second, MaxPingsOut: 2, WriteDeadline: 5 * time.Second},
		LeafNode: server.LeafNodeOpts{Host: "127.0.0.1", Port: c.LeafPort, TLSConfig: leafTLS, TLSHandshakeFirst: true, TLSTimeout: 3, AuthTimeout: 3, NoAdvertise: true, WriteDeadline: 5 * time.Second},
		Routes:   c.Routes, JetStream: c.ControlPlane, StoreDir: filepath.Join(c.DataDir, "nats"), JetStreamMaxMemory: 64 << 20, JetStreamMaxStore: 4 << 30, SyncAlways: true, JetStreamRequestQueueLimit: 256, JetStreamInfoQueueLimit: 64, StreamMaxBufferedMsgs: 256, StreamMaxBufferedSize: 8 << 20,
	}
	if c.Provisional {
		opts.Cluster = server.ClusterOpts{}
	}
	if c.RemoteLeaf != nil {
		if c.ControlPlane || c.ClusterID == "" || c.Roots == nil {
			return nil, errors.New("invalid leaf membership")
		}
		opts.LeafNode.Remotes = []*server.RemoteLeafOpts{{LocalAccount: "NX", URLs: []*url.URL{c.RemoteLeaf}, TLSConfig: pki.MutualClient(c.Keys.Broker, roots, "broker.nixmox", routeAllowed), TLSHandshakeFirst: true, TLSTimeout: 3, DialTimeout: 3 * time.Second, IgnoreDiscoveredServers: true}}
	}
	srv, err := server.NewServer(opts)
	if err != nil {
		return nil, fmt.Errorf("broker configuration: %w", err)
	}
	account, err = srv.LookupAccount("NX")
	if err != nil {
		srv.Shutdown()
		return nil, errors.New("broker account unavailable")
	}
	auth.account = account
	b := &Broker{Server: srv, config: c, auth: auth}
	go srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		srv.Shutdown()
		srv.WaitForShutdown()
		return nil, errors.New("broker listeners unavailable")
	}
	if c.ControlPlane {
		if err = account.EnableJetStream(map[string]server.JetStreamAccountLimits{"": {MaxMemory: 64 << 20, MaxStore: 4 << 30, MaxStreams: 16, MaxConsumers: 256, MaxAckPending: 256, MemoryMaxStreamBytes: 8 << 20, StoreMaxStreamBytes: 2 << 30, MaxBytesRequired: true}}, nil); err != nil {
			b.Close()
			return nil, err
		}
	}
	b.Client, err = nats.Connect(b.URL(), nats.Secure(b.ClientTLS()), nats.TLSHandshakeFirst(), nats.Timeout(3*time.Second), nats.MaxReconnects(10), nats.ReconnectWait(time.Second), nats.ReconnectBufSize(1<<20), nats.CustomInboxPrefix("_INBOX."+c.NodeID), nats.NoEcho(), nats.ErrorHandler(func(*nats.Conn, *nats.Subscription, error) {}))
	if err != nil {
		b.Close()
		return nil, fmt.Errorf("authenticated local broker connection failed: %w", err)
	}
	return b, nil
}
func (b *Broker) ClientTLS() *tls.Config {
	return pki.MutualClient(b.config.Keys.Daemon, pki.Pool(b.config.Keys.Broker.Leaf), pki.ServerName(b.config.Keys.Broker.Leaf), pki.OnlyKey(b.config.Keys.Broker.Leaf))
}
func (b *Broker) URL() string { return "tls://" + b.Server.Addr().(*net.TCPAddr).String() }
func (b *Broker) Close() {
	if b.Client != nil {
		b.Client.Close()
	}
	b.Server.Shutdown()
	b.Server.WaitForShutdown()
}
