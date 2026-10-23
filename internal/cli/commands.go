package cli

import (
	"encoding/base64"
	"github.com/spf13/cobra"

	"nixmox/internal/client"
	"nixmox/internal/domain"
)

func (o *options) identityCommand() *cobra.Command {
	group := &cobra.Command{Use: "identity", Short: "Inspect this daemon's identity"}
	group.AddCommand(&cobra.Command{Use: "show", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		api, err := o.api()
		if err != nil {
			return err
		}
		value, err := api.Identity(cmd.Context())
		return o.print(cmd, value, err)
	}})
	return group
}

func (o *options) hostCommands() *cobra.Command {
	group := &cobra.Command{Use: "hosts", Short: "Read host inventory and live measurements"}
	group.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		api, err := o.api()
		if err != nil {
			return err
		}
		value, err := api.Hosts(cmd.Context())
		return o.print(cmd, value, err)
	}})
	refresh := &cobra.Command{Use: "refresh HOST", Args: cobra.ExactArgs(1), Short: "Refresh observed connectivity at the selected executor"}
	o.mutation(refresh, "host.refresh", func(cmd *cobra.Command, api *client.Client, args []string) (any, domain.Versions, error) {
		id, err := resolveHost(cmd, api, args[0])
		return domain.HostTarget{HostID: id}, nil, err
	})
	group.AddCommand(refresh)
	for _, name := range []string{"leave", "remove", "shutdown", "reboot", "force-full-membership"} {
		var confirm string
		var force bool
		action := &cobra.Command{Use: name + " HOST", Args: cobra.ExactArgs(1)}
		if name != "leave" {
			action.Flags().StringVar(&confirm, "confirm-host", "", "Exact host name or ID reviewed for this action")
			action.MarkFlagRequired("confirm-host")
		}
		if name == "remove" {
			action.Flags().BoolVar(&force, "force", false, "Revoke membership even when the target is offline")
			action.MarkFlagRequired("force")
		}
		kind := "host." + name
		if name == "force-full-membership" {
			kind = "host.force_full_membership"
			action.Short = "Force full membership and routed storage eligibility without deployment verification"
		}
		if name == "leave" {
			kind = "cluster.leave"
		}
		o.mutation(action, kind, func(cmd *cobra.Command, api *client.Client, args []string) (any, domain.Versions, error) {
			id, err := resolveHost(cmd, api, args[0])
			if err != nil {
				return nil, nil, err
			}
			host, err := api.Host(cmd.Context(), id)
			if err != nil {
				return nil, nil, err
			}
			if host.MembershipEpoch == nil {
				return nil, nil, &domain.Error{Code: "INVALID_INPUT", Message: "Host is not enrolled"}
			}
			versions := domain.Versions{"host:" + string(id): host.Version}
			switch name {
			case "force-full-membership":
				return domain.ForceFullMembership{HostID: id, MembershipEpoch: *host.MembershipEpoch, ConfirmHost: confirm}, versions, nil
			case "leave":
				return domain.ClusterLeave{HostID: id, MembershipEpoch: *host.MembershipEpoch}, versions, nil
			case "remove":
				return domain.HostRemove{HostID: id, MembershipEpoch: *host.MembershipEpoch, Force: force, ConfirmHost: confirm}, versions, nil
			default:
				return domain.HostPower{HostID: id, ConfirmHost: confirm, MembershipEpoch: *host.MembershipEpoch}, versions, nil
			}
		})
		group.AddCommand(action)
	}
	for _, name := range []string{"show", "endpoints", "telemetry"} {
		group.AddCommand(&cobra.Command{Use: name + " HOST", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			api, err := o.api()
			if err != nil {
				return err
			}
			id, err := resolveHost(cmd, api, args[0])
			if err != nil {
				return operationError(err)
			}
			if name == "telemetry" {
				value, err := api.Telemetry(cmd.Context(), id)
				return o.print(cmd, value, err)
			}
			value, err := api.Host(cmd.Context(), id)
			return o.print(cmd, value, err)
		}})
	}
	return group
}

func resolveHost(cmd *cobra.Command, api *client.Client, selector string) (domain.ID, error) {
	page, err := api.Hosts(cmd.Context())
	if err != nil {
		return "", err
	}
	var matches []domain.ID
	for _, host := range page.Items {
		if string(host.ID) == selector {
			return host.ID, nil
		}
		if host.Name == selector {
			matches = append(matches, host.ID)
		}
	}
	if len(matches) != 1 {
		return "", &domain.Error{Code: "INVALID_INPUT", Message: "Host selector is missing or ambiguous; use its ID"}
	}
	return matches[0], nil
}

func (o *options) discoveryCommands() *cobra.Command {
	group := &cobra.Command{Use: "discovery", Short: "Inspect expiring management-network hints"}
	group.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		api, err := o.api()
		if err != nil {
			return err
		}
		value, err := api.Discoveries(cmd.Context())
		return o.print(cmd, value, err)
	}})
	search := &cobra.Command{Use: "search", Args: cobra.NoArgs, Short: "Search for 30 seconds; passive discovery continues afterward"}
	o.mutation(search, "discovery.search", func(*cobra.Command, *client.Client, []string) (any, domain.Versions, error) {
		return domain.Empty{}, nil, nil
	})
	group.AddCommand(search)
	return group
}
func (o *options) networkCommands() *cobra.Command {
	group := &cobra.Command{Use: "network", Short: "Observe the addressed node's management network"}
	group.AddCommand(&cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		api, err := o.api()
		if err != nil {
			return err
		}
		value, err := api.Network(cmd.Context())
		return o.print(cmd, value, err)
	}})
	check := &cobra.Command{Use: "check", Args: cobra.NoArgs, Short: "Persist and execute a network observation task"}
	o.mutation(check, "network.check", func(*cobra.Command, *client.Client, []string) (any, domain.Versions, error) {
		return domain.Empty{}, nil, nil
	})
	group.AddCommand(check)
	return group
}

func (o *options) taskCommands() *cobra.Command {
	group := &cobra.Command{Use: "tasks", Short: "Read durable execution state and logs"}
	var cursor string
	var limit int
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		api, err := o.api()
		if err != nil {
			return err
		}
		value, err := api.Tasks(cmd.Context(), domain.PageRequest{Cursor: cursor, Limit: limit})
		return o.print(cmd, value, err)
	}}
	list.Flags().StringVar(&cursor, "cursor", "", "Cursor from the previous page")
	list.Flags().IntVar(&limit, "limit", domain.PageDefault, "Page size (1–500)")
	group.AddCommand(list, &cobra.Command{Use: "show TASK", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		api, err := o.api()
		if err != nil {
			return err
		}
		value, err := api.Task(cmd.Context(), domain.ID(args[0]))
		return o.print(cmd, value, err)
	}})
	var offset int64
	var logLimit int
	logs := &cobra.Command{Use: "logs TASK", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		api, err := o.api()
		if err != nil {
			return err
		}
		chunk, err := api.Logs(cmd.Context(), domain.ID(args[0]), offset, logLimit)
		if o.json || err != nil {
			return o.print(cmd, chunk, err)
		}
		data, err := base64.StdEncoding.DecodeString(chunk.DataBase64)
		if err != nil {
			return operationError(err)
		}
		_, err = cmd.OutOrStdout().Write(data)
		return operationError(err)
	}}
	logs.Flags().Int64Var(&offset, "offset", 0, "Absolute log byte offset")
	logs.Flags().IntVar(&logLimit, "limit", domain.LogReadDefault, "Maximum log bytes (1–1048576)")
	group.AddCommand(logs)
	return group
}

func (o *options) receiptCommand() *cobra.Command {
	group := &cobra.Command{Use: "commands", Short: "Reconcile a submitted command's identity"}
	group.AddCommand(&cobra.Command{Use: "show COMMAND", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		api, err := o.api()
		if err != nil {
			return err
		}
		value, err := api.Receipt(cmd.Context(), domain.ID(args[0]))
		return o.print(cmd, value, err)
	}})
	return group
}
