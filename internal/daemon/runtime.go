// Package daemon owns process lifetimes and wires application services.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"

	"nixmox/internal/api"
	"nixmox/internal/cache"
	"nixmox/internal/discovery"
	"nixmox/internal/domain"
	"nixmox/internal/host"
	"nixmox/internal/identity"
	browser "nixmox/internal/ui/views/browser"
	"nixmox/internal/util"
)

const Version = "0.1.0-dev"

// Config contains operator settings, not persisted identity or membership.
// Viper owns file/environment/flag precedence; Open owns runtime validation.
type Config struct {
	DiscoveryPort      int    `mapstructure:"discovery-port"`
	Environment        string `mapstructure:"environment"`
	UIAssets           string `mapstructure:"ui-assets"`
	StateDir           string `mapstructure:"state-dir"`
	Name               string `mapstructure:"name"`
	Listen             string `mapstructure:"listen"`
	AdvertiseURL       string `mapstructure:"advertise"`
	NATSHost           string `mapstructure:"nats-host"`
	NATSPort           int    `mapstructure:"nats-port"`
	RoutePort          int    `mapstructure:"route-port"`
	BootstrapPort      int    `mapstructure:"bootstrap-port"`
	BootstrapRoutePort int    `mapstructure:"bootstrap-route-port"`
	SSHHostKey         string `mapstructure:"ssh-host-key"`
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
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		r.Close()
		return nil, err
	}
	if errors.Is(err, os.ErrNotExist) && node.State.ClusterID != "" {
		r.Close()
		return nil, errors.New("joined node is missing its saved cluster state; recovery required")
	}
	if err == nil {
		var saved startup
		if err = json.Unmarshal(data, &saved); err == nil {
			saved.Member.Environment = config.Environment
			err = r.startCluster(saved)
		}
		// Missing files during restoration are errors, not permission to treat
		// an enrolled host as new or apply a different cluster seed.
		if err != nil {
			r.Close()
			return nil, err
		}
	}
	if err = r.startDiscovery(); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

func (r *Runtime) Handler() http.Handler {
	return (api.Server{RequestEnrollment: r.requestEnrollment, EnrollmentStatus: r.enrollmentStatus, InviteEnrollment: r.inviteEnrollment, ConfirmEnrollment: r.confirmEnrollment, Discovery: r.discovery, Health: r.Health, CreateCluster: r.CreateCluster, Services: r.Services, UI: r.ui}).Handler()
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

var errRoleRestart = errors.New("membership role requires daemon restart")

func Run(ctx context.Context, config Config) error {
	for {
		err := runOnce(ctx, config)
		if !errors.Is(err, errRoleRestart) || ctx.Err() != nil {
			return err
		}
	}
}

func runOnce(ctx context.Context, config Config) error {
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
