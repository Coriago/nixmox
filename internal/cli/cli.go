// Package cli maps Cobra commands to the shared API client. It never owns
// cluster state or directly connects to NATS.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.opentelemetry.io/otel"
	"go.yaml.in/yaml/v3"

	"nixmox/internal/client"
	"nixmox/internal/daemon"
	"nixmox/internal/discovery"
	"nixmox/internal/util"
)

func Execute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	shutdown, err := util.Start(ctx, util.Config{Service: "nixmox", Endpoint: os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"), Output: stderr})
	if err != nil {
		fmt.Fprintln(stderr, "telemetry setup failed")
		return 1
	}
	defer func() {
		flush, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdown(flush)
	}()
	ctx, span := otel.Tracer("nixmox.cli").Start(ctx, "cli.execute")
	defer span.End()
	command := NewCommand(os.Stdin, stdout, stderr)
	command.SetArgs(args)
	if err = command.ExecuteContext(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return 130
		}
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

type console struct{ config *viper.Viper }

func NewCommand(stdin io.Reader, stdout, stderr io.Writer) *cobra.Command {
	config := viper.New()
	config.SetEnvPrefix("NIXMOX")
	config.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	config.AutomaticEnv()
	root := &cobra.Command{
		Use:           "nixmox",
		Short:         "Manage NixOS hosts through the Nixmox API",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.PersistentFlags().String("endpoint", "http://127.0.0.1:8080", "Daemon API endpoint")
	root.PersistentFlags().Bool("json", false, "Print compact JSON (streams use newline-delimited JSON)")
	root.PersistentFlags().String("config", "", "Configuration file (flags override environment and file values)")
	_ = config.BindPFlags(root.PersistentFlags())
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if path := config.GetString("config"); path != "" {
			config.SetConfigFile(path)
			return config.ReadInConfig()
		}
		return nil
	}
	c := console{config: config}
	root.AddCommand(c.daemonCommand(), c.get("health", "Show daemon and cluster health", "/health", 0))
	c.addReads(root)
	c.addMutations(root)
	root.AddCommand(c.uiCommand())
	return root
}

func (c console) daemonCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "daemon",
		Short: "Start the daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			config := daemon.Config{
				DiscoveryPort:      c.config.GetInt("discovery-port"),
				UIAssets:           c.config.GetString("ui-assets"),
				Environment:        c.config.GetString("environment"),
				StateDir:           c.config.GetString("state-dir"),
				Name:               c.config.GetString("name"),
				Listen:             c.config.GetString("listen"),
				AdvertiseURL:       c.config.GetString("advertise"),
				NATSHost:           c.config.GetString("nats-host"),
				NATSPort:           c.config.GetInt("nats-port"),
				RoutePort:          c.config.GetInt("route-port"),
				BootstrapPort:      c.config.GetInt("bootstrap-port"),
				BootstrapRoutePort: c.config.GetInt("bootstrap-route-port"),
				SSHHostKey:         c.config.GetString("ssh-host-key"),
			}
			return daemon.Run(cmd.Context(), config)
		},
	}
	f := command.Flags()
	f.String("environment", "installed", "Boot environment: installed or installer")
	f.String("ui-assets", "", "Browser assets directory (default: installed package assets)")
	f.String("state-dir", "/var/lib/nixmox", "Private persistent state directory")
	f.String("name", "", "Node name (defaults to hostname on first startup)")
	f.String("listen", "127.0.0.1:8080", "HTTP listen address")
	f.String("advertise", "http://127.0.0.1:8080", "Reachable HTTP URL advertised to peers")
	f.String("nats-host", "127.0.0.1", "NATS client and route bind address")
	f.Int("discovery-port", discovery.DefaultPort, "LAN discovery UDP port (0 disables discovery)")
	f.Int("nats-port", 4222, "NATS client port")
	f.Int("route-port", 6222, "NATS route port")
	f.Int("bootstrap-port", 4223, "Bootstrap NATS client port")
	f.Int("bootstrap-route-port", 6223, "Bootstrap route port")
	f.String("ssh-host-key", "/etc/ssh/ssh_host_ed25519_key", "SSH host key used to derive credential-store age identity")
	_ = c.config.BindPFlags(f)
	return command
}

func (c console) call(cmd *cobra.Command, method, path string, input any) error {
	api, err := client.New(c.config.GetString("endpoint"), nil)
	if err != nil {
		return err
	}
	defer api.Close()
	var result json.RawMessage
	if err = api.Do(cmd.Context(), method, path, input, &result); err != nil {
		return err
	}
	if len(result) == 0 {
		return nil
	}
	encoder := json.NewEncoder(cmd.OutOrStdout())
	if !c.config.GetBool("json") {
		encoder.SetIndent("", "  ")
	}
	return encoder.Encode(result)
}

func (c console) watch(cmd *cobra.Command, path string) error {
	api, err := client.New(c.config.GetString("endpoint"), nil)
	if err != nil {
		return err
	}
	defer api.Close()
	encoder := json.NewEncoder(cmd.OutOrStdout())
	return api.Watch(cmd.Context(), path, "", func(event client.Event) error { return encoder.Encode(event) })
}

func definition(cmd *cobra.Command) (json.RawMessage, error) {
	path, _ := cmd.Flags().GetString("file")
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open definition file")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, errors.New("definition file exceeds 1 MiB")
	}
	if json.Valid(data) {
		return data, nil
	}
	var object map[string]any
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err = decoder.Decode(&object); err != nil {
		return nil, errors.New("definition must be JSON or YAML")
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("definition must contain one document")
	}
	return json.Marshal(object)
}

func required(command *cobra.Command, name, description string) {
	command.Flags().String(name, "", description)
	_ = command.MarkFlagRequired(name)
}

func flag(command *cobra.Command, name string) string {
	value, _ := command.Flags().GetString(name)
	return value
}

func boolean(command *cobra.Command, name string) bool {
	value, _ := command.Flags().GetBool(name)
	return value
}
