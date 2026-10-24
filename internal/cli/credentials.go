package cli

import (
	"fmt"
	"os"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"nixmox/internal/client"
	"nixmox/internal/domain"
	"nixmox/internal/engine"
)

func resolveCredential(cmd *cobra.Command, api *client.Client, selector string) (domain.Credential, error) {
	return resolveSelector(cmd.Context(), selector, domain.PageRequest{}, api.Credentials, func(c domain.Credential) (domain.ID, string) { return c.ID, c.Name })
}

func (o *options) credentialCommands() *cobra.Command {
	group := &cobra.Command{Use: "credentials", Short: "Manage holder-local credentials; reads never return secret values"}
	var kind, node, file, commandID string
	var wait bool
	add := &cobra.Command{Use: "import NAME", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		api, err := o.api()
		if err != nil {
			return err
		}
		host, err := resolveSelector(cmd.Context(), node, domain.PageRequest{}, api.Hosts, func(h domain.Host) (domain.ID, string) { return h.ID, h.Name })
		if err != nil {
			return operationError(err)
		}
		input, err := os.Open(file)
		if err != nil {
			return fmt.Errorf("credential input file unavailable")
		}
		defer input.Close()
		id := domain.ID(commandID)
		if id == "" {
			id = domain.ID("cmd_" + uuid.NewString())
		}
		fmt.Fprintln(cmd.ErrOrStderr(), "command_id:", id)
		receipt, err := api.ImportCredential(cmd.Context(), engine.SecretIngress{CommandID: id, HolderID: host.ID, Name: args[0], Kind: kind}, input)
		if err != nil || !wait {
			return o.print(cmd, receipt, err)
		}
		current, err := api.Wait(cmd.Context(), *receipt.TaskID)
		if err := o.print(cmd, current, err); err != nil {
			return err
		}
		if current.State != domain.Succeeded {
			return &exitError{1, fmt.Errorf("credential import %s", current.State)}
		}
		return nil
	}}
	add.Flags().StringVar(&kind, "kind", "", "Credential kind")
	add.Flags().StringVar(&node, "node", "", "Full member holding the value")
	add.Flags().StringVar(&file, "file", "", "Private input file; never pass a value as a flag")
	add.Flags().StringVar(&commandID, "command-id", "", "Stable import ID for receipt reconciliation")
	add.Flags().BoolVar(&wait, "wait", false, "Wait for the import task")
	for _, flag := range []string{"kind", "node", "file"} {
		add.MarkFlagRequired(flag)
	}
	group.AddCommand(add)
	for _, name := range []string{"list", "show"} {
		command := &cobra.Command{Use: name + " CREDENTIAL", Args: cobra.ExactArgs(1)}
		var kind string
		var page domain.PageRequest
		if name == "list" {
			command.Use, command.Args = "list", cobra.NoArgs
			pageFlags(command, &page)
			command.Flags().StringVar(&kind, "kind", "", "Filter credential kind")
		}
		command.RunE = o.read(func(cmd *cobra.Command, api *client.Client, args []string) (any, error) {
			if name == "list" {
				page.Filters = map[string]string{}
				if kind != "" {
					page.Filters["kind"] = kind
				}
				return api.Credentials(cmd.Context(), page)
			}
			return resolveCredential(cmd, api, args[0])
		})
		group.AddCommand(command)
	}
	var repository string
	check := &cobra.Command{Use: "check CREDENTIAL", Args: cobra.ExactArgs(1)}
	check.Flags().StringVar(&repository, "repository", "", "Repository for a Git read-access check")
	o.mutation(check, "credential.check", func(cmd *cobra.Command, api *client.Client, args []string) (any, domain.Versions, error) {
		credential, err := resolveCredential(cmd, api, args[0])
		if err != nil {
			return nil, nil, err
		}
		p := domain.CredentialCheckPayload{CredentialID: credential.ID}
		if repository != "" {
			repo, err := resolveRepository(cmd, api, repository)
			if err != nil {
				return nil, nil, err
			}
			p.RepositoryID = &repo.ID
		}
		return p, nil, nil
	})
	var version uint64
	remove := &cobra.Command{Use: "remove CREDENTIAL", Args: cobra.ExactArgs(1)}
	remove.Flags().Uint64Var(&version, "expected-version", 0, "Reviewed credential version")
	remove.MarkFlagRequired("expected-version")
	o.mutation(remove, "credential.remove", func(cmd *cobra.Command, api *client.Client, args []string) (any, domain.Versions, error) {
		credential, err := resolveCredential(cmd, api, args[0])
		if err != nil {
			return nil, nil, err
		}
		return domain.CredentialTarget{CredentialID: credential.ID}, domain.Versions{"credential:" + string(credential.ID): domain.Version(version)}, nil
	})
	group.AddCommand(check, remove)
	return group
}

func (o *options) repositoryCredentialCommand() *cobra.Command {
	group := &cobra.Command{Use: "credential"}
	var username, hostKey string
	var version uint64
	set := &cobra.Command{Use: "set REPOSITORY CREDENTIAL", Args: cobra.ExactArgs(2)}
	set.Flags().StringVar(&username, "username", "git", "Git authentication username")
	set.Flags().StringVar(&hostKey, "host-key", "", "Verified Git SSH server public key; never a private key")
	set.Flags().Uint64Var(&version, "expected-version", 0, "Reviewed repository version")
	set.MarkFlagRequired("expected-version")
	o.mutation(set, "repository.credential.set", func(cmd *cobra.Command, api *client.Client, args []string) (any, domain.Versions, error) {
		repository, err := resolveRepository(cmd, api, args[0])
		if err != nil {
			return nil, nil, err
		}
		credential, err := resolveCredential(cmd, api, args[1])
		if err != nil {
			return nil, nil, err
		}
		return domain.RepositoryCredentialSet{RepositoryID: repository.ID, CredentialID: credential.ID, Username: username, HostKey: hostKey}, domain.Versions{"repository:" + string(repository.ID): domain.Version(version)}, nil
	})
	group.AddCommand(set)
	return group
}
