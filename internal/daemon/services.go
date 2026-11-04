package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

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
	"nixmox/internal/installer"
	"nixmox/internal/installer/installers"
	"nixmox/internal/membership"
	"nixmox/internal/recipe"
	"nixmox/internal/source"
	"nixmox/internal/task"
)

// wireServices shares domain service composition between server and client
// runtimes. Provisioning and authoritative membership writes stay with the
// coordinator; pending clients never decrypt signing credentials.
func (r *Runtime) wireServices(ctx context.Context, js jetstream.JetStream, nc *nats.Conn, member domain.Member) (*api.Services, error) {
	clusterStore, err := cluster.OpenStore[domain.Cluster](ctx, js, "CLUSTER")
	if err != nil {
		return nil, err
	}
	members, err := cluster.OpenStore[domain.Member](ctx, js, "MEMBERS")
	if err != nil {
		return nil, err
	}
	credentialStore, err := cluster.OpenStore[credentials.Record](ctx, js, "CREDENTIALS")
	if err != nil {
		return nil, err
	}
	recipients, err := cluster.OpenStore[credentials.Recipients](ctx, js, "RECIPIENTS")
	if err != nil {
		return nil, err
	}
	discoveryStore, err := cluster.OpenStore[discovery.Record](ctx, js, "DISCOVERY")
	if err != nil {
		return nil, err
	}
	enrollments, err := cluster.OpenStore[enrollment.Request](ctx, js, "ENROLLMENT")
	if err != nil {
		return nil, err
	}
	tasks, err := cluster.OpenStore[domain.Task](ctx, js, "TASKS")
	if err != nil {
		return nil, fmt.Errorf("open tasks: %w", err)
	}
	outputs, err := cluster.OpenStore[domain.TaskOutput](ctx, js, "OUTPUTS")
	if err != nil {
		return nil, fmt.Errorf("open outputs: %w", err)
	}
	locks, err := cluster.OpenStore[map[string]string](ctx, js, "LOCKS")
	if err != nil {
		return nil, fmt.Errorf("open locks: %w", err)
	}
	repos, err := cluster.OpenStore[source.Repository](ctx, js, "REPOS")
	if err != nil {
		return nil, fmt.Errorf("open repos: %w", err)
	}
	configurations, err := cluster.OpenStore[configuration.Version](ctx, js, "CONFIGURATIONS")
	if err != nil {
		return nil, fmt.Errorf("open configurations: %w", err)
	}
	recipes, err := cluster.OpenStore[recipe.Version](ctx, js, "RECIPES")
	if err != nil {
		return nil, fmt.Errorf("open recipes: %w", err)
	}
	installerDefinitions, err := cluster.OpenStore[installers.Definition](ctx, js, "INSTALLERS")
	if err != nil {
		return nil, fmt.Errorf("open installers: %w", err)
	}
	logs := cluster.Logs{JS: js}

	var local *cache.Local
	publicKey := ""
	if member.Status != "pending" {
		signing, err := credentialStore.Get(ctx, "nix-signing-key")
		if err != nil {
			return nil, err
		}
		secret, err := (credentials.Cipher{KeyFile: filepath.Join(r.config.StateDir, "age.key")}).Decrypt(ctx, signing.Value.Content)
		if err != nil {
			return nil, err
		}
		defer clear(secret)
		local, err = cache.NewLocal(string(secret), host.Process{})
		if err != nil {
			return nil, err
		}
		publicKey = local.PublicKey()
	}
	gateway := cache.NewGateway()
	success := false
	defer func() {
		if !success {
			gateway.Close()
		}
	}()
	if member.Status != "pending" && r.config.AdvertiseURL != "" {
		if err = gateway.ReplacePeers([]cache.Peer{{NodeID: member.NodeID, StoreURL: r.config.AdvertiseURL + "/store-cache"}}); err != nil {
			return nil, err
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
		Journal:   r.taskJournal(),
		HostID:    member.NodeID,
		Tasks:     tasks,
		Outputs:   outputs,
		Logs:      logs,
		Resources: task.Resources{Store: locks},
		Runners:   map[string]task.Runner{"build": builder.Runner},
	}

	installerService := &installer.Service{
		HostID: member.NodeID, Definitions: installerDefinitions, Tasks: manager,
		Repository:        builder.Repository,
		PreserveInstalled: r.preserveInstalled,
		Inspect: func(ctx context.Context) (installers.Target, error) {
			return installer.InspectLocal(ctx, member.NodeID, member.Environment)
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
			if nodeID == member.NodeID {
				return (host.Process{}).Disks(ctx)
			}
			var output json.RawMessage
			err := host.Observe(ctx, nc, nodeID, host.ObservationRequest{Kind: "disks"}, &output)
			return output, err
		},
		HostHardware: func(ctx context.Context, nodeID string, noFileSystems bool) (string, error) {
			if _, err := members.Get(ctx, nodeID); err != nil {
				return "", err
			}
			if nodeID == member.NodeID {
				return (host.Process{}).Hardware(ctx, noFileSystems)
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

	deployments := &deployment.Service{HostID: member.NodeID, Environment: r.config.Environment, Configurations: configurationService, Members: members, Tasks: manager, Builder: builder}
	deployments.Installation = &deployment.InstallationRuntime{
		Inspect: func(ctx context.Context) (installers.Target, uint64, error) {
			info, err := r.membershipInfo(ctx)
			return info.Target, info.Generation, err
		},
		Capture: r.captureInstallation, Preserve: r.preserveInstallation, Validate: r.validateInstalled, Promote: r.requestPromotion,
	}
	manager.Runners[deployment.InstallationTask] = deployments.InstallRunner
	manager.Runners["installation-build"] = deployments.InstallationBuild
	manager.Runners["deployment"] = deployments.Runner
	manager.Runners["configuration-build"] = deployments.Build
	metrics := cluster.Metrics{Connection: nc}
	enrollmentService := &enrollment.Service{NodeID: member.NodeID, Requests: enrollments, Members: members, Clusters: clusterStore, Discovery: discoveryStore, Issue: r.issueEnrollment, Authorize: func(ctx context.Context) error { return r.refreshAuthorization(ctx, members) }}
	membershipService := &membership.Service{HostID: member.NodeID, ClusterID: member.ClusterID, Members: members, Credentials: credentialStore, Recipients: recipients, Tasks: manager, Cipher: credentials.Cipher{KeyFile: filepath.Join(r.config.StateDir, "age.key")},
		Inspect: func(ctx context.Context, id string) (membership.Info, error) { return inspectMember(ctx, nc, id) },
		Apply: func(ctx context.Context, id string, change membership.Change) error {
			return applyRemoteMembership(ctx, nc, id, change)
		},
		Certificate: r.promotionCertificate,
		Authorize:   func(ctx context.Context) error { return r.refreshAuthorization(ctx, members) },
		Replicate:   func(ctx context.Context, count int) error { return replicateStreams(ctx, js, count) },
	}
	if member.Status != "pending" {
		manager.Runners[membership.PromotionTask] = membershipService.Runner
	}
	svc := &api.Services{
		Membership:     membershipService,
		Pending:        member.Status == "pending",
		HostDisks:      collector.HostDisks,
		Enrollment:     enrollmentService,
		Discovery:      discoveryStore,
		Metrics:        metrics,
		Deployments:    deployments,
		Credentials:    credentialStore,
		CachePublicKey: publicKey,
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
	}
	if local != nil {
		svc.StoreCache = local
	}
	success = true
	return svc, nil
}

func (r *Runtime) runServices(svc *api.Services) error {
	if err := r.startMembershipTransport(r.client, svc); err != nil {
		return err
	}
	r.workers.Go(func() {
		err := svc.Members.Watch(r.lifetime, ">", func(_ *cluster.Record[domain.Member], _ bool) error {
			if err := r.refreshAuthorization(r.lifetime, svc.Members); err != nil {
				return err
			}
			members, err := svc.Members.All(r.lifetime)
			if err != nil {
				return err
			}
			peers := []cache.Peer{}
			for _, member := range members {
				if member.Value.Status != "full" && member.Value.Status != "full-stable" {
					continue
				}
				for _, endpoint := range member.Value.Endpoints {
					if endpoint.HTTP != "" {
						peers = append(peers, cache.Peer{NodeID: member.Key, StoreURL: endpoint.HTTP + "/store-cache"})
						break
					}
				}
			}
			return svc.Cache.(*cache.Gateway).ReplacePeers(peers)
		})
		if err != nil && r.lifetime.Err() == nil {
			select {
			case r.failures <- err:
			default:
			}
		}
	})
	if !svc.Pending {
		r.workers.Go(func() {
			if err := svc.Enrollment.Run(r.lifetime); err != nil && r.lifetime.Err() == nil {
				select {
				case r.failures <- err:
				default:
				}
			}
		})
	}
	r.workers.Go(func() { host.Metrics(r.lifetime, svc.Tasks.HostID, svc.Metrics) })
	r.workers.Go(func() {
		if err := svc.Tasks.Run(r.lifetime); err != nil && r.lifetime.Err() == nil {
			select {
			case r.failures <- err:
			default:
			}
		}
	})
	return nil
}
