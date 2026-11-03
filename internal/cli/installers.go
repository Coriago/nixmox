package cli

import (
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
)

func (c console) addInstallers(root *cobra.Command) {
	register := &cobra.Command{
		Use: "register", Short: "Register an immutable pinned built-in installer", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			input, err := definition(cmd)
			if err != nil {
				return err
			}
			return c.call(cmd, http.MethodPost, "/installers", input)
		},
	}
	required(register, "file", "JSON or YAML installer registration")
	child(root, "installer").AddCommand(register)
	checks := &cobra.Command{
		Use: "checks", Short: "Check installer/prepare compatibility and select a pair", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return c.call(cmd, http.MethodPost, "/hosts/"+url.PathEscape(flag(cmd, "host"))+"/installer-checks",
				map[string]any{"installerId": flag(cmd, "installer"), "prepareId": flag(cmd, "prepare")})
		},
	}
	required(checks, "host", "Target host ID")
	checks.Flags().String("installer", "", "Optional installer ID")
	checks.Flags().String("prepare", "", "Optional prepare ID")
	child(root, "installer").AddCommand(checks)
	finalize := &cobra.Command{
		Use: "checks", Short: "Check general finalizer compatibility", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return c.call(cmd, http.MethodPost, "/hosts/"+url.PathEscape(flag(cmd, "host"))+"/install-finalize-checks",
				map[string]any{"root": flag(cmd, "root"), "systemPath": flag(cmd, "system"), "finalizeId": flag(cmd, "finalize")})
		},
	}
	required(finalize, "host", "Target installer host ID")
	required(finalize, "root", "Mounted installed root")
	required(finalize, "system", "Built NixOS system store path")
	finalize.Flags().String("finalize", "", "Finalizer ID (default: finalize-general)")
	child(root, "install-finalize").AddCommand(finalize)
	run := &cobra.Command{
		Use: "run", Short: "Submit general finalization of a mounted NixOS root", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return c.call(cmd, http.MethodPost, "/hosts/"+url.PathEscape(flag(cmd, "host"))+"/install-finalize",
				map[string]any{"root": flag(cmd, "root"), "systemPath": flag(cmd, "system"), "finalizeId": flag(cmd, "finalize")})
		},
	}
	required(run, "host", "Target installer host ID")
	required(run, "root", "Mounted installed root")
	required(run, "system", "Built NixOS system store path")
	run.Flags().String("finalize", "", "Finalizer ID (default: finalize-general)")
	child(root, "install-finalize").AddCommand(run)
}
