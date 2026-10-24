package client

import (
	"context"
	"encoding/json"
	"net/url"

	"nixmox/internal/domain"
)

// WatchTelemetry reconnects to a fresh snapshot; telemetry has no replay backlog.
func (c *Client) WatchTelemetry(ctx context.Context, id domain.ID, receive func(domain.HostTelemetry) error) error {
	return c.watch(ctx, func() url.Values {
		return url.Values{"topics": {"hosts.telemetry"}, "host_id": {string(id)}}
	}, func(data json.RawMessage) error {
		var sample domain.HostTelemetry
		if err := json.Unmarshal(data, &sample); err != nil {
			return err
		}
		return receive(sample)
	})
}
