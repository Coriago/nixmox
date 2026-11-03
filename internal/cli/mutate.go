package cli

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"
)

func (c console) addMutations(root *cobra.Command) {
	create := &cobra.Command{
		Use:   "create",
		Short: "Create a cluster on an unjoined host",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return c.call(cmd, http.MethodPost, "/cluster", map[string]any{"name": flag(cmd, "name")})
		},
	}
	required(create, "name", "Cluster name")
	child(root, "cluster").AddCommand(create)
	for _, action := range []string{"destroy", "recover"} {
		command := &cobra.Command{
			Use:   action,
			Short: action + " cluster (explicit confirmation required)",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return c.call(cmd, http.MethodPost, "/cluster/"+action, map[string]any{"confirm": flag(cmd, "confirm")})
			},
		}
		required(command, "confirm", "Exact cluster ID")
		child(root, "cluster").AddCommand(command)
	}
	for _, action := range []string{"promote", "demote", "remove", "reboot", "shutdown", "restart-daemon"} {
		command := &cobra.Command{
			Use:   action + " ID",
			Short: action + " host",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				payload := map[string]any{"force": boolean(cmd, "force")}
				if cmd.Flags().Lookup("to") != nil {
					target := flag(cmd, "to")
					if (action == "promote" && target != "full" && target != "full-stable") || (action == "demote" && target != "full" && target != "pending") {
						return errors.New("invalid membership target")
					}
					payload["toStatus"] = target
				}
				if cmd.Flags().Lookup("evacuate") != nil {
					payload["evacuate"] = boolean(cmd, "evacuate")
				}
				return c.call(cmd, http.MethodPost, "/hosts/"+url.PathEscape(args[0])+"/"+action, payload)
			},
		}
		command.Flags().Bool("force", false, "Force the operation (server quorum invariants still apply)")
		if action == "promote" || action == "demote" {
			required(command, "to", "Target membership status")
		}
		if action == "reboot" || action == "shutdown" || action == "restart-daemon" {
			command.Flags().Bool("evacuate", false, "Move replicas before stopping the host")
		}
		child(root, "host").AddCommand(command)
	}
	assignment := &cobra.Command{
		Use:   "configuration ID",
		Short: "Assign a host configuration",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			version, _ := cmd.Flags().GetUint64("version")
			if version == 0 {
				return errors.New("configuration version must be positive")
			}
			return c.call(cmd, http.MethodPut, "/hosts/"+url.PathEscape(args[0])+"/configuration", map[string]any{"configurationId": flag(cmd, "configuration"), "configurationVersion": version})
		},
	}
	required(assignment, "configuration", "Configuration ID")
	versionFlag(assignment)
	child(root, "host").AddCommand(assignment)
	for _, action := range []struct{ group, name, method, path, suffix string }{
		{"removal", "clear", http.MethodDelete, "/removals/", ""},
		{"repo", "remove", http.MethodDelete, "/repos/", ""},
		{"repo", "refresh", http.MethodPost, "/repos/", "/refresh"},
		{"credential", "remove", http.MethodDelete, "/credentials/", ""},
		{"configuration", "refresh", http.MethodPost, "/configurations/", "/refresh"},
		{"task", "cancel", http.MethodPost, "/tasks/", "/cancel"},
		{"task", "retry", http.MethodPost, "/tasks/", "/retry"},
		{"enrollment", "approve", http.MethodPost, "/enrollment/", "/approve"},
		{"enrollment", "reject", http.MethodPost, "/enrollment/", "/reject"},
	} {
		child(root, action.group).AddCommand(&cobra.Command{
			Use:   action.name + " ID",
			Short: action.name + " record",
			Args:  cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return c.call(cmd, action.method, action.path+url.PathEscape(args[0])+action.suffix, nil)
			},
		})
	}
	child(child(root, "enrollment"), "rejection").AddCommand(&cobra.Command{
		Use:   "clear ID",
		Short: "Clear an enrollment rejection",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.call(cmd, http.MethodDelete, "/enrollment/rejections/"+url.PathEscape(args[0]), nil)
		},
	})
	for _, action := range []string{"request", "add"} {
		command := &cobra.Command{
			Use:   action,
			Short: "Request enrollment",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				payload := map[string]any{}
				for flagName, jsonName := range map[string]string{"discovery": "discoveryId", "address": "address", "contact": "contactHostId"} {
					if cmd.Flags().Lookup(flagName) != nil && flag(cmd, flagName) != "" {
						payload[jsonName] = flag(cmd, flagName)
					}
				}
				return c.call(cmd, http.MethodPost, "/enrollment/request", payload)
			},
		}
		command.Flags().String("discovery", "", "Discovery record ID")
		command.Flags().String("address", "", "Explicit peer HTTP endpoint")
		command.MarkFlagsOneRequired("discovery", "address")
		command.MarkFlagsMutuallyExclusive("discovery", "address")
		if action == "add" {
			required(command, "contact", "Full-member contact host ID")
		}
		child(root, "enrollment").AddCommand(command)
	}
	for _, action := range []string{"add", "update"} {
		count := 0
		use := action
		if action == "update" {
			count = 1
			use += " ID"
		}
		command := &cobra.Command{
			Use:   use,
			Short: action + " tracked repository",
			Args:  cobra.ExactArgs(count),
			RunE: func(cmd *cobra.Command, args []string) error {
				input, err := definition(cmd)
				if err != nil {
					return err
				}
				method, path := http.MethodPost, "/repos"
				if len(args) > 0 {
					method = http.MethodPatch
					path += "/" + url.PathEscape(args[0])
				}
				return c.call(cmd, method, path, input)
			},
		}
		required(command, "file", "JSON or YAML repository definition")
		child(root, "repo").AddCommand(command)
	}
	approve := &cobra.Command{
		Use:   "approve ID",
		Short: "Approve the current task review",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.call(cmd, http.MethodPost, "/tasks/"+url.PathEscape(args[0])+"/approve", map[string]any{"outputId": flag(cmd, "output")})
		},
	}
	required(approve, "output", "Current review output ID")
	child(root, "task").AddCommand(approve)
	c.addVersionedCommands(root)
	c.addHostJobs(root)
	c.addSecrets(root)
}
