package cli

import "github.com/spf13/cobra"

func (o *options) cacheCommands() *cobra.Command {
	group := &cobra.Command{Use: "cache", Short: "Inspect signed peer cache readiness and locate store outputs"}
	group.AddCommand(&cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		api, err := o.api()
		if err != nil {
			return err
		}
		status, err := api.CacheStatus(cmd.Context())
		return o.print(cmd, status, err)
	}}, &cobra.Command{Use: "locate STORE-PATH", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		api, err := o.api()
		if err != nil {
			return err
		}
		locations, err := api.CacheLocations(cmd.Context(), args[0])
		return o.print(cmd, locations, err)
	}})
	return group
}
