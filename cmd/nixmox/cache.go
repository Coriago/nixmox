package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"nixmox/internal/client"
	"nixmox/internal/domain"
)

func cacheCommand(ctx context.Context, c *client.Client, args []string, out, errOut io.Writer) int {
	if len(args) == 0 {
		return 2
	}
	verb, rest := args[0], args[1:]
	path, ref := "", ""
	if verb == "locate" && len(rest) > 0 {
		path, rest = rest[0], rest[1:]
	}
	if verb == "credential" && len(rest) >= 2 && rest[0] == "set" {
		ref, rest = rest[1], rest[2:]
	}
	f := flags("cache "+verb, errOut)
	asJSON := f.Bool("json", false, "JSON output")
	wait := f.Bool("wait", true, "wait for completion")
	id := f.String("command-id", "", "stable retry identity")
	if f.Parse(rest) != nil || f.NArg() != 0 {
		return 2
	}
	if verb == "locate" && path != "" {
		var result []domain.CacheLocation
		if err := c.Read(ctx, "/api/v1/cache/locations?path="+url.QueryEscape(path), &result); err != nil {
			return exitError(errOut, err)
		}
		printJSON(out, result)
		return 0
	}
	var status domain.CacheStatus
	if err := c.Read(ctx, "/api/v1/cache/status", &status); err != nil {
		return exitError(errOut, err)
	}
	if verb == "status" {
		if *asJSON {
			printJSON(out, status)
		} else {
			fmt.Fprintf(out, "Gateway %s: %s\nServing: %t\n", status.GatewayState, status.LocalURL, status.Serving)
			if status.ServingReason != nil {
				fmt.Fprintln(out, *status.ServingReason)
			}
			for _, peer := range status.Peers {
				fmt.Fprintf(out, "%s: %s\n", peer.NodeID, peer.State)
			}
		}
		return 0
	}
	if verb != "credential" || !domain.SafeID.MatchString(ref) {
		return 2
	}
	if *id == "" {
		*id = domain.NewID("cmd_")
	}
	payload, _ := json.Marshal(map[string]string{"credential_id": ref})
	return submitCommand(ctx, c, domain.Command{ID: *id, Type: "cache.credential.set", TargetID: status.NodeID, ExpectedVersions: map[string]int64{"settings:cache:" + status.NodeID: status.SettingsVersion}, Payload: payload}, *wait, *asJSON, out, errOut)
}
