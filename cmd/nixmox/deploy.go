package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"nixmox/internal/client"
	"nixmox/internal/deployment"
	"nixmox/internal/domain"
	"nixmox/internal/presentation"
	"time"
)

func deployCommand(ctx context.Context, c *client.Client, args []string, out, errOut io.Writer) int {
	if len(args) < 2 {
		return 2
	}
	verb, name := args[0], args[1]
	rest := args[2:]
	if verb == "candidate" {
		if name != "show" || len(rest) < 1 || !domain.SafeID.MatchString(rest[0]) {
			return 2
		}
		var candidate domain.Candidate
		if err := c.Read(ctx, "/api/v1/candidates/"+rest[0], &candidate); err != nil {
			return exitError(errOut, err)
		}
		printJSON(out, candidate)
		return 0
	}
	f := flags("deploy "+verb, errOut)
	asJSON := f.Bool("json", false, "machine-readable result")
	wait := f.Bool("wait", false, "follow build progress")
	repoName := f.String("repository", "", "repository name or ID")
	cfgName := f.String("configuration", "", "configuration name or ID")
	ref := f.String("ref", "", "tracked source ref")
	commit := f.String("commit", "", "exact source commit")
	builder := f.String("builder", "", "builder host")
	version := f.Int64("expected-version", 0, "reviewed host version")
	commandID := f.String("command-id", "", "stable retry command ID")
	if f.Parse(rest) != nil || f.NArg() != 0 {
		return 2
	}
	if verb != "build" && verb != "preview" {
		return 2
	}
	h, err := resolveHost(ctx, c, name)
	if err != nil {
		return exitError(errOut, err)
	}
	selector := deployment.Selector{Configuration: *cfgName, RefID: *ref, Commit: *commit}
	if *repoName != "" {
		r, err := resolveRepository(ctx, c, *repoName)
		if err != nil {
			return exitError(errOut, err)
		}
		selector.RepositoryID = r.ID
	}
	p := map[string]any{"host_id": h.ID}
	if selector != (deployment.Selector{}) {
		p["source"] = selector
	}
	if *builder != "" {
		b, err := resolveHost(ctx, c, *builder)
		if err != nil {
			return exitError(errOut, err)
		}
		p["builder_id"] = b.ID
	}
	if *version == 0 {
		*version = h.Version
	}
	if *commandID == "" {
		*commandID = domain.NewID("cmd_")
	}
	body, _ := json.Marshal(p)
	cmd := domain.Command{ID: *commandID, Type: "deployment." + verb, TargetID: h.ID, ExpectedVersions: map[string]int64{"host:" + h.ID: *version}, Payload: body}
	if !*wait || *asJSON {
		return submitCommand(ctx, c, cmd, *wait, *asJSON, out, errOut)
	}
	fmt.Fprintln(errOut, "Command ID:", cmd.ID)
	receipt, err := c.Submit(ctx, cmd)
	if err != nil {
		return exitError(errOut, err)
	}
	return watchTask(ctx, c, receipt.TaskID, false, out, errOut)
}
func watchTask(ctx context.Context, c *client.Client, id string, asJSON bool, out, errOut io.Writer) int {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	last := ""
	for {
		task, err := c.Task(ctx, id)
		if err != nil {
			return exitError(errOut, err)
		}
		text := presentation.Task(task) + "\n" + presentation.TaskProgress(task)
		if text != last {
			if asJSON {
				printJSON(out, task)
			} else {
				fmt.Fprintln(out, text)
			}
			last = text
		}
		if task.Terminal() || task.State == "blocked" {
			if !asJSON {
				if result := presentation.TaskResult(task); result != "" {
					fmt.Fprintln(out, result)
				}
			}
			if task.State == "succeeded" {
				return 0
			}
			if task.State == "blocked" {
				return 4
			}
			return 1
		}
		select {
		case <-ctx.Done():
			return exitError(errOut, ctx.Err())
		case <-ticker.C:
		}
	}
}
