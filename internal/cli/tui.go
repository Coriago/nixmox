package cli

import (
	"fmt"
	"os"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"nixmox/internal/ui/views/terminal"
)

func (o *options) tuiCommand() *cobra.Command {
	return &cobra.Command{Use: "tui", Short: "Open the cluster console using the HTTP API", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if o.json {
			return fmt.Errorf("--json is not supported by the terminal console")
		}
		if !term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd()) {
			return fmt.Errorf("tui requires terminal input and output")
		}
		api, err := o.api()
		if err != nil {
			return err
		}
		return operationError(terminal.Run(cmd.Context(), api))
	}}
}
