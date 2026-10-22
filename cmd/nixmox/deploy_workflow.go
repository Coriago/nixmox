package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"github.com/charmbracelet/x/term"
	"io"
	"net/url"
	"nixmox/internal/client"
	"nixmox/internal/deployment"
	"nixmox/internal/domain"
	"nixmox/internal/presentation"
	"os"
	"strings"
	"time"
)

func deployWorkflowCommand(ctx context.Context, c *client.Client, args []string, out, errOut io.Writer) int {
	if len(args) < 2 {
		return 2
	}
	verb, name := args[0], args[1]
	rest := args[2:]
	if verb == "diff" && name == "show" {
		if len(rest) < 1 || !domain.SafeID.MatchString(rest[0]) {
			return 2
		}
		id := rest[0]
		f := flags("diff show", errOut)
		asJSON := f.Bool("json", false, "JSON result")
		kind := f.String("kind", "", "change category")
		query := f.String("q", "", "literal search")
		cursor := f.String("cursor", "", "next page")
		limit := f.String("limit", "100", "page size")
		if f.Parse(rest[1:]) != nil || f.NArg() != 0 {
			return 2
		}
		var d domain.PackageDiff
		if err := c.Read(ctx, "/api/v1/diffs/"+id, &d); err != nil {
			return exitError(errOut, err)
		}
		var page domain.Page[domain.PackageChange]
		q := url.Values{"kind": {*kind}, "q": {*query}, "limit": {*limit}, "cursor": {*cursor}}
		if err := c.Read(ctx, "/api/v1/diffs/"+id+"/changes?"+q.Encode(), &page); err != nil {
			return exitError(errOut, err)
		}
		d.Changes = page.Items
		d.NextCursor = page.NextCursor
		if *asJSON {
			printJSON(out, d)
		} else {
			fmt.Fprint(out, presentation.PackageDiff(d))
		}
		return 0
	}
	f := flags("deploy "+verb, errOut)
	asJSON := f.Bool("json", false, "JSON resource")
	wait := f.Bool("wait", false, "follow task")
	detach := f.Bool("detach", false, "return on acceptance")
	yes := f.Bool("yes", false, "approve this deployment")
	mode := f.String("mode", "switch", "switch or boot")
	candidate := f.String("candidate", "", "built candidate")
	generation := f.String("generation", "", "retained generation")
	baseline := f.String("baseline-generation", "", "comparison baseline")
	diffID := f.String("diff", "", "reviewed comparison")
	diffVersion := f.Int64("diff-version", 0, "reviewed comparison version")
	version := f.Int64("expected-version", 0, "reviewed host version")
	commandID := f.String("command-id", "", "stable retry command ID")
	repository := f.String("repository", "", "source repository")
	configuration := f.String("configuration", "", "source configuration")
	ref := f.String("ref", "", "source ref")
	commit := f.String("commit", "", "pinned source commit")
	builder := f.String("builder", "", "builder host")
	if f.Parse(rest) != nil || f.NArg() != 0 || *detach && *wait {
		return 2
	}
	if verb == "show" {
		var d domain.Deployment
		if err := c.Read(ctx, "/api/v1/deployments/"+url.PathEscape(name), &d); err != nil {
			return exitError(errOut, err)
		}
		printJSON(out, d)
		return 0
	}
	if verb == "approve" {
		task, err := c.Task(ctx, name)
		if err != nil {
			return exitError(errOut, err)
		}
		if *diffID == "" || *diffVersion < 1 {
			return 2
		}
		if *commandID == "" {
			*commandID = domain.NewID("cmd_")
		}
		body, _ := json.Marshal(map[string]any{"task_id": name, "diff_id": *diffID, "diff_version": *diffVersion})
		receipt, err := c.Submit(ctx, domain.Command{ID: *commandID, Type: "deployment.approve", TargetID: task.TargetID, Payload: body})
		if err != nil {
			return exitError(errOut, err)
		}
		fmt.Fprintln(errOut, "Command:", *commandID, "Task:", receipt.TaskID, "Deployment:", name)
		if !*wait {
			printJSON(out, receipt)
			return 0
		}
		approval, err := c.Wait(ctx, receipt.TaskID)
		if err != nil {
			return exitError(errOut, err)
		}
		if approval.State != "succeeded" {
			return exitError(errOut, approval.Error)
		}
		return watchDeployment(ctx, c, name, *asJSON, out, errOut)
	}
	h, err := resolveHost(ctx, c, name)
	if err != nil {
		return exitError(errOut, err)
	}
	if verb == "generations" {
		var page domain.Page[domain.Generation]
		if err = c.Read(ctx, "/api/v1/hosts/"+h.ID+"/generations?limit=500", &page); err != nil {
			return exitError(errOut, err)
		}
		printJSON(out, page)
		return 0
	}
	p := map[string]any{"host_id": h.ID}
	versions := map[string]int64{}
	switch verb {
	case "diff":
		p["candidate_id"] = *candidate
		if *baseline != "" {
			p["baseline_generation_id"] = *baseline
		}
	case "apply", "rollback":
		p["mode"] = *mode
		p["auto_approve"] = *yes
		if *candidate != "" {
			p["candidate_id"] = *candidate
		}
		if *generation != "" {
			p["generation_id"] = *generation
		}
		selector := deployment.Selector{Configuration: *configuration, RefID: *ref, Commit: *commit}
		if *repository != "" {
			r, err := resolveRepository(ctx, c, *repository)
			if err != nil {
				return exitError(errOut, err)
			}
			selector.RepositoryID = r.ID
		}
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
		versions["host:"+h.ID] = *version
	default:
		return 2
	}
	if *commandID == "" {
		*commandID = domain.NewID("cmd_")
	}
	body, _ := json.Marshal(p)
	fmt.Fprintln(errOut, "Target:", h.Name, "Mode:", *mode, "Command:", *commandID)
	receipt, err := c.Submit(ctx, domain.Command{ID: *commandID, Type: "deployment." + verb, TargetID: h.ID, Payload: body, ExpectedVersions: versions})
	if err != nil {
		return exitError(errOut, err)
	}
	fmt.Fprintln(errOut, "Task:", receipt.TaskID)
	if *detach || verb != "apply" && !*wait {
		printJSON(out, receipt)
		return 0
	}
	return watchDeployment(ctx, c, receipt.TaskID, *asJSON, out, errOut)
}

func watchDeployment(ctx context.Context, c *client.Client, id string, asJSON bool, out, errOut io.Writer) int {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	last := ""
	review := ""
	for {
		t, err := c.Task(ctx, id)
		if err != nil {
			return exitError(errOut, err)
		}
		text := presentation.Task(t) + "\n" + presentation.TaskProgress(t)
		if text != last && !asJSON {
			fmt.Fprintln(out, text)
			if t.Source != nil {
				b, _ := json.Marshal(t.Source)
				fmt.Fprintln(out, "Pinned source:", string(b))
			}
			last = text
		}
		if t.Approval != nil && t.Approval.DiffID != review && !asJSON {
			var d domain.PackageDiff
			if err = c.Read(ctx, "/api/v1/diffs/"+t.Approval.DiffID, &d); err != nil {
				return exitError(errOut, err)
			}
			fmt.Fprint(out, presentation.PackageDiff(d))
			review = d.ID
		}
		if t.Terminal() {
			if asJSON {
				printJSON(out, t)
			} else {
				fmt.Fprintln(out, presentation.TaskResult(t))
			}
			if t.State == "succeeded" {
				return 0
			}
			return 1
		}
		if t.RequiresAttention() {
			if asJSON {
				printJSON(out, t)
				return 4
			}
			if t.Approval == nil || t.Approval.State != "pending" || !term.IsTerminal(os.Stdin.Fd()) {
				if t.Approval != nil {
					fmt.Fprintf(out, "Approve: nixmox deploy approve %s --diff %s --diff-version %d --wait\n", t.ID, t.Approval.DiffID, t.Approval.DiffVersion)
				}
				return 4
			}
			fmt.Fprint(errOut, "Deploy this candidate in "+t.Approval.Mode+" mode? [y/N] ")
			line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			accept := strings.EqualFold(strings.TrimSpace(line), "y") || strings.EqualFold(strings.TrimSpace(line), "yes")
			payload := map[string]any{"task_id": t.ID}
			kind := "task.cancel"
			expected := map[string]int64{"task:" + t.ID: t.Version}
			if accept {
				kind = "deployment.approve"
				expected = nil
				payload["diff_id"] = t.Approval.DiffID
				payload["diff_version"] = t.Approval.DiffVersion
			}
			body, _ := json.Marshal(payload)
			commandID := domain.NewID("cmd_")
			fmt.Fprintln(errOut, "Command:", commandID)
			r, err := c.Submit(ctx, domain.Command{ID: commandID, Type: kind, TargetID: t.TargetID, ExpectedVersions: expected, Payload: body})
			if err != nil {
				return exitError(errOut, err)
			}
			if !accept {
				return 1
			}
			a, err := c.Wait(ctx, r.TaskID)
			if err != nil {
				return exitError(errOut, err)
			}
			if a.State != "succeeded" {
				return exitError(errOut, a.Error)
			}
		}
		select {
		case <-ctx.Done():
			return exitError(errOut, ctx.Err())
		case <-ticker.C:
		}
	}
}
