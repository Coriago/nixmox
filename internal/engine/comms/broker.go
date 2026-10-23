// Package comms implements embedded NATS, machine authorization and bounded transports.
package comms

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"

	"nixmox/internal/domain"
	"nixmox/internal/engine/internal/shared"
)

func Subject(cluster domain.ID, kind string, node domain.ID) string {
	return "nx." + shared.Token(cluster) + "." + kind + "." + shared.Token(node)
}

// FullGrants allows cluster reads but constrains executor-originated publications.
func FullGrants(cluster, node domain.ID) domain.Grants {
	prefix := "nx." + shared.Token(cluster) + "."
	return domain.Grants{
		Publish: []string{Subject(cluster, "view", node), prefix + "command.*", prefix + "query.*", prefix + "secret.*", prefix + "reply.*.>",
			Subject(cluster, "heartbeat", node), Subject(cluster, "result", node), Subject(cluster, "activity", node), Subject(cluster, "log", node) + ".*", Subject(cluster, "progress", node) + ".*", "$JS.API.>", "$JS.ACK.>", "$KV.nx_state.>", "$O.nx_git.>"},
		Subscribe: []string{prefix + ">", "_INBOX." + shared.Token(node) + ".>", "$KV.nx_state.>", "$O.nx_git.>"},
	}
}

func PendingGrants(cluster, node domain.ID) domain.Grants {
	return domain.Grants{
		Publish:   []string{Subject(cluster, "view", node), Subject(cluster, "heartbeat", node), Subject(cluster, "result", node), Subject(cluster, "activity", node), Subject(cluster, "log", node) + ".*", Subject(cluster, "progress", node) + ".*", Subject(cluster, "reply", node) + ".>"},
		Subscribe: []string{Subject(cluster, "command", node), Subject(cluster, "query", node), Subject(cluster, "reply", node) + ".>"},
	}
}

func permissions(g domain.Grants) *server.Permissions {
	return &server.Permissions{Publish: &server.SubjectPermission{Allow: g.Publish}, Subscribe: &server.SubjectPermission{Allow: g.Subscribe}}
}

type machineAuth struct {
	keys   []domain.Digest
	user   *server.User
	broker *server.Server
}

func (a machineAuth) Check(c server.ClientAuthentication) bool {
	state := c.GetTLSConnectionState()
	if state == nil || len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
		return false
	}
	key := shared.Fingerprint(state.PeerCertificates[0].RawSubjectPublicKeyInfo)
	for _, allowed := range a.keys {
		if key == allowed {
			account, err := a.broker.LookupAccount("NX")
			if err != nil {
				return false
			}
			user := *a.user
			user.Account = account
			c.RegisterUser(&user)
			return true
		}
	}
	return false
}

// PinnedTLS adds approved-key verification without disabling CA/name verification.
func PinnedTLS(base *tls.Config, keys []domain.Digest) (*tls.Config, error) {
	if base == nil || base.InsecureSkipVerify || base.MinVersion < tls.VersionTLS12 || len(base.Certificates) == 0 {
		return nil, errors.New("TLS requires a certificate, TLS 1.2+, verification (an empty approved-key set denies all peers)")
	}
	cfg := base.Clone()
	prior := cfg.VerifyConnection
	cfg.VerifyConnection = func(state tls.ConnectionState) error {
		if len(state.VerifiedChains) == 0 || len(state.PeerCertificates) == 0 {
			return errors.New("unverified TLS peer")
		}
		if prior != nil {
			if err := prior(state); err != nil {
				return err
			}
		}
		got := shared.Fingerprint(state.PeerCertificates[0].RawSubjectPublicKeyInfo)
		for _, key := range keys {
			if got == key {
				return nil
			}
		}
		return errors.New("TLS peer key is not admitted")
	}
	return cfg, nil
}

type EmbeddedBroker struct{ server *server.Server }

// BrokerOptions keeps transport setup independent of enrollment and topology policy.
type BrokerOptions struct {
	NodeID, ClusterID                                 domain.ID
	DataDir, ClientAddress, RouteAddress, LeafAddress string
	ClientTLS, RouteTLS, LeafTLS                      *tls.Config
	DaemonKeys, BrokerKeys                            []domain.Digest
	RouteCertificates                                 []domain.Digest
	LeafCertificates                                  map[domain.ID]domain.Digest // exact issued leaf certificates, binding the membership epoch.
	Leaves                                            map[domain.ID]domain.Digest // broker key; cert DNS SAN must equal SubjectToken(node).
	Routes                                            []*url.URL
	LeafRemotes                                       []*server.RemoteLeafOpts
	Pending                                           bool
	MaxStoreBytes                                     int64
}

func listenAddress(address string, loopback bool) (string, int, error) {
	host, p, err := net.SplitHostPort(address)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(p)
	if err != nil || port < 1 || port > 65535 {
		return "", 0, fmt.Errorf("invalid port in %q", address)
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() || (loopback && !ip.IsLoopback()) {
		return "", 0, errors.New("listener must use an explicit permitted IP (clients: loopback)")
	}
	return host, port, nil
}

func StartBroker(ctx context.Context, cfg BrokerOptions) (*EmbeddedBroker, error) {
	if cfg.NodeID == "" || cfg.ClusterID == "" || cfg.DataDir == "" || cfg.MaxStoreBytes <= 0 {
		return nil, errors.New("broker identity, state directory and storage bound required")
	}
	host, port, err := listenAddress(cfg.ClientAddress, true)
	if err != nil {
		return nil, err
	}
	clientTLS, err := PinnedTLS(cfg.ClientTLS, cfg.DaemonKeys)
	if err != nil {
		return nil, err
	}
	if clientTLS.ClientAuth != tls.RequireAndVerifyClientCert || clientTLS.ClientCAs == nil {
		return nil, errors.New("client listener requires mutual TLS")
	}
	opts := &server.Options{}
	accountConfig := "accounts { NX {} }"
	if !cfg.Pending {
		accountConfig = "accounts {\n NX { jetstream: enabled }\n SYS {}\n}\nsystem_account: SYS"
	}
	if err = opts.ProcessConfigString(accountConfig); err != nil {
		return nil, err
	}
	var account *server.Account
	for _, candidate := range opts.Accounts {
		if candidate.Name == "NX" {
			account = candidate
		}
	}
	grants := FullGrants(cfg.ClusterID, cfg.NodeID)
	if cfg.Pending {
		grants = PendingGrants(cfg.ClusterID, cfg.NodeID)
	}
	opts.Host, opts.Port, opts.ServerName = host, port, shared.Token(cfg.NodeID)
	opts.NoSigs, opts.NoLog = true, true
	opts.TLSConfig, opts.TLS, opts.TLSVerify, opts.TLSHandshakeFirst = clientTLS, true, true, true
	auth := &machineAuth{keys: cfg.DaemonKeys, user: &server.User{Username: shared.Token(cfg.NodeID), Account: account, Permissions: permissions(grants)}}
	opts.CustomClientAuthentication = auth
	opts.JetStream, opts.StoreDir, opts.JetStreamMaxStore = !cfg.Pending, cfg.DataDir, cfg.MaxStoreBytes
	opts.JetStreamMaxMemory, opts.MaxPayload, opts.MaxPending = 8<<20, 512<<10, 4<<20
	opts.SyncAlways = true
	opts.Routes = cfg.Routes
	if cfg.RouteAddress != "" {
		if cfg.Pending {
			return nil, errors.New("pending brokers cannot join permanent routes")
		}
		h, p, e := listenAddress(cfg.RouteAddress, false)
		if e != nil {
			return nil, e
		}
		tlsCfg, e := PinnedTLS(cfg.RouteTLS, cfg.BrokerKeys)
		if e != nil {
			return nil, e
		}
		if tlsCfg.ClientAuth != tls.RequireAndVerifyClientCert || tlsCfg.ClientCAs == nil || tlsCfg.RootCAs == nil {
			return nil, errors.New("route TLS must verify both directions")
		}
		prior := tlsCfg.VerifyConnection
		tlsCfg.VerifyConnection = func(state tls.ConnectionState) error {
			if err := prior(state); err != nil {
				return err
			}
			pin := shared.Fingerprint(state.PeerCertificates[0].Raw)
			for _, allowed := range cfg.RouteCertificates {
				if pin == allowed {
					return nil
				}
			}
			return errors.New("route certificate is not authorized for this topology")
		}
		opts.Cluster = server.ClusterOpts{Name: shared.Token(cfg.ClusterID), Host: h, Port: p, TLSConfig: tlsCfg, PoolSize: 1}
	} else if len(cfg.Routes) > 0 {
		return nil, errors.New("routes require a secured route listener")
	}
	if cfg.LeafAddress != "" {
		if cfg.Pending {
			return nil, errors.New("pending brokers cannot accept leaves")
		}
		h, p, e := listenAddress(cfg.LeafAddress, false)
		if e != nil {
			return nil, e
		}
		keys := make([]domain.Digest, 0, len(cfg.Leaves))
		for _, key := range cfg.Leaves {
			keys = append(keys, key)
		}
		tlsCfg, e := PinnedTLS(cfg.LeafTLS, keys)
		if e != nil {
			return nil, e
		}
		if tlsCfg.ClientAuth != tls.RequireAndVerifyClientCert || tlsCfg.ClientCAs == nil {
			return nil, errors.New("leaf listener requires mutual TLS")
		}
		prior := tlsCfg.VerifyConnection
		tlsCfg.VerifyConnection = func(state tls.ConnectionState) error {
			if err := prior(state); err != nil {
				return err
			}
			cert := state.PeerCertificates[0]
			for id, key := range cfg.Leaves {
				if pin, ok := cfg.LeafCertificates[id]; ok && pin != shared.Fingerprint(cert.Raw) {
					continue
				}
				if shared.Fingerprint(cert.RawSubjectPublicKeyInfo) == key && len(cert.DNSNames) == 1 && cert.DNSNames[0] == shared.Token(id) {
					return nil
				}
			}
			return errors.New("leaf certificate identity/key mismatch")
		}
		opts.LeafNode = server.LeafNodeOpts{Host: h, Port: p, TLSConfig: tlsCfg, TLSMap: true, TLSHandshakeFirst: true}
		for id := range cfg.Leaves {
			opts.LeafNode.Users = append(opts.LeafNode.Users, &server.User{Username: shared.Token(id), Account: account, Permissions: permissions(PendingGrants(cfg.ClusterID, id))})
		}
	}
	for _, remote := range cfg.LeafRemotes {
		if !cfg.Pending || remote.TLSConfig == nil || remote.TLSConfig.InsecureSkipVerify || remote.TLSConfig.VerifyConnection == nil || remote.TLSConfig.RootCAs == nil {
			return nil, errors.New("leaf remotes require pending role and pinned TLS")
		}
	}
	opts.LeafNode.Remotes = cfg.LeafRemotes
	srv, err := server.NewServer(opts)
	if err != nil {
		return nil, err
	}
	auth.broker = srv
	srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		srv.Shutdown()
		return nil, fmt.Errorf("broker listeners unavailable (client=%s route=%s leaf=%s)", cfg.ClientAddress, cfg.RouteAddress, cfg.LeafAddress)
	}
	if err = ctx.Err(); err != nil {
		srv.Shutdown()
		return nil, err
	}
	return &EmbeddedBroker{srv}, nil
}

func (b *EmbeddedBroker) RouteConnections() int { return b.server.NumRoutes() }

func (b *EmbeddedBroker) LeafConnections() int { return b.server.NumLeafNodes() }

func (b *EmbeddedBroker) Close() { b.server.Shutdown(); b.server.WaitForShutdown() }

func ConnectBroker(address string, node domain.ID, cfg *tls.Config) (*nats.Conn, error) {
	if cfg == nil || cfg.InsecureSkipVerify || cfg.MinVersion < tls.VersionTLS12 || len(cfg.Certificates) == 0 || cfg.RootCAs == nil {
		return nil, errors.New("verified client TLS required")
	}
	return nats.Connect(address, nats.Secure(cfg), nats.TLSHandshakeFirst(), nats.CustomInboxPrefix("_INBOX."+shared.Token(node)), nats.ReconnectBufSize(-1), nats.Timeout(2*time.Second), nats.MaxReconnects(-1))
}
