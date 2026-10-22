package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"nixmox/internal/client"
	"nixmox/internal/domain"
	"nixmox/internal/presentation"
)

func hostTelemetryCommand(ctx context.Context, c *client.Client, args []string, out, errOut io.Writer) int {
	if len(args) == 0 || !domain.SafeID.MatchString(args[0]) {
		fmt.Fprintln(errOut, "A host ID is required.")
		return 2
	}
	host := args[0]
	f := flags("hosts telemetry", errOut)
	asJSON := f.Bool("json", false, "JSON output")
	watch := f.Bool("watch", false, "follow current telemetry")
	if f.Parse(args[1:]) != nil || f.NArg() != 0 {
		return 2
	}
	write := func(sample domain.HostTelemetry) error {
		if *asJSON {
			return json.NewEncoder(out).Encode(sample)
		}
		_, err := fmt.Fprint(out, presentation.TelemetryText(sample))
		return err
	}
	if !*watch {
		sample, err := c.Telemetry(ctx, host)
		if err != nil {
			return exitError(errOut, err)
		}
		return errorCode(errOut, write(sample))
	}
	for update := range c.TelemetryEvents(ctx, host) {
		if update.Error != nil {
			fmt.Fprintln(errOut, update.Error)
			continue
		}
		if err := write(update.Sample); err != nil {
			return exitError(errOut, err)
		}
	}
	return 0
}
