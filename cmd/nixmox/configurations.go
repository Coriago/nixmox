package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"nixmox/internal/client"
	"nixmox/internal/configuration"
	"nixmox/internal/domain"
	"os"
	"strings"
)

type fieldFlags map[string]any

func (f *fieldFlags) String() string { return "" }
func (f *fieldFlags) Set(value string) error {
	name, text, ok := strings.Cut(value, "=")
	if !ok || name == "" {
		return fmt.Errorf("--set requires Name=value")
	}
	var v any
	if json.Unmarshal([]byte(text), &v) != nil {
		v = text
	}
	if *f == nil {
		*f = fieldFlags{}
	}
	(*f)[name] = v
	return nil
}
func configurationCommand(ctx context.Context, c *client.Client, args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		return 2
	}
	verb := args[0]
	rest := args[1:]
	if verb == "draft" {
		if len(rest) == 0 {
			return 2
		}
		verb = "draft." + rest[0]
		rest = rest[1:]
	}
	id := ""
	if verb != "list" {
		if len(rest) == 0 {
			fmt.Fprintln(errOut, "A repository or draft ID is required.")
			return 2
		}
		id = rest[0]
		rest = rest[1:]
	}
	f := flags("configurations "+verb, errOut)
	asJSON := f.Bool("json", false, "machine-readable output")
	wait := f.Bool("wait", false, "wait for completion")
	commandID := f.String("command-id", "", "retry identity")
	repoName := f.String("repository", "", "repository name or ID")
	ref := f.String("ref", "", "accepted ref ID")
	commit := f.String("commit", "", "exact source commit")
	templateID := f.String("template", "", "configuration template ID")
	version := f.Int64("expected-version", 0, "reviewed draft version")
	showDiff := f.Bool("diff", false, "show draft file changes")
	filePath := f.String("path", "", "generated file path")
	fileInput := f.String("file", "", "UTF-8 replacement file")
	values := fieldFlags{}
	f.Var(&values, "set", "typed field Name=value; JSON for booleans, integers and lists")
	if f.Parse(rest) != nil || f.NArg() != 0 {
		return 2
	}
	q := url.Values{}
	if *ref != "" {
		q.Set("ref_id", *ref)
	}
	if *commit != "" {
		q.Set("commit", *commit)
	}
	if verb == "list" {
		if *repoName != "" {
			repo, err := resolveRepository(ctx, c, *repoName)
			if err != nil {
				return exitError(errOut, err)
			}
			q.Set("repository_id", repo.ID)
		}
		var page domain.Page[domain.Configuration]
		if err := c.Read(ctx, "/api/v1/configurations?"+q.Encode(), &page); err != nil {
			return exitError(errOut, err)
		}
		if *asJSON {
			printJSON(out, page)
		} else {
			for _, v := range page.Items {
				fmt.Fprintf(out, "%s  %s  %s  %s  %s\n", v.ID, v.RepositoryID, v.Name, v.Readiness, v.InspectedCommit)
			}
		}
		return 0
	}
	var repo domain.Repository
	if verb == "templates" || verb == "generate" {
		var err error
		repo, err = resolveRepository(ctx, c, id)
		if err != nil {
			return exitError(errOut, err)
		}
	}
	if verb == "templates" {
		var v any
		if err := c.Read(ctx, "/api/v1/repositories/"+repo.ID+"/templates?"+q.Encode(), &v); err != nil {
			return exitError(errOut, err)
		}
		printJSON(out, v)
		return 0
	}
	if !domain.SafeID.MatchString(id) {
		return 2
	}
	if verb == "draft.show" {
		suffix := ""
		if *showDiff {
			suffix = "/diff"
		}
		var v any
		if err := c.Read(ctx, "/api/v1/drafts/"+id+suffix, &v); err != nil {
			return exitError(errOut, err)
		}
		printJSON(out, v)
		return 0
	}
	if verb == "show" {
		var v domain.Configuration
		if err := c.Read(ctx, "/api/v1/configurations/"+id, &v); err != nil {
			return exitError(errOut, err)
		}
		printJSON(out, v)
		return 0
	}
	identity, err := c.Identity(ctx)
	if err != nil {
		return exitError(errOut, err)
	}
	cmd := domain.Command{ID: *commandID, TargetID: identity.NodeID, ExpectedVersions: map[string]int64{}}
	if cmd.ID == "" {
		cmd.ID = domain.NewID("cmd_")
	}
	p := map[string]any{"draft_id": id}
	switch verb {
	case "generate":
		cmd.Type = "configuration.generate"
		p = map[string]any{"repository_id": repo.ID, "values": values}
		if *templateID != "" {
			p["template_id"] = *templateID
		}
		if *ref != "" {
			p["ref_id"] = *ref
		}
		if *commit != "" {
			p["base_commit"] = *commit
		}
	case "draft.edit":
		cmd.Type = "configuration.draft.edit"
		cmd.ExpectedVersions["draft:"+id] = *version
		if (*filePath == "") != (*fileInput == "") || (*filePath != "" && len(values) != 0) {
			fmt.Fprintln(errOut, "Use --set or both --path and --file.")
			return 2
		}
		if *filePath != "" {
			file, err := os.Open(*fileInput)
			if err != nil {
				return exitError(errOut, err)
			}
			b, err := io.ReadAll(io.LimitReader(file, domain.MaxCommandBytes+1))
			file.Close()
			if err != nil {
				return exitError(errOut, err)
			}
			if len(b) > domain.MaxCommandBytes {
				return 2
			}
			p["file"] = map[string]string{"path": *filePath, "content": string(b)}
		} else {
			p["values"] = values
		}
	case "draft.validate":
		cmd.Type = "configuration.validate"
	case "draft.commit":
		cmd.Type = "configuration.commit"
		var draft configuration.Draft
		if err := c.Read(ctx, "/api/v1/drafts/"+id, &draft); err != nil {
			return exitError(errOut, err)
		}
		if *version != draft.Version {
			return exitError(errOut, domain.Err("VERSION_CONFLICT", "--expected-version must match the reviewed draft.", 409))
		}
		cmd.ExpectedVersions = draft.ExpectedVersions
		cmd.TargetID = draft.OwnerNodeID
	default:
		fmt.Fprintln(errOut, "Unknown configuration command.")
		return 2
	}
	cmd.Payload, _ = json.Marshal(p)
	return submitCommand(ctx, c, cmd, *wait, *asJSON, out, errOut)
}
