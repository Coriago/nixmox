package main

import (
	"context"
	"github.com/spf13/cobra"
	"nixmox/internal/installerui"
)

func newInstallerCommand() *cobra.Command {
	var auto bool
	cmd := &cobra.Command{
		Use: "installer", Short: "Open the installer terminal menu for networking and local status",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return installerui.Run(cmd.Context(), installerui.NetworkManager{}, cmd.InOrStdin(), cmd.OutOrStdout(), auto,
				func(ctx context.Context) ([]byte, error) { return agentRequest(ctx, "GET", "/api/v1/bootstrap", nil) })
		},
	}
	cmd.Flags().BoolVar(&auto, "auto", false, "Wait briefly for networking and skip the menu when a wired or Wi-Fi IPv4 connection is ready")
	return cmd
}
