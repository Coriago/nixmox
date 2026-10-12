package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"nixmox/internal/config"
	"nixmox/internal/daemon"
)

var (
	cfg        *config.Config
	configFile string
)
var rootCmd = &cobra.Command{
	Use: "nixmox", Short: "nixmox — Proxmox-like management daemon for NixOS",
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
var serveCmd = &cobra.Command{Use: "serve", Short: "Start the nixmox daemon", RunE: func(cmd *cobra.Command, args []string) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return daemon.Run(ctx, cfg, cmd.OutOrStdout())
}}

func agentRequest(ctx context.Context, method, path string, body any) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var b bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&b).Encode(body); err != nil {
			return nil, err
		}
	}
	addr := cfg.HTTPAddr
	if host, port, err := net.SplitHostPort(addr); err == nil && (host == "" || host == "0.0.0.0" || host == "::") {
		addr = net.JoinHostPort("127.0.0.1", port)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+addr+path, &b)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach local agent at %s: %w", addr, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("agent: %s", strings.TrimSpace(string(out)))
	}
	return out, nil
}

var joinCmd = &cobra.Command{Use: "join <peer>", Short: "Join an existing nixmox cluster", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
	out, err := agentRequest(cmd.Context(), "POST", "/api/v1/cluster/join/initiate", map[string]string{"seed": args[0]})
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "joined: %s\n", strings.TrimSpace(string(out)))
	return nil
}}
var statusCmd = &cobra.Command{Use: "status", Short: "Show cluster status", RunE: func(cmd *cobra.Command, args []string) error {
	out, err := agentRequest(cmd.Context(), "GET", "/api/v1/bootstrap", nil)
	if err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), strings.TrimSpace(string(out)))
	return nil
}}
var versionCmd = &cobra.Command{Use: "version", Short: "Print version information", Run: func(cmd *cobra.Command, args []string) {
	fmt.Fprintf(cmd.OutOrStdout(), "version: %s, commit: %s\n", config.Version, config.Commit)
}}

func init() {
	rootCmd.PersistentFlags().StringVar(&configFile, "config", "", "config file path (default: /etc/nixmox/config.yaml or $NIXMOX_CONFIG)")
	rootCmd.AddCommand(serveCmd, joinCmd, statusCmd, versionCmd)
}
func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
