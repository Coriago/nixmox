package cli

import (
	"github.com/spf13/cobra"
	"nixmox/internal/client"
	"nixmox/internal/domain"
)

func resolveRepository(cmd *cobra.Command, api *client.Client, selector string) (domain.Repository, error) {
	return resolveSelector(cmd.Context(), selector, domain.PageRequest{}, api.Repositories, func(r domain.Repository) (domain.ID, string) { return r.ID, r.Name })
}

func (o *options) repositoryCommands() *cobra.Command {
	group := &cobra.Command{Use: "repositories", Short: "Import remote-backed source and inspect accepted refs"}
	var remote, branch, credential, username, hostKey string
	add := &cobra.Command{Use: "import NAME", Args: cobra.ExactArgs(1)}
	add.Flags().StringVar(&remote, "url", "", "Remote URL (no embedded secrets)")
	add.Flags().StringVar(&branch, "upstream", "main", "Upstream branch")
	add.Flags().StringVar(&credential, "credential", "", "Managed read credential reference")
	add.MarkFlagRequired("url")
	add.Flags().StringVar(&username, "username", "git", "Git authentication username")
	add.Flags().StringVar(&hostKey, "host-key", "", "Verified Git SSH server public key")
	o.mutation(add, "repository.import", func(cmd *cobra.Command, api *client.Client, args []string) (any, domain.Versions, error) {
		p := domain.RepositoryImport{Name: args[0], RemoteURL: remote, UpstreamBranch: branch, Username: username, HostKey: hostKey}
		if credential != "" {
			value, err := resolveCredential(cmd, api, credential)
			if err != nil {
				return nil, nil, err
			}
			p.CredentialRef = &value.ID
		}
		return p, nil, nil
	})
	fetch := &cobra.Command{Use: "fetch REPOSITORY", Args: cobra.ExactArgs(1)}
	o.mutation(fetch, "repository.fetch", func(cmd *cobra.Command, api *client.Client, args []string) (any, domain.Versions, error) {
		repo, err := resolveRepository(cmd, api, args[0])
		return domain.RepositoryTarget{RepositoryID: repo.ID}, nil, err
	})
	group.AddCommand(add, fetch)
	for _, name := range []string{"list", "show", "refs"} {
		command := &cobra.Command{Use: name + " REPOSITORY", Args: cobra.ExactArgs(1)}
		if name == "list" {
			command.Use = "list"
			command.Args = cobra.NoArgs
		}
		var page domain.PageRequest
		if name != "show" {
			pageFlags(command, &page)
		}
		command.RunE = o.read(func(cmd *cobra.Command, api *client.Client, args []string) (any, error) {
			if name == "list" {
				return api.Repositories(cmd.Context(), page)
			}
			repo, err := resolveRepository(cmd, api, args[0])
			if err != nil {
				return nil, err
			}
			if name == "refs" {
				return api.References(cmd.Context(), repo.ID, page)
			}
			return repo, nil
		})
		group.AddCommand(command)
	}
	remoteGroup := &cobra.Command{Use: "remote", Short: "Change reviewed remote settings without fetching or publishing"}
	var newURL, newBranch string
	var version uint64
	set := &cobra.Command{Use: "set REPOSITORY", Args: cobra.ExactArgs(1)}
	set.Flags().StringVar(&newURL, "url", "", "New remote URL")
	set.Flags().StringVar(&newBranch, "upstream", "", "Change upstream branch (omission preserves it)")
	set.Flags().Uint64Var(&version, "expected-version", 0, "Reviewed repository version")
	set.MarkFlagRequired("url")
	set.MarkFlagRequired("expected-version")
	o.mutation(set, "repository.remote.set", func(cmd *cobra.Command, api *client.Client, args []string) (any, domain.Versions, error) {
		if version == 0 {
			return nil, nil, &domain.Error{Code: "INVALID_INPUT", Message: "Repository version must be positive"}
		}
		repo, err := resolveRepository(cmd, api, args[0])
		if err != nil {
			return nil, nil, err
		}
		p := domain.RepositoryRemoteSet{RepositoryID: repo.ID, URL: newURL}
		if cmd.Flags().Changed("upstream") {
			p.UpstreamBranch = &newBranch
		}
		return p, domain.Versions{"repository:" + string(repo.ID): domain.Version(version)}, nil
	})
	remoteGroup.AddCommand(set)
	group.AddCommand(remoteGroup, o.repositoryCredentialCommand())
	return group
}

func (o *options) configurationCommands() *cobra.Command {
	group := &cobra.Command{Use: "configurations", Short: "Inspect configuration catalogs at accepted or explicit cached revisions"}
	var repository, ref, commit string
	var page domain.PageRequest
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: o.read(func(cmd *cobra.Command, api *client.Client, _ []string) (any, error) {
		page.Filters = map[string]string{}
		if repository != "" {
			repo, err := resolveRepository(cmd, api, repository)
			if err != nil {
				return nil, err
			}
			page.Filters["repository_id"] = string(repo.ID)
		}
		if ref != "" {
			page.Filters["ref_id"] = ref
		}
		if commit != "" {
			page.Filters["commit"] = commit
		}
		return api.Configurations(cmd.Context(), page)
	})}
	list.Flags().StringVar(&repository, "repository", "", "Repository ID or unambiguous name")
	list.Flags().StringVar(&ref, "ref", "", "Accepted reference ID (requires --repository)")
	list.Flags().StringVar(&commit, "commit", "", "Exact cached commit (requires --repository)")
	pageFlags(list, &page)
	list.MarkFlagsMutuallyExclusive("ref", "commit")
	group.AddCommand(list, &cobra.Command{Use: "show CONFIGURATION", Args: cobra.ExactArgs(1), RunE: o.read(func(cmd *cobra.Command, api *client.Client, args []string) (any, error) {
		return api.Configuration(cmd.Context(), domain.ID(args[0]))
	})})
	return group
}

func (o *options) assignmentCommand() *cobra.Command {
	var repository, configuration, ref string
	var hostVersion, configVersion uint64
	cmd := &cobra.Command{Use: "assign HOST", Args: cobra.ExactArgs(1), Short: "Change desired configuration, never running-system evidence"}
	cmd.Flags().StringVar(&repository, "repository", "", "Repository ID or name")
	cmd.Flags().StringVar(&configuration, "configuration", "", "Configuration name")
	cmd.Flags().StringVar(&ref, "ref", "", "Accepted reference ID (defaults to upstream)")
	cmd.Flags().Uint64Var(&hostVersion, "expected-version", 0, "Reviewed host version")
	cmd.Flags().Uint64Var(&configVersion, "configuration-version", 0, "Reviewed configuration version")
	for _, flag := range []string{"repository", "configuration", "expected-version", "configuration-version"} {
		cmd.MarkFlagRequired(flag)
	}
	o.mutation(cmd, "host.assign", func(cmd *cobra.Command, api *client.Client, args []string) (any, domain.Versions, error) {
		host, err := resolveHost(cmd, api, args[0])
		if err != nil {
			return nil, nil, err
		}
		repo, err := resolveRepository(cmd, api, repository)
		if err != nil {
			return nil, nil, err
		}
		page := domain.PageRequest{Limit: domain.PageLimit, Filters: map[string]string{"repository_id": string(repo.ID)}}
		if ref != "" {
			page.Filters["ref_id"] = ref
		}
		selected, err := resolveSelector(cmd.Context(), configuration, page, api.Configurations, func(c domain.Configuration) (domain.ID, string) { return c.ID, c.Name })
		if err != nil {
			return nil, nil, err
		}
		if selected.ID == "" || hostVersion == 0 || configVersion == 0 {
			return nil, nil, &domain.Error{Code: "INVALID_INPUT", Message: "Configuration and positive reviewed versions are required"}
		}
		p := domain.HostAssign{HostID: host, RepositoryID: repo.ID, Configuration: selected.Name}
		if ref != "" {
			id := domain.ID(ref)
			p.RefID = &id
		}
		return p, domain.Versions{"host:" + string(host): domain.Version(hostVersion), "configuration:" + string(selected.ID): domain.Version(configVersion)}, nil
	})
	return cmd
}
