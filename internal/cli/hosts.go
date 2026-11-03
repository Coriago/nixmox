package cli

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
)

func (c console) addHostJobs(root *cobra.Command) {
	build := &cobra.Command{
		Use:   "build",
		Short: "Build a pinned Nix installable",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			input, err := definition(cmd)
			if err != nil {
				return err
			}
			return c.call(cmd, http.MethodPost, "/hosts/"+url.PathEscape(flag(cmd, "host"))+"/builds", input)
		},
	}
	required(build, "host", "Executing host ID")
	required(build, "file", "JSON or YAML build inputs")
	root.AddCommand(build)
	for _, action := range []string{"deploy", "install"} {
		command := &cobra.Command{
			Use:   action,
			Short: action + " a pinned configuration version",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				version, _ := cmd.Flags().GetUint64("version")
				if version == 0 {
					return errors.New("configuration version must be positive")
				}
				payload := map[string]any{"configurationId": flag(cmd, "configuration"), "configurationVersion": version}
				if buildHost := flag(cmd, "build-host"); buildHost != "" {
					payload["buildHostId"] = buildHost
				}
				route := "installations"
				if action == "deploy" {
					route = "deployments"
					payload["autoApprove"] = boolean(cmd, "approve")
				}
				return c.call(cmd, http.MethodPost, "/hosts/"+url.PathEscape(flag(cmd, "host"))+"/"+route, payload)
			},
		}
		required(command, "host", "Target host ID")
		required(command, "configuration", "Configuration ID")
		versionFlag(command)
		command.Flags().String("build-host", "", "Optional build host ID")
		if action == "deploy" {
			command.Flags().Bool("approve", false, "Automatically approve deployment diff")
		}
		root.AddCommand(command)
	}
	rollback := &cobra.Command{
		Use:   "rollback",
		Short: "Activate the previous retained generation",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return c.call(cmd, http.MethodPost, "/hosts/"+url.PathEscape(flag(cmd, "host"))+"/rollbacks", nil)
		},
	}
	required(rollback, "host", "Target host ID")
	root.AddCommand(rollback)
	media := &cobra.Command{Use: "media", Short: "Create installer media"}
	root.AddCommand(media)
	for _, action := range []string{"launch", "create"} {
		command := &cobra.Command{
			Use:   action,
			Short: action + " installer",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				payload := map[string]any{"installerId": flag(cmd, "installer")}
				if buildHost := flag(cmd, "build-host"); buildHost != "" {
					payload["buildHostId"] = buildHost
				}
				if action == "launch" {
					payload["prepareId"] = flag(cmd, "prepare")
				}
				route := "launch-installer"
				if action == "create" {
					route = "boot-media"
					payload["device"] = flag(cmd, "device")
				}
				return c.call(cmd, http.MethodPost, "/hosts/"+url.PathEscape(flag(cmd, "host"))+"/"+route, payload)
			},
		}
		required(command, "host", "Target host ID")
		if action == "create" {
			required(command, "installer", "Installer definition ID")
		} else {
			command.Flags().String("installer", "", "Installer ID (default: best compatible)")
			command.Flags().String("prepare", "", "Prepare ID (default: best compatible)")
		}
		command.Flags().String("build-host", "", "Build host ID")
		if action == "create" {
			required(command, "device", "Selected drive path (will be overwritten)")
			media.AddCommand(command)
		} else {
			child(root, "installer").AddCommand(command)
		}
	}
}
