package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"nixmox/internal/apply"
	"nixmox/internal/cluster"
	"nixmox/internal/config"
	"nixmox/internal/gitops"
	"nixmox/internal/hostinfo"
	"nixmox/internal/jobs"
	"nixmox/internal/portal"
	"nixmox/internal/server"
)

var (
	cfg        *config.Config
	configFile string
)

var rootCmd = &cobra.Command{
	Use:   "nixmox",
	Short: "nixmox — Proxmox-like management daemon for NixOS",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if cmd.Name() == "version" || cmd.Name() == "help" {
			return nil
		}
		path := config.FindConfigFile(configFile)
		if configFile != "" || os.Getenv("NIXMOX_CONFIG") != "" {
			if _, err := os.Stat(path); err != nil {
				return fmt.Errorf("config file %q not found: %w", path, err)
			}
		}
		var err error
		cfg, err = config.Load(path)
		return err
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(&configFile, "config", "", "config file path (default: /etc/nixmox/config.yaml or $NIXMOX_CONFIG)")
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the nixmox daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		if cfg == nil {
			return fmt.Errorf("config not loaded")
		}
		fmt.Fprintln(cmd.OutOrStdout(), "nixmox serve — config loaded:")
		fmt.Fprintln(cmd.OutOrStdout(), cfg.String())

		if err := cfg.EnsureDataDir(); err != nil {
			return fmt.Errorf("failed to ensure data dir: %w", err)
		}

		store, err := jobs.NewFileStore(cfg.DataDir)
		if err != nil {
			return fmt.Errorf("failed to initialize job store: %w", err)
		}

		runner := jobs.NewRunner(store)

		gitMgr, err := buildGitOps(cfg)
		if err != nil {
			return fmt.Errorf("failed to initialize gitops: %w", err)
		}

		hostname := hostinfo.Get(cfg).Hostname

		records, err := cluster.NewLocalRecordStore(cfg.DataDir)
		if err != nil {
			return fmt.Errorf("failed to initialize applylog: %w", err)
		}
		switchStore := cluster.NewSwitchingRecordStore(records)
		applyMgr := apply.NewManager(cfg, gitMgr, switchStore, runner, store, hostname)
		runner.OnTerminal = applyMgr.HandleTerminal

		clusterIP := cfg.AdvertiseAddr
		if clusterIP == "" {
			clusterIP = "127.0.0.1"
		}
		sup := cluster.NewSupervisor(cfg, cfg.DataDir, clusterIP)
		view := cluster.NewView()
		rt := cluster.NewRuntime(view, switchStore, func() cluster.HostPayload {
			h := hostinfo.Get(cfg)
			payload := cluster.HostPayload{
				NodeID:       cfg.Hostname,
				Hostname:     h.Hostname,
				NixOSVersion: h.NixOSVersion,
				AgentVersion: h.AgentVersion,
				Uptime:       h.Uptime,
				PulledSHA:    gitMgr.State().LastPulledSHA,
			}
			if latest, ok, err := switchStore.Latest(cfg.Hostname); err == nil && ok {
				rec := latest
				payload.ApplyRecord = &rec
			}
			return payload
		})
		sup.OnReconfigure = rt.Rearm
		if err := sup.Start(context.Background()); err != nil {
			return fmt.Errorf("failed to start embedded nats: %w", err)
		}

		p, err := portal.New()
		if err != nil {
			return fmt.Errorf("failed to initialize portal: %w", err)
		}
		p.Hostname = hostname

		srv := server.New(cfg, store, runner)
		srv.SetGitOps(gitMgr)
		srv.SetApply(applyMgr, switchStore)
		srv.SetCluster(sup, view)
		srv.MountHandlers(p, server.NoopAuth{})
		srv.MountGitOps(p)
		srv.MountApply(p)
		srv.MountCluster(p)

		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		var cron *jobs.Cron
		if cfg.GitPollInterval > 0 {
			cron = jobs.NewCron()
			poller := gitops.NewPoller(gitMgr, applyMgr.OnNewSHA)
			spec := fmt.Sprintf("@every %ds", cfg.GitPollInterval)
			if _, err := cron.AddFunc(spec, func() { poller.Tick(context.Background()) }); err != nil {
				return fmt.Errorf("invalid git poll interval: %w", err)
			}
			cron.Start(ctx)
		}

		fmt.Fprintf(cmd.OutOrStdout(), "Listening on %s\n", cfg.HTTPAddr)
		if err := srv.Start(ctx); err != nil {
			return fmt.Errorf("server error: %w", err)
		}

		// Stop runner after HTTP server stops accepting requests
		runner.Stop()
		rt.Stop()
		sup.Stop()

		fmt.Fprintln(cmd.OutOrStdout(), "Server stopped cleanly")
		return nil
	},
}

func buildGitOps(cfg *config.Config) (*gitops.Manager, error) {
	var env []string
	if cfg.SSHKeyPath != "" {
		env = append(env, "GIT_SSH_COMMAND=ssh -i "+cfg.SSHKeyPath+" -o StrictHostKeyChecking=accept-new")
	}
	if cfg.NetrcPath != "" {
		env = append(env,
			"GIT_CONFIG_COUNT=1",
			"GIT_CONFIG_KEY_0=credential.helper",
			"GIT_CONFIG_VALUE_0=netrc -f "+cfg.NetrcPath,
		)
	}
	mgr := gitops.NewManager(cfg.DataDir, gitops.ExecClient{ExtraEnv: env})
	if err := mgr.Load(); err != nil {
		log.Printf("gitops: could not load saved state: %v", err)
	}
	if cfg.GitRepoURL != "" {
		if err := mgr.Configure(cfg.GitRepoURL, cfg.GitBranch); err != nil {
			return nil, err
		}
	}
	return mgr, nil
}

var joinCmd = &cobra.Command{
	Use:   "join <peer>",
	Short: "Join an existing nixmox cluster",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// The CLI POSTs to the local agent's join/initiate endpoint — the same
		// code path the portal's join form uses (single implementation in the
		// cluster supervisor).
		body, _ := json.Marshal(map[string]string{"seed": args[0]})
		url := "http://" + cfg.HTTPAddr + "/api/v1/cluster/join/initiate"
		resp, err := http.Post(url, "application/json", bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("join: cannot reach local agent at %s (is `nixmox serve` running?): %w", cfg.HTTPAddr, err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("join failed: %s", strings.TrimSpace(string(out)))
		}
		fmt.Fprintf(cmd.OutOrStdout(), "joined: %s\n", strings.TrimSpace(string(out)))
		return nil
	},
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show cluster status",
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("status: not yet implemented")
	},
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprintf(cmd.OutOrStdout(), "version: %s, commit: %s\n", config.Version, config.Commit)
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(joinCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(versionCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
