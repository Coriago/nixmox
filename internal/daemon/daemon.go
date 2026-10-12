// Package daemon is the composition root. Components own their own state;
// startup and teardown are paired here, including failures during startup.
package daemon

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"nixmox/internal/apply"
	"nixmox/internal/cluster"
	"nixmox/internal/config"
	"nixmox/internal/discovery"
	"nixmox/internal/gitops"
	"nixmox/internal/hostinfo"
	"nixmox/internal/jobs"
	"nixmox/internal/logbuf"
	"nixmox/internal/portal"
	"nixmox/internal/server"
)

func Run(ctx context.Context, cfg *config.Config, out io.Writer) error {
	if cfg == nil {
		return fmt.Errorf("config not loaded")
	}
	cfg.ResolveAdvertise()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var workers sync.WaitGroup
	spawn := func(fn func()) { workers.Add(1); go func() { defer workers.Done(); fn() }() }
	logs := logbuf.New(128 << 10)
	previous := log.Writer()
	log.SetOutput(io.MultiWriter(previous, logs))
	defer log.SetOutput(previous)
	if err := cfg.EnsureDataDir(); err != nil {
		return err
	}
	store, err := jobs.NewFileStore(cfg.DataDir)
	if err != nil {
		return err
	}
	runner := jobs.NewRunner(store)
	defer runner.Stop()
	git, err := GitOps(cfg)
	if err != nil {
		return err
	}
	records, err := cluster.NewLocalRecordStore(cfg.DataDir)
	if err != nil {
		return err
	}
	mirror := cluster.NewSwitchingRecordStore(records)
	manager := apply.NewManager(cfg, git, mirror, runner, store, cfg.Hostname)
	runner.OnTerminal = manager.HandleTerminal
	clusterIP := cfg.AdvertiseAddr
	if clusterIP == "" {
		clusterIP = "127.0.0.1"
	}
	supervisor := cluster.NewSupervisor(cfg, cfg.DataDir, clusterIP)
	view := cluster.NewView()
	if err := view.Load(cfg.DataDirPath("hosts.json")); err != nil {
		log.Printf("inventory: %v", err)
	}
	defer func() {
		if err := view.Save(cfg.DataDirPath("hosts.json")); err != nil {
			log.Printf("save inventory: %v", err)
		}
	}()
	runtime := cluster.NewRuntime(view, mirror, func() cluster.HostPayload {
		h := hostinfo.Get(cfg)
		p := cluster.HostPayload{Kernel: h.Kernel, NodeID: cfg.Hostname, Hostname: h.Hostname, NixOSVersion: h.NixOSVersion, AgentVersion: h.AgentVersion, Uptime: h.Uptime, Resources: h.Resources, HTTPAddress: cfg.PeerAddress(), ClusterRoute: fmt.Sprintf("nats://%s:%d", clusterIP, cfg.ClusterPort), PulledSHA: git.State().LastPulledSHA}
		if latest, ok, err := records.Latest(cfg.Hostname); err == nil && ok {
			p.ApplyRecord = &latest
		}
		return p
	})
	supervisor.OnReconfigure = runtime.Rearm
	if err := supervisor.Start(ctx); err != nil {
		return err
	}
	defer supervisor.Stop()
	defer runtime.Stop()
	if cfg.JoinSeed != "" && supervisor.AutoJoinAllowed() {
		if err := supervisor.JoinCluster(ctx, cfg.JoinSeed); err != nil {
			log.Printf("configured join: %v (retry via portal)", err)
		}
	}
	p, err := portal.New()
	if err != nil {
		return err
	}
	p.Hostname = cfg.Hostname
	srv := server.New(cfg, store, runner)
	srv.Logs = logs
	srv.SetGitOps(git)
	srv.SetApply(manager, mirror)
	srv.SetCluster(supervisor, view)
	registry := discovery.New()
	srv.Discoveries = registry
	if cfg.Discovery {
		spawn(func() {
			if err := registry.Run(ctx, func() discovery.Peer {
				return discovery.Peer{NodeID: cfg.Hostname, Address: cfg.PeerAddress(), Cluster: supervisor.State(0).ClusterName}
			}); err != nil {
				log.Printf("discovery: %v", err)
			}
		})
	}
	srv.MountHandlers(p, server.NoopAuth{})
	srv.MountGitOps(p)
	srv.MountApply(p)
	srv.MountCluster(p)
	if cfg.GitPollInterval > 0 {
		poller := gitops.NewPoller(git, func(sha string) { manager.OnNewSHAContext(ctx, sha) })
		spawn(func() {
			t := time.NewTicker(time.Duration(cfg.GitPollInterval) * time.Second)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					poller.Tick(ctx)
				}
			}
		})
	}
	// Persist offline wake addresses periodically as well as on orderly shutdown.
	spawn(func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := view.Save(cfg.DataDirPath("hosts.json")); err != nil {
					log.Printf("save inventory: %v", err)
				}
			}
		}
	})
	defer func() { cancel(); workers.Wait() }()
	fmt.Fprintf(out, "Listening on %s\n", cfg.HTTPAddr)
	return srv.Start(ctx)
}
func GitOps(cfg *config.Config) (*gitops.Manager, error) {
	var env []string
	if cfg.SSHKeyPath != "" {
		env = append(env, "GIT_SSH_COMMAND=ssh -i '"+strings.ReplaceAll(cfg.SSHKeyPath, "'", `'"'"'`)+"' -o StrictHostKeyChecking=accept-new")
	}
	if cfg.NetrcPath != "" {
		env = append(env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0=netrc -f '"+strings.ReplaceAll(cfg.NetrcPath, "'", `'"'"'`)+"'")
	}
	mgr := gitops.NewManager(cfg.DataDir, gitops.ExecClient{ExtraEnv: env})
	if err := mgr.Load(); err != nil {
		return nil, err
	}
	if cfg.GitRepoURL != "" {
		if err := mgr.Configure(cfg.GitRepoURL, cfg.GitBranch); err != nil {
			return nil, err
		}
	}
	return mgr, nil
}
