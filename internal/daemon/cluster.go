package daemon

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nix-community/go-nix/pkg/narinfo/signature"

	"nixmox/internal/cluster"
	"nixmox/internal/credentials"
	"nixmox/internal/discovery"
	"nixmox/internal/domain"
	"nixmox/internal/identity"
)

func (r *Runtime) CreateCluster(ctx context.Context, name string) (domain.Cluster, error) {
	r.transition.Lock()
	defer r.transition.Unlock()
	if strings.TrimSpace(name) == "" || len(name) > 128 {
		return domain.Cluster{}, errors.New("cluster name must contain 1 to 128 characters")
	}
	r.mu.RLock()
	joined := r.node.State.ClusterID != ""
	r.mu.RUnlock()
	if joined {
		return domain.Cluster{}, errors.New("only an unjoined node can create a cluster")
	}
	// A failed creation is resumed, never silently replaced with a new identity.
	if data, err := os.ReadFile(filepath.Join(r.config.StateDir, "cluster.json")); err == nil {
		var saved startup
		if err = json.Unmarshal(data, &saved); err != nil {
			return domain.Cluster{}, err
		}
		if saved.Cluster.Name != name {
			return domain.Cluster{}, errors.New("unfinished cluster creation has a different name")
		}
		return saved.Cluster, r.startCluster(saved)
	} else if !errors.Is(err, os.ErrNotExist) {
		return domain.Cluster{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	publicAge, err := credentials.DeriveIdentity(ctx, r.config.SSHHostKey, filepath.Join(r.config.StateDir, "age.key"))
	if err != nil {
		return domain.Cluster{}, err
	}
	clusterID := uuid.NewString()
	authority, err := identity.NewAuthority(clusterID, time.Now())
	if err != nil {
		return domain.Cluster{}, err
	}
	csr, err := r.node.CSR()
	if err != nil {
		return domain.Cluster{}, err
	}
	fingerprint, err := identity.Thumbprint(r.node.Key.Public())
	if err != nil {
		return domain.Cluster{}, err
	}
	names := []string{"localhost", "127.0.0.1", r.node.State.Name}
	if endpoint, err := url.Parse(r.config.AdvertiseURL); err == nil && endpoint.Hostname() != "" {
		names = append(names, endpoint.Hostname())
	}
	if r.config.NATSHost != "" && r.config.NATSHost != "0.0.0.0" && r.config.NATSHost != "::" {
		names = append(names, r.config.NATSHost)
	}
	certificate, err := authority.Issue(csr, r.node.State.NodeID, fingerprint, names, time.Now())
	if err != nil {
		return domain.Cluster{}, err
	}
	caKey, err := x509.MarshalPKCS8PrivateKey(authority.Key)
	if err != nil {
		return domain.Cluster{}, err
	}
	defer clear(caKey)
	caSecret := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: caKey})
	defer clear(caSecret)
	cacheKey, _, err := signature.GenerateKeypair("nixmox-"+clusterID, nil)
	if err != nil {
		return domain.Cluster{}, err
	}
	recipients := []credentials.Recipient{{NodeID: r.node.State.NodeID, AgePublicKey: publicAge}}
	cipher := credentials.Cipher{KeyFile: filepath.Join(r.config.StateDir, "age.key")}
	caDocument, err := cipher.Encrypt(ctx, caSecret, recipients)
	if err != nil {
		return domain.Cluster{}, err
	}
	cacheDocument, err := cipher.Encrypt(ctx, []byte(cacheKey.String()), recipients)
	if err != nil {
		return domain.Cluster{}, err
	}
	saved := startup{
		Cluster:            domain.Cluster{ID: clusterID, Name: name, CreatedByNodeID: r.node.State.NodeID, CA: domain.ClusterCA{Certificate: string(authority.PEM), CredentialID: "cluster-ca"}},
		Member:             domain.Member{NodeID: r.node.State.NodeID, Name: r.node.State.Name, ClusterID: clusterID, Status: "full-stable", Environment: r.node.State.Environment, AgePublicKey: publicAge},
		Certificate:        string(certificate),
		InitialCredentials: []credentials.Record{{ID: "cluster-ca", Name: "Cluster certificate authority", Type: "cluster-ca", Content: caDocument}, {ID: "nix-signing-key", Name: "Nix cache signing key", Type: "nix-signing", Content: cacheDocument}},
	}
	if err = r.saveStartup(saved); err != nil {
		return domain.Cluster{}, err
	}
	// After committing bootstrap input, startup has a daemon-owned lifetime.
	return saved.Cluster, r.startCluster(saved)
}

func (r *Runtime) startBootstrap(saved startup) (err error) {
	ctx, cancel := context.WithTimeout(r.lifetime, 30*time.Second)
	defer cancel()
	r.mu.Lock()
	r.status = "starting"
	r.mu.Unlock()
	var servers []*server.Server
	var serverOptions []*server.Options
	var nc *nats.Conn
	success := false
	defer func() {
		if !success {
			if nc != nil {
				nc.Close()
			}
			for _, s := range servers {
				s.Shutdown()
				s.WaitForShutdown()
			}
			r.mu.Lock()
			r.status = "disconnected"
			r.mu.Unlock()
		}
	}()
	tlsConfig, err := r.nodeTLS(saved)
	if err != nil {
		return err
	}
	ports, err := routePorts(r.config.NATSHost, r.config.RoutePort, r.config.BootstrapRoutePort)
	if err != nil {
		return err
	}
	routeHostName := r.config.NATSHost
	if routeHostName == "" || routeHostName == "0.0.0.0" || routeHostName == "::" {
		routeHostName = "127.0.0.1"
	}
	bootstrapRoute, err := url.Parse("nats-route://" + net.JoinHostPort(routeHostName, strconvPort(ports[1])))
	if err != nil {
		return err
	}
	options, err := r.peerOptions(saved, tlsConfig, []*url.URL{bootstrapRoute}, ports[0], []domain.Member{{NodeID: r.node.State.NodeID, Status: "full-stable"}})
	if err != nil {
		return err
	}
	serverOptions = append(serverOptions, options.Clone())
	main, err := cluster.NewServer(options)
	if err != nil {
		return err
	}
	routeURL, err := url.Parse("nats-route://" + net.JoinHostPort(routeHostName, strconvPort(ports[0])))
	if err != nil {
		return err
	}
	options.ServerName = "bootstrap-" + saved.Cluster.ID
	options.Port = r.config.BootstrapPort
	options.Cluster.Port = ports[1]
	options.StoreDir = filepath.Join(r.config.StateDir, "nats", "bootstrap")
	options.Routes = []*url.URL{routeURL}
	if err = configureAccounts(&options, r.node.State.NodeID); err != nil {
		return err
	}
	serverOptions = append(serverOptions, options.Clone())
	bootstrap, err := cluster.NewServer(options)
	if err != nil {
		return err
	}
	servers = []*server.Server{main, bootstrap}
	// Both route addresses are known before startup. Launch both peers before
	// waiting for either listener, including when restoring persistent stores.
	var starting sync.WaitGroup
	for _, s := range servers {
		starting.Go(s.Start)
	}
	starting.Wait()
	for _, s := range servers {
		if !s.ReadyForConnections(10 * time.Second) {
			return fmt.Errorf("NATS server %s failed listener readiness", s.Name())
		}
	}
	clientURL, err := url.Parse(main.ClientURL())
	if err != nil {
		return err
	}
	if clientURL.Hostname() == "0.0.0.0" || clientURL.Hostname() == "::" {
		clientURL.Host = net.JoinHostPort("127.0.0.1", clientURL.Port())
	}
	nc, err = cluster.Connect([]string{clientURL.String()}, tlsConfig, r.node.State.NodeID)
	if err != nil {
		return err
	}
	js, err := jetstream.New(nc)
	if err != nil {
		return err
	}
	// Initial metadata election is a bounded readiness probe, not a daemon poller.
	for {
		probe, cancelProbe := context.WithTimeout(ctx, time.Second)
		_, err = js.AccountInfo(probe)
		cancelProbe()
		if err == nil {
			break
		}
		if errors.Is(err, jetstream.ErrJetStreamNotEnabledForAccount) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("JetStream readiness: %w (last probe: %v)", ctx.Err(), err)
		case <-time.After(100 * time.Millisecond):
		}
	}
	buckets := []string{"CLUSTER", "MEMBERS", "CREDENTIALS", "RECIPIENTS", "TASKS", "OUTPUTS", "LOCKS", "REPOS", "CONFIGURATIONS", "RECIPES", "INSTALLERS", "ENROLLMENT"}
	configs := make([]jetstream.KeyValueConfig, 0, len(buckets)+1)
	for _, bucket := range buckets {
		configs = append(configs, jetstream.KeyValueConfig{Bucket: bucket, Storage: jetstream.FileStorage, Replicas: 2})
	}
	configs = append(configs, jetstream.KeyValueConfig{Bucket: "DISCOVERY", Storage: jetstream.FileStorage, Replicas: 2, TTL: discovery.Lifetime, MaxBytes: 4 << 20, MaxValueSize: 16 << 10})
	for _, config := range configs {
		// A cold start must not reset a topology transition's committed replication.
		_, err = cluster.OpenStore[json.RawMessage](ctx, js, config.Bucket)
		if err == nil {
			continue
		}
		if !errors.Is(err, jetstream.ErrBucketNotFound) {
			return fmt.Errorf("restore bucket %s: %w", config.Bucket, err)
		}
		if _, err = js.CreateKeyValue(ctx, config); err != nil {
			return fmt.Errorf("provision bucket %s: %w", config.Bucket, err)
		}
	}
	streams := js.StreamNames(ctx)
	for name := range streams.Name() {
		stream, err := js.Stream(ctx, name)
		if err != nil {
			return fmt.Errorf("restore stream %s: %w", name, err)
		}
		if err = cluster.WaitStreamReady(ctx, stream, 0); err != nil {
			return fmt.Errorf("restore stream quorum %s: %w", name, err)
		}
	}
	if err = streams.Err(); err != nil {
		return err
	}
	clusterStore, err := cluster.OpenStore[domain.Cluster](ctx, js, "CLUSTER")
	if err != nil {
		return fmt.Errorf("open clusterStore: %w", err)
	}
	members, err := cluster.OpenStore[domain.Member](ctx, js, "MEMBERS")
	if err != nil {
		return fmt.Errorf("open members: %w", err)
	}
	credentialStore, err := cluster.OpenStore[credentials.Record](ctx, js, "CREDENTIALS")
	if err != nil {
		return fmt.Errorf("open credentialStore: %w", err)
	}
	recipients, err := cluster.OpenStore[credentials.Recipients](ctx, js, "RECIPIENTS")
	if err != nil {
		return fmt.Errorf("open recipients: %w", err)
	}
	if _, err = clusterStore.Create(ctx, "identity", saved.Cluster); err != nil && !errors.Is(err, jetstream.ErrKeyExists) {
		return fmt.Errorf("seed cluster identity: %w", err)
	}
	for _, credential := range saved.InitialCredentials {
		if _, err = credentialStore.Create(ctx, credential.ID, credential); err != nil && !errors.Is(err, jetstream.ErrKeyExists) {
			return err
		}
	}
	if _, err = recipients.Create(ctx, "recipients", credentials.Recipients{ClusterID: saved.Cluster.ID, Recipients: []credentials.Recipient{{NodeID: saved.Member.NodeID, AgePublicKey: saved.Member.AgePublicKey}}}); err != nil && !errors.Is(err, jetstream.ErrKeyExists) {
		return fmt.Errorf("seed recipients: %w", err)
	}
	publicClientURL, publicRouteURL := *clientURL, *routeURL
	if endpoint, parseErr := url.Parse(r.config.AdvertiseURL); parseErr == nil && endpoint.Hostname() != "" {
		publicClientURL.Host = net.JoinHostPort(endpoint.Hostname(), clientURL.Port())
		publicRouteURL.Host = net.JoinHostPort(endpoint.Hostname(), routeURL.Port())
	}
	member := saved.Member
	member.Daemon = domain.DaemonInfo{Version: Version, StartedAt: r.started}
	member.Endpoints = []domain.Endpoint{{Network: "lan", HTTP: r.config.AdvertiseURL, NATSClient: publicClientURL.String(), NATSRoute: publicRouteURL.String()}}
	current, err := members.Get(ctx, member.NodeID)
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		_, err = members.Create(ctx, member.NodeID, member)
	} else if err == nil {
		// Never overwrite membership decisions from stale startup data.
		if current.Value.Status != "full-stable" {
			return errors.New("saved server role differs from shared membership; transition recovery required")
		}
		member = current.Value
		member.Daemon = domain.DaemonInfo{Version: Version, StartedAt: r.started}
		member.Endpoints = []domain.Endpoint{{Network: "lan", HTTP: r.config.AdvertiseURL, NATSClient: publicClientURL.String(), NATSRoute: publicRouteURL.String()}}
		_, err = members.Update(ctx, member.NodeID, member, current.Revision)
	}
	if err != nil {
		return fmt.Errorf("publish startup member: %w", err)
	}
	if err = (cluster.Logs{JS: js}).Provision(ctx, 2); err != nil {
		return fmt.Errorf("restore logs: %w", err)
	}
	saved.Member = member
	saved.InitialCredentials = nil
	r.mu.Lock()
	r.servers, r.serverOptions = servers, serverOptions
	r.mu.Unlock()
	if err = r.activate(ctx, saved, nc, js); err != nil {
		return err
	}
	success = true
	return nil
}
