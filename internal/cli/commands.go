package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/spf13/cobra"

	"nixmox/internal/client"
	"nixmox/internal/domain"
)

func (o *options) identityCommand() *cobra.Command {
	group := &cobra.Command{Use: "identity", Short: "Inspect this daemon's identity"}
	group.AddCommand(&cobra.Command{Use: "show", Args: cobra.NoArgs, RunE: o.read(func(cmd *cobra.Command, api *client.Client, _ []string) (any, error) {
		return api.Identity(cmd.Context())
	})})
	return group
}

func (o *options) hostCommands() *cobra.Command {
	group := &cobra.Command{Use: "hosts", Short: "Read host inventory and live measurements"}
	group.AddCommand(o.assignmentCommand())
	group.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: o.read(func(cmd *cobra.Command, api *client.Client, _ []string) (any, error) {
		return api.Hosts(cmd.Context())
	})})
	refresh := &cobra.Command{Use: "refresh HOST", Args: cobra.ExactArgs(1), Short: "Refresh observed connectivity at the selected executor"}
	o.mutation(refresh, "host.refresh", func(cmd *cobra.Command, api *client.Client, args []string) (any, domain.Versions, error) {
		id, err := resolveHost(cmd, api, args[0])
		return domain.HostTarget{HostID: id}, nil, err
	})
	group.AddCommand(refresh)
	for _, name := range []string{"leave", "remove", "shutdown", "reboot", "force-full-membership", "demote"} {
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
			case "force-full-membership", "demote":
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
		group.AddCommand(&cobra.Command{Use: name + " HOST", Args: cobra.ExactArgs(1), RunE: o.read(func(cmd *cobra.Command, api *client.Client, args []string) (any, error) {
			id, err := resolveHost(cmd, api, args[0])
			if err != nil {
				return nil, err
			}
			if name == "telemetry" {
				return api.Telemetry(cmd.Context(), id)
			}
			return api.Host(cmd.Context(), id)
		})})
	}
	return group
}

func resolveHost(cmd *cobra.Command, api *client.Client, selector string) (domain.ID, error) {
	host, err := resolveSelector(cmd.Context(), selector, domain.PageRequest{}, api.Hosts, func(h domain.Host) (domain.ID, string) { return h.ID, h.Name })
	return host.ID, err
}

// IDs take precedence; names must be unique across the entire result, not one page.
func resolveSelector[T any](ctx context.Context, selector string, page domain.PageRequest, list func(context.Context, ...domain.PageRequest) (domain.Page[T], error), identity func(T) (domain.ID, string)) (T, error) {
	var selected, zero T
	matches := 0
	page.Limit = domain.PageLimit
	for {
		result, err := list(ctx, page)
		if err != nil {
			return zero, err
		}
		for _, item := range result.Items {
			id, name := identity(item)
			if string(id) == selector {
				return item, nil
			}
			if name == selector {
				selected = item
				matches++
			}
		}
		if result.NextCursor == nil {
			break
		}
		page.Cursor = *result.NextCursor
	}
	if matches != 1 {
		return zero, &domain.Error{Code: "INVALID_INPUT", Message: "Selector is missing or ambiguous; use its ID"}
	}
	return selected, nil
}

func (o *options) discoveryCommands() *cobra.Command {
	group := &cobra.Command{Use: "discovery", Short: "Inspect expiring management-network hints"}
	group.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: o.read(func(cmd *cobra.Command, api *client.Client, _ []string) (any, error) {
		return api.Discoveries(cmd.Context())
	})})
	search := &cobra.Command{Use: "search", Args: cobra.NoArgs, Short: "Search for 30 seconds; passive discovery continues afterward"}
	o.mutation(search, "discovery.search", func(*cobra.Command, *client.Client, []string) (any, domain.Versions, error) {
		return domain.Empty{}, nil, nil
	})
	group.AddCommand(search)
	return group
}
func (o *options) networkCommands() *cobra.Command {
	group := &cobra.Command{Use: "network", Short: "Observe the addressed node's management network"}
	group.AddCommand(&cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: o.read(func(cmd *cobra.Command, api *client.Client, _ []string) (any, error) {
		return api.Network(cmd.Context())
	})})
	check := &cobra.Command{Use: "check", Args: cobra.NoArgs, Short: "Persist and execute a network observation task"}
	o.mutation(check, "network.check", func(*cobra.Command, *client.Client, []string) (any, domain.Versions, error) {
		return domain.Empty{}, nil, nil
	})
	group.AddCommand(check)
	return group
}

func (o *options) taskCommands() *cobra.Command {
	group := &cobra.Command{Use: "tasks", Short: "Read durable execution state and logs"}
	var page domain.PageRequest
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: o.read(func(cmd *cobra.Command, api *client.Client, _ []string) (any, error) {
		return api.Tasks(cmd.Context(), page)
	})}
	pageFlags(list, &page)
	group.AddCommand(list, &cobra.Command{Use: "show TASK", Args: cobra.ExactArgs(1), RunE: o.read(func(cmd *cobra.Command, api *client.Client, args []string) (any, error) {
		return api.Task(cmd.Context(), domain.ID(args[0]))
	})})
	watch := &cobra.Command{Use: "watch TASK", Args: cobra.ExactArgs(1), Short: "Watch task state until terminal; Ctrl-C only stops watching"}
	watch.RunE = func(cmd *cobra.Command, args []string) error {
		return o.watchTask(cmd, args[0], false, 0)
	}
	group.AddCommand(watch)
	var offset int64
	var logLimit int
	var follow bool
	logs := &cobra.Command{Use: "logs TASK", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if follow {
			return o.watchTask(cmd, args[0], true, offset)
		}
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
	logs.Flags().BoolVarP(&follow, "follow", "f", false, "Follow logs through completion; Ctrl-C only stops following")
	logs.Flags().Int64Var(&offset, "offset", 0, "Absolute log byte offset")
	logs.Flags().IntVar(&logLimit, "limit", domain.LogReadDefault, "Maximum log bytes (1–1048576)")
	group.AddCommand(logs)
	for _, action := range []string{"cancel", "retry"} {
		var version uint64
		command := &cobra.Command{Use: action + " TASK", Args: cobra.ExactArgs(1)}
		command.Flags().Uint64Var(&version, "version", 0, "Reviewed task lifecycle version")
		command.Flags().Uint64Var(&version, "expected-version", 0, "Reviewed task lifecycle version (alias for --version)")
		command.MarkFlagsOneRequired("version", "expected-version")
		command.MarkFlagsMutuallyExclusive("version", "expected-version")
		o.mutation(command, "task."+action, func(_ *cobra.Command, _ *client.Client, args []string) (any, domain.Versions, error) {
			if version == 0 {
				return nil, nil, &domain.Error{Code: "INVALID_INPUT", Message: "Task version must be positive"}
			}
			return domain.TaskTarget{TaskID: domain.ID(args[0])}, domain.Versions{"task:" + args[0]: domain.Version(version)}, nil
		})
		group.AddCommand(command)
	}
	return group
}

func (o *options) watchTask(cmd *cobra.Command, id string, logs bool, offset int64) error {
	api, err := o.api()
	if err != nil {
		return err
	}
	return operationError(api.WatchTask(cmd.Context(), domain.ID(id), logs, offset, func(data json.RawMessage) error {
		if !logs || o.json {
			return o.print(cmd, data, nil)
		}
		var chunk domain.LogChunk
		if err := json.Unmarshal(data, &chunk); err != nil {
			return err
		}
		decoded, err := base64.StdEncoding.DecodeString(chunk.DataBase64)
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write(decoded)
		return err
	}))
}

func (o *options) receiptCommand() *cobra.Command {
	group := &cobra.Command{Use: "commands", Short: "Reconcile a submitted command's identity"}
	group.AddCommand(&cobra.Command{Use: "show COMMAND", Args: cobra.ExactArgs(1), RunE: o.read(func(cmd *cobra.Command, api *client.Client, args []string) (any, error) {
		return api.Receipt(cmd.Context(), domain.ID(args[0]))
	})})
	return group
}
