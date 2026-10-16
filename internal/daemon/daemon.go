// Package daemon is the composition root. Components own their own state;
// startup and teardown are paired here, including failures during startup.
package daemon

import (
	"context"
	"encoding/json"
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
	"nixmox/internal/events"
	"nixmox/internal/gitops"
	"nixmox/internal/hostinfo"
	"nixmox/internal/jobs"
	"nixmox/internal/logbuf"
	"nixmox/internal/messaging"
	"nixmox/internal/natsx"
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
	files, err := jobs.NewFileStore(cfg.DataDir)
	if err != nil {
		return err
	}
	updates, publications := &events.Signal{}, &events.Signal{}
	changed := func() { updates.Notify(); publications.Notify() }
	var bus *messaging.Bus
	store := jobs.ObservableStore{Store: files, Changed: changed, LogAppended: func(id string, chunk jobs.LogChunk) {
		if bus != nil {
			bus.PublishLog(id, chunk)
		}
	}}
	runner := jobs.NewRunner(store)
	defer runner.Shutdown()
	git, err := GitOps(cfg)
	if err != nil {
		return err
	}
	git.Changed = changed
	records, err := cluster.NewLocalRecordStore(cfg.DataDir)
	if err != nil {
		return err
	}
	mirror := cluster.NewSwitchingRecordStore(records)
	manager := apply.NewManager(cfg, git, mirror, runner, store, cfg.Hostname)
	runner.OnTerminal = func(j jobs.Job) { manager.HandleTerminal(j); changed() }

	clusterIP := cfg.AdvertiseAddr
	if clusterIP == "" {
		clusterIP = "127.0.0.1"
	}
	supervisor := cluster.NewSupervisor(cfg, cfg.DataDir, clusterIP)
	view := cluster.NewView()
	view.Changed = updates.Notify
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
		// Public recipient travels with inventory through the existing NATS KV.
		p := cluster.HostPayload{Kernel: h.Kernel, NodeID: cfg.Hostname, Hostname: h.Hostname, NixOSVersion: h.NixOSVersion, AgentVersion: h.AgentVersion, Uptime: h.Uptime, Resources: h.Resources, HTTPAddress: cfg.PeerAddress(), ClusterRoute: fmt.Sprintf("nats://%s:%d", clusterIP, cfg.ClusterPort), RemoteSHA: git.State().RemoteSHA, PulledSHA: git.State().LastPulledSHA}
		active, err := jobs.ActiveDeployment(store)
		p.AgeRecipient, p.AgeRecipientError = h.AgeRecipient, h.AgeRecipientError
		p.ActiveDeployment, p.StatusUnavailable = active, err != nil
		if latest, ok, err := records.Latest(cfg.Hostname); err == nil && ok {
			p.ApplyRecord = &latest
		}
		return p
	})
	runtime.Telemetry = func() hostinfo.HostInfo { return hostinfo.Sample(cfg) }
	runtime.TelemetryChanged = updates.Notify
	runtime.Changes = publications
	p, err := portal.New()
	if err != nil {
		return err
	}
	p.Hostname = cfg.Hostname
	srv := server.New(cfg, store, runner)
	srv.Updates = updates
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
	bus, err = messaging.New(ctx, cfg.Hostname, cfg.DataDir)
	if err != nil {
		return err
	}
	bus.Changed = changed
	srv.SetBus(bus)
	supervisor.OnReconfigure = func(c context.Context, emb *natsx.EmbeddedServer) { runtime.Rearm(c, emb); bus.Rearm(c, emb) }
	if err := runner.Recover(); err != nil {
		return err
	}
	if err := supervisor.Start(ctx); err != nil {
		return err
	}
	defer supervisor.Stop()
	defer runtime.Stop()
	defer bus.Stop()
	if cfg.JoinSeed != "" && supervisor.AutoJoinAllowed() {
		if err := supervisor.JoinCluster(ctx, cfg.JoinSeed); err != nil {
			log.Printf("configured join: %v (retry via portal)", err)
		}
	}
	if cfg.GitPollInterval > 0 {
		poller := gitops.NewPoller(git, func(sha string) {
			if !cfg.AutoApply {
				return
			}
			for _, path := range []string{"/api/v1/gitops/pull", "/api/v1/apply"} {
				call, cancel := context.WithTimeout(ctx, time.Minute)
				res, err := bus.Command(call, messaging.Request{Target: cfg.Hostname, Method: "POST", Path: path})
				cancel()
				if err != nil || res.Status >= 300 {
					log.Printf("auto-apply command: %v %s", err, res.Body)
					return
				}
				// Pull must finish before apply pins a revision.
				if path == "/api/v1/gitops/pull" {
					var j jobs.Job
					if json.Unmarshal(res.Body, &j) != nil {
						return
					}
					for {
						notification := updates.Watch()
						current, err := store.Get(j.ID)
						if err != nil || current.State == "failed" {
							return
						}
						if current.State == "applied" {
							break
						}
						select {
						case <-ctx.Done():
							return
						case <-notification:
						}
					}
				}
			}
		})
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
