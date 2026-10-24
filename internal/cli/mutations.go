package cli

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"nixmox/internal/client"
	"nixmox/internal/domain"
)

// mutation shares receipt identity and wait/detach semantics, while Cobra still
// owns each command's arguments, flags, help and validation.
func (o *options) mutation(cmd *cobra.Command, kind string, payload func(*cobra.Command, *client.Client, []string) (any, domain.Versions, error)) {
	var commandID string
	var wait, detach bool
	cmd.Flags().StringVar(&commandID, "command-id", "", "Stable ID to reuse after uncertain submission")
	cmd.Flags().BoolVar(&wait, "wait", false, "Wait for completion or required action")
	cmd.Flags().BoolVar(&detach, "detach", false, "Return after admission (the default)")
	cmd.MarkFlagsMutuallyExclusive("wait", "detach")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		api, err := o.api()
		if err != nil {
			return err
		}
		value, versions, err := payload(cmd, api, args)
		if err != nil {
			return err
		}
		data, err := json.Marshal(value)
		if err != nil {
			return err
		}
		identity, err := api.Identity(cmd.Context())
		if err != nil {
			return operationError(err)
		}
		id := domain.ID(commandID)
		if id == "" {
			id = domain.ID("cmd_" + uuid.NewString())
		}
		fmt.Fprintln(cmd.ErrOrStderr(), "command_id:", id)
		target := identity.NodeID
		switch p := value.(type) {
		case domain.TaskTarget:
			target = p.TaskID
		case domain.HostAssign:
			target = p.HostID
		case domain.HostTarget:
			target = p.HostID
		case domain.ForceFullMembership:
			target = p.HostID
		case domain.HostPower:
			target = p.HostID
		case domain.ClusterLeave:
			target = p.HostID
		case domain.HostRemove:
			target = p.HostID
		}
		receipt, err := api.Submit(cmd.Context(), domain.Command{ID: id, Type: kind, TargetID: target, ExpectedVersions: versions, Payload: data})
		if err != nil || !wait {
			return o.print(cmd, receipt, err)
		}
		fmt.Fprintln(cmd.ErrOrStderr(), "task_id:", *receipt.TaskID)
		current, err := api.Wait(cmd.Context(), *receipt.TaskID)
		if err = o.print(cmd, current, err); err != nil {
			return err
		}
		switch current.State {
		case domain.Succeeded:
			return nil
		case domain.Failed, domain.Cancelled:
			return &exitError{1, fmt.Errorf("task %s", current.State)}
		case domain.Blocked:
			return &exitError{4, fmt.Errorf("task requires action")}
		default:
			return &exitError{3, fmt.Errorf("task requires reconciliation")}
		}
	}
}

func (o *options) clusterCommand() *cobra.Command {
	group := &cobra.Command{Use: "cluster", Short: "Create a provisional cluster and enroll pending nodes"}
	group.AddCommand(&cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: o.read(func(cmd *cobra.Command, api *client.Client, _ []string) (any, error) {
		return api.Cluster(cmd.Context())
	})})
	create := &cobra.Command{Use: "create NAME", Args: cobra.ExactArgs(1)}
	o.mutation(create, "cluster.create", func(_ *cobra.Command, _ *client.Client, args []string) (any, domain.Versions, error) {
		return domain.ClusterCreate{Name: args[0]}, nil, nil
	})
	var address, name string
	join := &cobra.Command{Use: "join", Args: cobra.NoArgs, Short: "Submit signed CSRs and wait for administrator approval"}
	join.Flags().StringVar(&address, "address", "", "Cluster enrollment host:port or HTTP origin")
	join.Flags().StringVar(&name, "name", "", "Expected cluster name")
	join.MarkFlagRequired("address")
	join.MarkFlagRequired("name")
	o.mutation(join, "cluster.join", func(*cobra.Command, *client.Client, []string) (any, domain.Versions, error) {
		return domain.ClusterJoin{Address: address, ExpectedClusterName: name}, nil, nil
	})
	requests := &cobra.Command{Use: "requests", Short: "Review exact node keys before enrollment"}
	requests.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: o.read(func(cmd *cobra.Command, api *client.Client, _ []string) (any, error) {
		return api.Requests(cmd.Context())
	})}, &cobra.Command{Use: "show REQUEST", Args: cobra.ExactArgs(1), RunE: o.read(func(cmd *cobra.Command, api *client.Client, args []string) (any, error) {
		return api.Enrollment(cmd.Context(), domain.ID(args[0]))
	})})
	for _, decision := range []string{"approve", "reject"} {
		var version uint64
		command := &cobra.Command{Use: decision + " REQUEST", Args: cobra.ExactArgs(1)}
		command.Flags().Uint64Var(&version, "version", 0, "Reviewed join-request version")
		command.MarkFlagRequired("version")
		o.mutation(command, "cluster.request."+decision, func(_ *cobra.Command, _ *client.Client, args []string) (any, domain.Versions, error) {
			if version == 0 {
				return nil, nil, fmt.Errorf("--version must be positive")
			}
			return domain.JoinDecisionPayload{RequestID: domain.ID(args[0])}, domain.Versions{"join_request:" + args[0]: domain.Version(version)}, nil
		})
		requests.AddCommand(command)
	}
	group.AddCommand(create, join, requests)
	return group
}
