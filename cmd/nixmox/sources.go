package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"nixmox/internal/client"
	"nixmox/internal/domain"
	"strings"
)

func resolveRepository(ctx context.Context, c *client.Client, name string) (domain.Repository, error) {
	if strings.HasPrefix(name, "r_") {
		var r domain.Repository
		err := c.Read(ctx, "/api/v1/repositories/"+name, &r)
		if err == nil {
			return r, nil
		}
	}
	cursor := ""
	for {
		var page domain.Page[domain.Repository]
		if err := c.Read(ctx, "/api/v1/repositories?limit=500&cursor="+cursor, &page); err != nil {
			return domain.Repository{}, err
		}
		for _, r := range page.Items {
			if r.Name == name || r.ID == name {
				return r, nil
			}
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	return domain.Repository{}, domain.Err("NOT_FOUND", "Repository not found.", 404)
}
func repositoryCommand(ctx context.Context, c *client.Client, args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		return 2
	}
	verb := args[0]
	rest := args[1:]
	if verb == "remote" || verb == "credential" {
		if len(rest) == 0 || rest[0] != "set" {
			return 2
		}
		verb += ".set"
		rest = rest[1:]
	}
	name := ""
	if verb != "list" {
		if len(rest) == 0 {
			fmt.Fprintln(errOut, "A repository name or ID is required.")
			return 2
		}
		name = rest[0]
		rest = rest[1:]
	}
	f := flags("repositories "+verb, errOut)
	asJSON := f.Bool("json", false, "machine-readable output")
	wait := f.Bool("wait", false, "wait for task completion")
	commandID := f.String("command-id", "", "stable retry command ID")
	remote := f.String("url", "", "Git remote URL")
	branch := f.String("upstream", "", "upstream branch")
	credential := f.String("credential-ref", "", "app-managed credential reference")
	version := f.Int64("expected-version", 0, "reviewed repository version")
	from := f.String("from", "", "exact base commit")
	to := f.String("to", "", "exact candidate commit")
	if f.Parse(rest) != nil || f.NArg() != 0 {
		return 2
	}
	if verb == "list" {
		var page domain.Page[domain.Repository]
		if err := c.Read(ctx, "/api/v1/repositories", &page); err != nil {
			return exitError(errOut, err)
		}
		if *asJSON {
			printJSON(out, page)
		} else {
			for _, r := range page.Items {
				fmt.Fprintf(out, "%s  %s  %s  %s\n", r.ID, r.Name, r.Upstream.Branch, r.Remote.URL)
			}
		}
		return 0
	}
	var repo domain.Repository
	if verb != "import" {
		var err error
		repo, err = resolveRepository(ctx, c, name)
		if err != nil {
			return exitError(errOut, err)
		}
	}
	switch verb {
	case "show":
		printJSON(out, repo)
		return 0
	case "refs":
		printJSON(out, repo.Refs)
		return 0
	case "diff":
		var result any
		if err := c.Read(ctx, "/api/v1/repositories/"+repo.ID+"/diff?from="+*from+"&to="+*to, &result); err != nil {
			return exitError(errOut, err)
		}
		printJSON(out, result)
		return 0
	}
	identity, err := c.Identity(ctx)
	if err != nil {
		return exitError(errOut, err)
	}
	cmd := domain.Command{ID: *commandID, Type: "repository." + verb, TargetID: identity.NodeID, ExpectedVersions: map[string]int64{}}
	if cmd.ID == "" {
		cmd.ID = domain.NewID("cmd_")
	}
	p := map[string]any{"repository_id": repo.ID}
	switch verb {
	case "import":
		p = map[string]any{"name": name, "remote_url": *remote, "upstream_branch": *branch}
		if *credential != "" {
			p["credential_ref"] = *credential
		}
	case "fetch":
	case "remote.set":
		p["url"] = *remote
		if *branch != "" {
			p["upstream_branch"] = *branch
		}
	case "credential.set":
		p["credential_ref"] = nil
		if *credential != "" {
			p["credential_ref"] = *credential
		}
	default:
		fmt.Fprintln(errOut, "Unknown repository command.")
		return 2
	}
	if verb == "remote.set" || verb == "credential.set" {
		if *version == 0 {
			*version = repo.Version
		}
		cmd.ExpectedVersions["repository:"+repo.ID] = *version
	}
	cmd.Payload, _ = json.Marshal(p)
	return submitCommand(ctx, c, cmd, *wait, *asJSON, out, errOut)
}
