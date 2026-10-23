package comms

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/jsm.go"
	"github.com/nats-io/jsm.go/api"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"nixmox/internal/domain"
	"nixmox/internal/engine/internal/shared"
)

func validateRoutes(cfg BrokerOptions) error {
	if cfg.RouteAddress == "" {
		if len(cfg.Routes) != 0 {
			return errors.New("routes require a secured listener")
		}
		return nil
	}
	real := false
	for _, route := range cfg.Routes {
		if route == nil || route.User != nil || route.Hostname() == "" || route.Port() == "" || route.Path != "" || route.RawQuery != "" || route.Fragment != "" {
			return errors.New("invalid route URL")
		}
		if route.Scheme != "nats" && route.Scheme != "nats-route" && route.Scheme != "tls" {
			return errors.New("invalid route scheme")
		}
		if route.Host != cfg.RouteAddress {
			real = true
		}
	}
	if !real {
		return errors.New("clustered JetStream requires a real peer route; a singleton must stay standalone")
	}
	return nil
}
func maintenanceGrants(grants domain.Grants, cluster domain.ID) domain.Grants {
	prefix := "nx." + shared.Token(cluster) + "."
	allowed := []string{}
	for _, subject := range grants.Publish {
		if !strings.HasPrefix(subject, "$") && !strings.HasPrefix(subject, prefix+"command.") && !strings.HasPrefix(subject, prefix+"result.") {
			allowed = append(allowed, subject)
		}
	}
	grants.Publish = allowed
	return grants
}

// admin creates a dedicated cancellable connection. jsm's final restore RPC can
// wait up to an hour internally; closing this connection also interrupts that RPC.
func (b *EmbeddedBroker) admin(ctx context.Context, node domain.ID, certificate *tls.Config, system bool) (*nats.Conn, *jsm.Manager, func(), error) {
	user := "nixmox-maintenance"
	if system {
		user = "nixmox-system"
	}
	nc, err := connectBroker(b.server.ClientURL(), node, certificate, nats.UserInfo(user, b.secret))
	if err != nil {
		return nil, nil, nil, err
	}
	stop := context.AfterFunc(ctx, nc.Close)
	close := func() { stop(); nc.Close() }
	manager, err := jsm.New(nc, jsm.WithTimeout(2*time.Second))
	if err != nil {
		close()
		return nil, nil, nil, err
	}
	return nc, manager, close, nil
}

// Diagnostics remains local and available without quorum. Observations are not
// authorization to write; NATS acknowledgements still establish committed changes.
func (b *EmbeddedBroker) Diagnostics() (*server.JSInfo, error) {
	return b.server.Jsz(&server.JSzOptions{Accounts: true, Streams: true, Consumer: true, Config: true, RaftGroups: true})
}

// ReloadRoutes is only for approved changes within an existing routed topology.
// The zero-peer transition requires a store handoff, not a reload or auto-shrink.
func (b *EmbeddedBroker) ReloadRoutes(cfg BrokerOptions) error {
	if b.options.Cluster.Name == "" {
		return errors.New("standalone to routed requires a store transition")
	}
	if shared.Token(cfg.NodeID) != b.options.ServerName || shared.Token(cfg.ClusterID) != b.options.Cluster.Name || cfg.RouteAddress != net.JoinHostPort(b.options.Cluster.Host, strconv.Itoa(b.options.Cluster.Port)) || cfg.Pending {
		return errors.New("route reload cannot change broker identity, mode or listeners")
	}
	if err := validateRoutes(cfg); err != nil {
		return err
	}
	certificate, err := routeTLS(cfg)
	if err != nil {
		return err
	}
	options := *b.options
	options.Routes = append([]*url.URL(nil), cfg.Routes...)
	options.Cluster.TLSConfig = certificate
	if err := b.server.ReloadOptions(&options); err != nil {
		return err
	}
	b.options = &options
	return nil
}

// RemovePeer orders replica changes, asset evacuation, leader stepdown and
// committed metadata removal. Keep the target connected until this returns,
// then revoke its grants/remove routes. Membership policy persists the intent.
// A missing quorum never becomes permission to force a local topology reset.
func (r *Runtime) RemovePeer(ctx context.Context, node domain.ID, remaining int) error {
	if node == "" || !r.Broker.server.JetStreamIsClustered() {
		return errors.New("peer removal requires a routed group and target")
	}
	if err := r.PrepareDeparture(ctx, node, remaining); err != nil {
		return err
	}
	_, manager, close, err := r.Broker.admin(ctx, r.options.NodeID, r.clientTLS, true)
	if err != nil {
		return err
	}
	defer close()
	name := shared.Token(node)
	return await(ctx, func() error {
		info, err := r.Broker.Diagnostics()
		if err != nil {
			return err
		}
		if info.Meta != nil && info.Meta.Leader == name {
			if err = manager.MetaLeaderStandDown(nil); err != nil {
				return err
			}
			return errNotReady
		}
		err = manager.MetaPeerRemove(name, "")
		var apiErr *api.ApiError
		if errors.As(err, &apiErr) {
			switch server.ErrorIdentifier(apiErr.ErrCode) {
			case server.JSClusterServerNotMemberErr:
				return nil
			case server.JSClusterServerMemberChangeInflightErr:
				return errNotReady
			}
		}
		return err
	})
}
