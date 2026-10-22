package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"

	"nixmox/internal/client"
	"nixmox/internal/domain"
	"nixmox/internal/presentation"
)

func clusterCommand(ctx context.Context, c *client.Client, args []string, out, errOut io.Writer) int {
	verb, rest := args[0], args[1:]
	if verb == "requests" {
		if len(rest) == 0 {
			fmt.Fprintln(errOut, "Usage: cluster requests {list|show ID|approve ID|reject ID}")
			return 2
		}
		verb = "request." + rest[0]
		rest = rest[1:]
	}
	var id string
	switch verb {
	case "create", "request.show", "request.approve", "request.reject":
		if len(rest) == 0 {
			fmt.Fprintln(errOut, "A name or request ID is required.")
			return 2
		}
		id = rest[0]
		rest = rest[1:]
	case "join", "status", "request.list":
	default:
		fmt.Fprintln(errOut, "Unknown cluster command.")
		return 2
	}
	f := flags("cluster "+verb, errOut)
	asJSON := f.Bool("json", false, "JSON output")
	mutation := verb == "create" || verb == "join" || verb == "request.approve" || verb == "request.reject"
	var wait bool
	var commandID, address, name, state, limit, cursor string
	var version int64
	if mutation {
		f.BoolVar(&wait, "wait", false, "wait for completion")
		f.StringVar(&commandID, "command-id", "", "stable retry ID")
	}
	if verb == "join" {
		f.StringVar(&address, "address", "", "machine endpoint host:port")
		f.StringVar(&name, "name", "", "expected cluster name")
	}
	if verb == "request.approve" || verb == "request.reject" {
		f.Int64Var(&version, "version", 0, "reviewed request version")
	}
	if verb == "request.list" {
		f.StringVar(&state, "state", "", "request state")
		f.StringVar(&limit, "limit", "", "page size")
		f.StringVar(&cursor, "cursor", "", "next-page cursor")
	}
	if err := f.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if f.NArg() != 0 {
		fmt.Fprintln(errOut, "Unexpected arguments.")
		return 2
	}
	if !mutation {
		var value any
		var err error
		switch verb {
		case "status":
			value, err = c.Cluster(ctx)
		case "request.show":
			value, err = c.JoinRequest(ctx, id)
		case "request.list":
			value, err = c.JoinRequests(ctx, url.Values{"state": {state}, "limit": {limit}, "cursor": {cursor}})
		}
		if err != nil {
			return exitError(errOut, err)
		}
		printJSON(out, value)
		return 0
	}
	identity, err := c.Identity(ctx)
	if err != nil {
		return exitError(errOut, err)
	}
	if commandID == "" {
		commandID = domain.NewID("cmd_")
	}
	cmd := domain.Command{ID: commandID, Type: "cluster." + verb, TargetID: identity.NodeID, ExpectedVersions: map[string]int64{}}
	switch verb {
	case "create":
		cmd.Payload, _ = json.Marshal(map[string]string{"name": id})
	case "join":
		cmd.Payload, _ = json.Marshal(map[string]string{"address": address, "expected_cluster_name": name})
	default:
		if version <= 0 || !domain.SafeID.MatchString(id) {
			fmt.Fprintln(errOut, "A request ID and --version from the reviewed request are required.")
			return 2
		}
		status, err := c.Cluster(ctx)
		if err != nil {
			return exitError(errOut, err)
		}
		if status.IssuerNodeID == nil {
			return exitError(errOut, domain.Err("UNAVAILABLE", "Cluster issuer unavailable.", 503))
		}
		cmd.TargetID = *status.IssuerNodeID
		cmd.ExpectedVersions["join_request:"+id] = version
		cmd.Payload, _ = json.Marshal(map[string]string{"request_id": id})
	}
	return submitCommand(ctx, c, cmd, wait, *asJSON, out, errOut)
}
func submitCommand(ctx context.Context, c *client.Client, cmd domain.Command, wait, asJSON bool, out, errOut io.Writer) int {
	fmt.Fprintln(errOut, "Command ID:", cmd.ID)
	receipt, err := c.Submit(ctx, cmd)
	if err != nil {
		return exitError(errOut, err)
	}
	if !wait {
		if asJSON {
			printJSON(out, receipt)
		} else {
			fmt.Fprintf(out, "Accepted %s · task %s\n", receipt.CommandID, receipt.TaskID)
		}
		return 0
	}
	task, err := c.Wait(ctx, receipt.TaskID)
	if err != nil {
		return exitError(errOut, err)
	}
	if asJSON {
		printJSON(out, task)
	} else {
		fmt.Fprintln(out, presentation.Task(task))
		if result := task.NetworkResult(); result != nil {
			fmt.Fprintln(out, presentation.Network(*result))
		}
	}
	if task.State == "blocked" {
		return 4
	}
	if task.State != "succeeded" {
		return 1
	}
	return 0
}
