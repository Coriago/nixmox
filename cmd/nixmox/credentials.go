package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"nixmox/internal/client"
	"nixmox/internal/domain"
	"os"
)

func credentialCommand(ctx context.Context, c *client.Client, args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		return 2
	}
	verb := args[0]
	f := flags("credentials "+verb, errOut)
	node := f.String("node", "", "credential holder")
	kind := f.String("kind", "", "credential kind")
	file := f.String("file", "", "private credential file")
	repository := f.String("repository", "", "read-access check repository")
	wait := f.Bool("wait", false, "wait for completion")
	asJSON := f.Bool("json", false, "JSON resource")
	version := f.Int64("expected-version", 0, "reviewed credential version")
	commandID := f.String("command-id", "", "stable import or command ID")
	limit := f.String("limit", "100", "page size")
	cursor := f.String("cursor", "", "page cursor")
	name := ""
	rest := args[1:]
	if verb != "list" {
		if len(rest) == 0 {
			return 2
		}
		name = rest[0]
		rest = rest[1:]
	}
	if f.Parse(rest) != nil || f.NArg() != 0 {
		return 2
	}
	if verb == "list" {
		var page domain.Page[domain.Credential]
		if err := c.Read(ctx, "/api/v1/credentials?limit="+*limit+"&cursor="+*cursor, &page); err != nil {
			return exitError(errOut, err)
		}
		printJSON(out, page)
		return 0
	}
	if verb == "import" {
		if *kind == "" || *file == "" {
			return 2
		}
		target := ""
		if *node != "" {
			h, err := resolveHost(ctx, c, *node)
			if err != nil {
				return exitError(errOut, err)
			}
			target = h.ID
		} else {
			id, err := c.Identity(ctx)
			if err != nil {
				return exitError(errOut, err)
			}
			target = id.NodeID
		}
		input, err := os.Open(*file)
		if err != nil {
			return exitError(errOut, domain.Err("CREDENTIAL_UNAVAILABLE", "Cannot read the selected local credential file.", 503))
		}
		defer input.Close()
		if *commandID == "" {
			*commandID = domain.NewID("cmd_")
		}
		fmt.Fprintln(errOut, "Import ID:", *commandID)
		receipt, err := c.ImportCredential(ctx, domain.SecretImport{ID: *commandID, Name: name, Kind: *kind, StorageNodeID: target}, input)
		if err != nil {
			return exitError(errOut, err)
		}
		if !*wait {
			printJSON(out, receipt)
			return 0
		}
		return watchTask(ctx, c, receipt.TaskID, *asJSON, out, errOut)
	}
	if !domain.SafeID.MatchString(name) {
		return 2
	}
	var credential domain.Credential
	if err := c.Read(ctx, "/api/v1/credentials/"+name, &credential); err != nil {
		return exitError(errOut, err)
	}
	if verb == "show" {
		printJSON(out, credential)
		return 0
	}
	if verb != "check" && verb != "remove" {
		return 2
	}
	target := ""
	if *node != "" {
		h, err := resolveHost(ctx, c, *node)
		if err != nil {
			return exitError(errOut, err)
		}
		target = h.ID
	} else {
		for _, replica := range credential.Replicas {
			if replica.State == "available" {
				target = replica.NodeID
				break
			}
		}
	}
	if target == "" {
		return exitError(errOut, domain.Err("CREDENTIAL_UNAVAILABLE", "No available credential holder.", 503))
	}
	p := map[string]string{"credential_id": credential.ID}
	versions := map[string]int64{}
	if verb == "check" && *repository != "" {
		r, err := resolveRepository(ctx, c, *repository)
		if err != nil {
			return exitError(errOut, err)
		}
		p["repository_id"] = r.ID
	}
	if verb == "remove" {
		if *version == 0 {
			*version = credential.Version
		}
		versions["credential:"+credential.ID] = *version
	}
	if *commandID == "" {
		*commandID = domain.NewID("cmd_")
	}
	body, _ := json.Marshal(p)
	return submitCommand(ctx, c, domain.Command{ID: *commandID, Type: "credential." + verb, TargetID: target, Payload: body, ExpectedVersions: versions}, *wait, *asJSON, out, errOut)
}
