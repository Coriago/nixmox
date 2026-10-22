package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"nixmox/internal/client"
	"nixmox/internal/domain"
	"strings"
)

func resolveHost(ctx context.Context, c *client.Client, name string) (domain.Host, error) {
	if strings.HasPrefix(name, "n_") && domain.SafeID.MatchString(name) {
		var h domain.Host
		err := c.Read(ctx, "/api/v1/hosts/"+name, &h)
		return h, err
	}
	next := ""
	var found *domain.Host
	for {
		var page domain.Page[domain.Host]
		if err := c.Read(ctx, "/api/v1/hosts?limit=500&cursor="+url.QueryEscape(next), &page); err != nil {
			return domain.Host{}, err
		}
		for _, h := range page.Items {
			if h.Name == name || h.ID == name {
				if found != nil {
					return domain.Host{}, domain.Err("AMBIGUOUS_HOST", "Host name is not unique; use its ID.", 409)
				}
				copy := h
				found = &copy
			}
		}
		if page.NextCursor == nil {
			break
		}
		next = *page.NextCursor
	}
	if found == nil {
		return domain.Host{}, domain.Err("NOT_FOUND", "Host not found.", 404)
	}
	return *found, nil
}
func inventoryCommand(ctx context.Context, c *client.Client, args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		return 2
	}
	verb := args[0]
	rest := args[1:]
	name := ""
	if verb != "list" {
		if len(rest) == 0 {
			return 2
		}
		name = rest[0]
		rest = rest[1:]
	}
	f := flags("hosts "+verb, errOut)
	asJSON := f.Bool("json", false, "machine-readable output")
	wait := f.Bool("wait", false, "wait for task")
	repoName := f.String("repository", "", "repository name or ID")
	configuration := f.String("configuration", "", "configuration name or ID")
	ref := f.String("ref", "", "configuration ref ID")
	version := f.Int64("expected-version", 0, "reviewed host version")
	commandID := f.String("command-id", "", "stable retry identity")
	if f.Parse(rest) != nil || f.NArg() != 0 {
		return 2
	}
	if verb == "list" {
		var page domain.Page[domain.Host]
		if err := c.Read(ctx, "/api/v1/hosts", &page); err != nil {
			return exitError(errOut, err)
		}
		if *asJSON {
			printJSON(out, page)
		} else {
			for _, h := range page.Items {
				fmt.Fprintf(out, "%s  %s  %s  %s  %s\n", h.ID, h.Name, h.Membership, h.Environment, h.Reachability)
			}
		}
		return 0
	}
	h, err := resolveHost(ctx, c, name)
	if err != nil {
		return exitError(errOut, err)
	}
	switch verb {
	case "show":
		printJSON(out, h)
		return 0
	case "endpoints":
		printJSON(out, map[string]any{"endpoints": h.Endpoints, "connectivity": h.Connectivity})
		return 0
	case "assign":
	default:
		return 2
	}
	r, err := resolveRepository(ctx, c, *repoName)
	if err != nil {
		return exitError(errOut, err)
	}
	q := url.Values{"repository_id": {r.ID}, "limit": {"500"}}
	if *ref != "" {
		q.Set("ref_id", *ref)
	}
	var configs domain.Page[domain.Configuration]
	if err = c.Read(ctx, "/api/v1/configurations?"+q.Encode(), &configs); err != nil {
		return exitError(errOut, err)
	}
	var selected domain.Configuration
	for _, cfg := range configs.Items {
		if cfg.ID == *configuration || cfg.Name == *configuration {
			selected = cfg
			break
		}
	}
	if selected.ID == "" {
		return exitError(errOut, domain.Err("NOT_FOUND", "Configuration not found in selected repository/ref.", 404))
	}
	if *version == 0 {
		*version = h.Version
	}
	if *commandID == "" {
		*commandID = domain.NewID("cmd_")
	}
	p := map[string]string{"host_id": h.ID, "repository_id": r.ID, "configuration": selected.ID}
	if *ref != "" {
		p["ref_id"] = *ref
	}
	b, _ := json.Marshal(p)
	cmd := domain.Command{ID: *commandID, Type: "host.assign", TargetID: h.ID, ExpectedVersions: map[string]int64{"host:" + h.ID: *version, "configuration:" + selected.ID: selected.Version}, Payload: b}
	return submitCommand(ctx, c, cmd, *wait, *asJSON, out, errOut)
}
