package cli

import (
	"github.com/spf13/cobra"
	"nixmox/internal/client"
	"nixmox/internal/ui/views/terminal"
)

func (c console) uiCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "ui",
		Aliases: []string{"tui"},
		Short:   "Open the terminal console",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			api, err := client.New(c.config.GetString("endpoint"), nil)
			if err != nil {
				return err
			}
			defer api.Close()
			return terminal.Run(cmd.Context(), api)
		},
	}
}
