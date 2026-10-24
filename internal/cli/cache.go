package cli

import (
	"github.com/spf13/cobra"
	"nixmox/internal/client"
)

func (o *options) cacheCommands() *cobra.Command {
	group := &cobra.Command{Use: "cache", Short: "Inspect signed peer cache readiness and locate store outputs"}
	group.AddCommand(&cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: o.read(func(cmd *cobra.Command, api *client.Client, _ []string) (any, error) {
		return api.CacheStatus(cmd.Context())
	})}, &cobra.Command{Use: "locate STORE-PATH", Args: cobra.ExactArgs(1), RunE: o.read(func(cmd *cobra.Command, api *client.Client, args []string) (any, error) {
		return api.CacheLocations(cmd.Context(), args[0])
	})})
	return group
}
