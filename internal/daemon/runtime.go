// Package daemon owns process lifetimes and wires application services.
package daemon

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
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
	"go.opentelemetry.io/otel"

	"nixmox/internal/api"
	"nixmox/internal/cache"
	"nixmox/internal/cluster"
	"nixmox/internal/configuration"
	"nixmox/internal/credentials"
	"nixmox/internal/deployment"
	"nixmox/internal/discovery"
	"nixmox/internal/domain"
	"nixmox/internal/enrollment"
	"nixmox/internal/host"
	"nixmox/internal/identity"
	"nixmox/internal/installer"
	"nixmox/internal/installer/installers"
	"nixmox/internal/recipe"
	"nixmox/internal/source"
	"nixmox/internal/task"
	browser "nixmox/internal/ui/views/browser"
	"nixmox/internal/util"
)

const Version = "0.1.0-dev"

type Config struct {
	DiscoveryPort      int
	Environment        string
	UIAssets           string
	StateDir           string
	Name               string
	Listen             string
	AdvertiseURL       string
	NATSHost           string
	NATSPort           int
	RoutePort          int
	BootstrapPort      int
	BootstrapRoutePort int
	SSHHostKey         string
}

type startup struct {
	Cluster         domain.Cluster       `json:"cluster"`
	Member          domain.Member        `json:"member"`
	Certificate     string               `json:"certificate"`
	NATSEndpoints   []string             `json:"natsEndpoints,omitempty"`
	SeedCredentials []credentials.Record `json:"seedCredentials,omitempty"`
}

type Runtime struct {
	discovery         *discovery.Service
	config            Config
	node              *identity.Node
	lifetime          context.Context
	cancel            context.CancelFunc
	mu                sync.RWMutex
	transition        sync.Mutex
	services          *api.Services
	ui                http.Handler
	client            *nats.Conn
	servers           []*server.Server
	serverOptions     []*server.Options
	authorization     sync.Mutex
	authorizedMembers string
	gateway           *cache.Gateway
	workers           sync.WaitGroup
	failures          chan error
	status            string
	started           time.Time
}

// Open starts only a private identity for an unjoined node; it does not launch
// NATS until cluster creation or restoration of saved cluster startup state.
func Open(ctx context.Context, config Config) (*Runtime, error) {
	if config.StateDir == "" {
		return nil, errors.New("state directory required")
	}
	if config.Environment == "" {
		config.Environment = "installed"
	}
	if config.Environment != "installed" && config.Environment != "installer" {
		return nil, errors.New("environment must be installed or installer")
	}
	node, err := identity.Open(config.StateDir, config.Name)
	if err != nil {
		return nil, err
	}
	if node.State.Environment != config.Environment {
		node.State.Environment = config.Environment
		if err = node.Save(); err != nil {
			node.Close()
			return nil, err
		}
	}
	lifetime, cancel := context.WithCancel(ctx)
	r := &Runtime{config: config, node: node, lifetime: lifetime, cancel: cancel, failures: make(chan error, 1), status: "online", started: time.Now().UTC()}
	assets := config.UIAssets
	if assets == "" {
		if executable, exeErr := os.Executable(); exeErr == nil {
			candidate := filepath.Join(filepath.Dir(executable), "../share/nixmox-ui")
			if _, statErr := os.Stat(filepath.Join(candidate, "app.wasm")); statErr == nil {
				assets = candidate
			}
		}
	}
	if assets != "" {
		r.ui, err = browser.Handler(assets)
		if err != nil {
			r.Close()
			return nil, err
		}
	}
	data, err := os.ReadFile(filepath.Join(config.StateDir, "cluster.json"))
	if err == nil {
		var saved startup
		if err = json.Unmarshal(data, &saved); err == nil {
			saved.Member.Environment = config.Environment
			err = r.start(saved)
		}
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		r.Close()
		return nil, err
	}
	if err = r.startDiscovery(); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

func (r *Runtime) Handler() http.Handler {
	return (api.Server{InviteEnrollment: r.inviteEnrollment, ConfirmEnrollment: r.confirmEnrollment, Discovery: r.discovery, Health: r.Health, CreateCluster: r.CreateCluster, Services: r.Services, UI: r.ui}).Handler()
}

func (r *Runtime) Services() (*api.Services, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.services == nil || r.client == nil || !r.client.IsConnected() {
		return nil, errors.New("cluster disconnected")
	}
	return r.services, nil
}

func (r *Runtime) Health(context.Context) domain.Health {
	r.mu.RLock()
	defer r.mu.RUnlock()
	health := domain.Health{NodeID: r.node.State.NodeID, Status: r.status, CheckedAt: time.Now().UTC(), Checks: map[string]domain.Check{"natsClient": {Status: "not-applicable"}, "natsServer": {Status: "not-applicable"}, "cluster": {Status: "not-applicable"}}}
	if r.client != nil {
		status := "healthy"
		if !r.client.IsConnected() {
			status = "unhealthy"
			health.Status = "disconnected"
		}
		health.Checks["natsClient"] = domain.Check{Status: status}
		health.Checks["cluster"] = domain.Check{Status: status}
	}
	if len(r.servers) > 0 {
		status := "healthy"
		for _, s := range r.servers {
			if !s.Running() {
				status = "unhealthy"
			}
		}
		health.Checks["natsServer"] = domain.Check{Status: status}
	}
	return health
}

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
		return saved.Cluster, r.start(saved)
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
		Cluster:         domain.Cluster{ID: clusterID, Name: name, CreatedByNodeID: r.node.State.NodeID, CA: domain.ClusterCA{Certificate: string(authority.PEM), CredentialID: "cluster-ca"}},
		Member:          domain.Member{NodeID: r.node.State.NodeID, Name: r.node.State.Name, ClusterID: clusterID, Status: "full-stable", Environment: r.node.State.Environment, AgePublicKey: publicAge},
		Certificate:     string(certificate),
		SeedCredentials: []credentials.Record{{ID: "cluster-ca", Name: "Cluster certificate authority", Type: "cluster-ca", Content: caDocument}, {ID: "nix-signing-key", Name: "Nix cache signing key", Type: "nix-signing", Content: cacheDocument}},
	}
	if err = r.saveStartup(saved); err != nil {
		return domain.Cluster{}, err
	}
	// After committing bootstrap input, startup has a daemon-owned lifetime.
	return saved.Cluster, r.start(saved)
}

func (r *Runtime) saveStartup(saved startup) error {
	data, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	return identity.WritePrivate(filepath.Join(r.config.StateDir, "cluster.json"), data)
}

func (r *Runtime) start(saved startup) (err error) {
	if saved.Member.Status == "pending" {
		return r.startPending(saved)
	}
	if saved.Member.Status != "full-stable" {
		return errors.New("unsupported startup membership role")
	}
	ctx, cancel := context.WithTimeout(r.lifetime, 30*time.Second)
	defer cancel()
	r.mu.Lock()
	r.status = "starting"
	r.mu.Unlock()
	var servers []*server.Server
	var serverOptions []*server.Options
	var nc *nats.Conn
	var gateway *cache.Gateway
	success := false
	defer func() {
		if !success {
			if gateway != nil {
				gateway.Close()
			}
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
	key, err := os.ReadFile(filepath.Join(r.config.StateDir, "node.key"))
	if err != nil {
		return err
	}
	defer clear(key)
	tlsConfig, err := cluster.TLSConfig([]byte(saved.Certificate), key, []byte(saved.Cluster.CA.Certificate))
	if err != nil {
		return err
	}
	if err = r.node.VerifyEnrollment([]byte(saved.Certificate), []byte(saved.Cluster.CA.Certificate), time.Now()); err != nil {
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
	options := server.Options{
		ServerName: r.node.State.NodeID,
		Host:       r.config.NATSHost,
		Port:       r.config.NATSPort,
		JetStream:  true,
		StoreDir:   filepath.Join(r.config.StateDir, "nats", "main"),
		TLSConfig:  tlsConfig,
		TLSVerify:  true,
		TLSMap:     true,
		Users:      []*server.User{{Username: "CN=" + r.node.State.NodeID}},
		Routes:     []*url.URL{bootstrapRoute},
		Cluster: server.ClusterOpts{
			Name:      saved.Cluster.ID,
			Host:      r.config.NATSHost,
			Port:      ports[0],
			TLSConfig: tlsConfig.Clone(),
		},
	}
	if err = configureAccounts(&options, r.node.State.NodeID); err != nil {
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
	for _, bucket := range buckets {
		if _, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket, Storage: jetstream.FileStorage, Replicas: 2}); err != nil {
			return fmt.Errorf("provision bucket %s: %w", bucket, err)
		}
	}
	if _, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "DISCOVERY", Storage: jetstream.FileStorage, Replicas: 2, TTL: discovery.Lifetime, MaxBytes: 4 << 20, MaxValueSize: 16 << 10}); err != nil {
		return fmt.Errorf("provision discovery: %w", err)
	}
	discoveryStore, err := cluster.OpenStore[discovery.Record](ctx, js, "DISCOVERY")
	if err != nil {
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
	enrollments, err := cluster.OpenStore[enrollment.Request](ctx, js, "ENROLLMENT")
	if err != nil {
		return err
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
	for _, credential := range saved.SeedCredentials {
		if _, err = credentialStore.Create(ctx, credential.ID, credential); err != nil && !errors.Is(err, jetstream.ErrKeyExists) {
			return err
		}
	}
	if _, err = recipients.Create(ctx, "recipients", credentials.Recipients{ClusterID: saved.Cluster.ID, Recipients: []credentials.Recipient{{NodeID: saved.Member.NodeID, AgePublicKey: saved.Member.AgePublicKey}}}); err != nil && !errors.Is(err, jetstream.ErrKeyExists) {
		return err
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
		return err
	}
	tasks, err := cluster.OpenStore[domain.Task](ctx, js, "TASKS")
	if err != nil {
		return fmt.Errorf("open tasks: %w", err)
	}
	outputs, err := cluster.OpenStore[domain.TaskOutput](ctx, js, "OUTPUTS")
	if err != nil {
		return fmt.Errorf("open outputs: %w", err)
	}
	locks, err := cluster.OpenStore[map[string]string](ctx, js, "LOCKS")
	if err != nil {
		return fmt.Errorf("open locks: %w", err)
	}
	repos, err := cluster.OpenStore[source.Repository](ctx, js, "REPOS")
	if err != nil {
		return fmt.Errorf("open repos: %w", err)
	}
	configurations, err := cluster.OpenStore[configuration.Version](ctx, js, "CONFIGURATIONS")
	if err != nil {
		return fmt.Errorf("open configurations: %w", err)
	}
	recipes, err := cluster.OpenStore[recipe.Version](ctx, js, "RECIPES")
	if err != nil {
		return fmt.Errorf("open recipes: %w", err)
	}
	installerDefinitions, err := cluster.OpenStore[installers.Definition](ctx, js, "INSTALLERS")
	if err != nil {
		return fmt.Errorf("open installers: %w", err)
	}
	logs := cluster.Logs{JS: js}
	if err = logs.Provision(ctx, 2); err != nil {
		return err
	}
	signing, err := credentialStore.Get(ctx, "nix-signing-key")
	if err != nil {
		return err
	}
	secret, err := (credentials.Cipher{KeyFile: filepath.Join(r.config.StateDir, "age.key")}).Decrypt(ctx, signing.Value.Content)
	if err != nil {
		return err
	}
	defer clear(secret)
	local, err := cache.NewLocal(string(secret), host.Process{})
	if err != nil {
		return err
	}
	gateway = cache.NewGateway()
	if r.config.AdvertiseURL != "" {
		if err = gateway.ReplacePeers([]cache.Peer{{NodeID: member.NodeID, StoreURL: r.config.AdvertiseURL + "/store-cache"}}); err != nil {
			return err
		}
	}
	sources := source.Manager{Root: filepath.Join(r.config.StateDir, "worktrees")}
	builder := host.Builder{
		Sources: sources,
		Repository: func(ctx context.Context, id string) (source.Repository, error) {
			record, err := repos.Get(ctx, id)
			return record.Value, err
		},
	}
	manager := &task.Manager{
		HostID:    member.NodeID,
		Tasks:     tasks,
		Outputs:   outputs,
		Logs:      logs,
		Resources: task.Resources{Store: locks},
		Runners:   map[string]task.Runner{"build": builder.Runner},
	}

	installerService := &installer.Service{
		HostID: member.NodeID, Definitions: installerDefinitions, Tasks: manager,
		Repository: builder.Repository,
		Inspect: func(ctx context.Context) (installers.Target, error) {
			return installer.InspectLocal(ctx, member.NodeID, saved.Member.Environment)
		},
	}
	manager.Runners[installer.LaunchTask] = installerService.Runner
	manager.Runners[installer.FinalizeTask] = installerService.FinalizeRunner

	recipeService := &recipe.Service{
		Repository:   recipe.Repository{Records: recipes},
		Sources:      sources,
		Repositories: repos,
	}
	collector := &configuration.Collector{
		HostDisks: func(ctx context.Context, nodeID string) (any, error) {
			if _, err := members.Get(ctx, nodeID); err != nil {
				return nil, err
			}
			var output json.RawMessage
			err := host.Observe(ctx, nc, nodeID, host.ObservationRequest{Kind: "disks"}, &output)
			return output, err
		},
		HostHardware: func(ctx context.Context, nodeID string, noFileSystems bool) (string, error) {
			if _, err := members.Get(ctx, nodeID); err != nil {
				return "", err
			}
			var output string
			err := host.Observe(ctx, nc, nodeID, host.ObservationRequest{Kind: "hardware", NoFileSystems: noFileSystems}, &output)
			return output, err
		},
		Members:      members,
		Repositories: repos,
		Sources:      sources,
		CredentialIDs: func(ctx context.Context) ([]string, error) {
			return credentialStore.KV.Keys(ctx)
		},
		HostAgeKey: func(ctx context.Context, id string) (string, error) {
			record, err := members.Get(ctx, id)
			return record.Value.AgePublicKey, err
		},
	}
	configurationService := &configuration.Service{
		Repository:   configuration.Repository{Records: configurations},
		Recipes:      recipeService.Repository,
		Observer:     collector,
		Sources:      sources,
		Repositories: repos,
	}
	registry := &configuration.Registry{
		Sources:        sources,
		Repositories:   repos,
		Configurations: configurationService,
		Run:            (host.Process{}).Run,
	}

	deployments := &deployment.Service{HostID: member.NodeID, Configurations: configurationService, Members: members, Tasks: manager, Builder: builder}
	manager.Runners["deployment"] = deployments.Runner
	manager.Runners["configuration-build"] = deployments.Build
	metrics := cluster.Metrics{Connection: nc}
	enrollmentService := &enrollment.Service{NodeID: member.NodeID, Requests: enrollments, Members: members, Clusters: clusterStore, Discovery: discoveryStore, Issue: r.issueEnrollment, Authorize: func(ctx context.Context) error { return r.refreshAuthorization(ctx, members) }}
	svc := &api.Services{
		HostDisks:      collector.HostDisks,
		Enrollment:     enrollmentService,
		Discovery:      discoveryStore,
		Metrics:        metrics,
		Deployments:    deployments,
		Credentials:    credentialStore,
		CachePublicKey: local.PublicKey(),
		Cluster:        clusterStore,
		Members:        members,
		Repositories:   repos,
		Sources:        sources,
		Configurations: configurationService,
		Registry:       registry,
		Recipes:        recipeService,
		Installers:     installerService,
		Tasks:          manager,
		Logs:           logs,
		Cache:          gateway,
		StoreCache:     local,
	}
	r.mu.Lock()
	r.node.State.ClusterID = saved.Cluster.ID
	r.node.State.Status = "full-stable"
	err = r.node.Save()
	r.mu.Unlock()
	if err != nil {
		return err
	}
	saved.SeedCredentials = nil
	if err = r.saveStartup(saved); err != nil {
		return err
	}
	r.mu.Lock()
	r.client = nc
	r.servers = servers
	r.serverOptions = serverOptions
	r.gateway = gateway
	r.services = svc
	r.status = "online"
	r.mu.Unlock()
	r.authorization.Lock()
	r.authorizedMembers = authorizationKey([]domain.Member{{NodeID: member.NodeID, Status: "full-stable"}})
	r.authorization.Unlock()
	if err = r.refreshAuthorization(ctx, members); err != nil {
		return err
	}
	if err = r.startObservations(nc, member.NodeID); err != nil {
		return err
	}
	success = true
	r.workers.Go(func() {
		err := members.Watch(r.lifetime, ">", func(_ *cluster.Record[domain.Member], _ bool) error {
			return r.refreshAuthorization(r.lifetime, members)
		})
		if err != nil && r.lifetime.Err() == nil {
			select {
			case r.failures <- err:
			default:
			}
		}
	})
	r.workers.Go(func() {
		if err := enrollmentService.Run(r.lifetime); err != nil && r.lifetime.Err() == nil {
			select {
			case r.failures <- err:
			default:
			}
		}
	})
	r.workers.Go(func() { host.Metrics(r.lifetime, member.NodeID, metrics) })
	r.workers.Go(func() {
		if err := manager.Run(r.lifetime); err != nil && r.lifetime.Err() == nil {
			select {
			case r.failures <- err:
			default:
			}
		}
	})
	return nil
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

func (r *Runtime) Close() error {
	r.cancel()
	r.transition.Lock()
	defer r.transition.Unlock()
	r.workers.Wait()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status = "shutting down"
	r.services = nil
	if r.gateway != nil {
		r.gateway.Close()
	}
	if r.client != nil {
		r.client.Close()
	}
	for _, s := range r.servers {
		s.Shutdown()
		s.WaitForShutdown()
	}
	r.servers = nil
	return r.node.Close()
}

func Run(ctx context.Context, config Config) error {
	runtime, err := Open(ctx, config)
	if err != nil {
		return err
	}
	defer runtime.Close()
	metrics, err := host.ObserveMetrics(otel.Meter("nixmox.host"))
	if err != nil {
		return err
	}
	defer metrics.Unregister()
	listener, err := net.Listen("tcp", config.Listen)
	if err != nil {
		return err
	}
	httpServer := &http.Server{Handler: runtime.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 32 << 10}
	done := make(chan error, 1)
	go func() { done <- httpServer.Serve(listener) }()
	util.From(ctx).Info("daemon listening", "node_id", runtime.Health(ctx).NodeID, "environment", runtime.config.Environment, "listen", listener.Addr().String())
	select {
	case <-ctx.Done():
		err = nil
	case err = <-runtime.failures:
	case err = <-done:
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if stopErr := httpServer.Shutdown(shutdown); stopErr != nil {
		httpServer.Close()
		err = errors.Join(err, stopErr)
	}
	return err
}
