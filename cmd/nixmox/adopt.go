package main

import (
	"context"
	"encoding/json"
	"io"
	"nixmox/internal/client"
	"nixmox/internal/domain"
)

func adoptCommand(ctx context.Context, c *client.Client, args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		return 2
	}
	name := args[0]
	f := flags("hosts adopt", errOut)
	candidate := f.String("candidate", "", "pinned built candidate")
	confirmation := f.String("confirm-host", "", "confirm exact host name or ID")
	wait := f.Bool("wait", false, "wait for verification")
	asJSON := f.Bool("json", false, "JSON result")
	version := f.Int64("expected-version", 0, "reviewed host version")
	commandID := f.String("command-id", "", "stable command ID")
	if f.Parse(args[1:]) != nil || f.NArg() != 0 || *candidate == "" || *confirmation == "" {
		return 2
	}
	h, err := resolveHost(ctx, c, name)
	if err != nil {
		return exitError(errOut, err)
	}
	if *version == 0 {
		*version = h.Version
	}
	if *commandID == "" {
		*commandID = domain.NewID("cmd_")
	}
	body, _ := json.Marshal(map[string]string{"host_id": h.ID, "candidate_id": *candidate, "confirm_host": *confirmation})
	return submitCommand(ctx, c, domain.Command{ID: *commandID, Type: "host.adopt", TargetID: h.ID, ExpectedVersions: map[string]int64{"host:" + h.ID: *version}, Payload: body}, *wait, *asJSON, out, errOut)
}
