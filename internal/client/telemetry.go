package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"nixmox/internal/domain"
)

// WatchTelemetry reconnects to a fresh snapshot; telemetry has no replay backlog.
func (c *Client) WatchTelemetry(ctx context.Context, id domain.ID, receive func(domain.HostTelemetry) error) error {
	transport := *c.http
	transport.Timeout = 0
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/api/v1/events?topics=hosts.telemetry&host_id="+url.QueryEscape(string(id)), nil)
		if err != nil {
			return err
		}
		request.Header.Set("Accept", "text/event-stream")
		response, err := transport.Do(request)
		if err == nil {
			err = consumeWatch(response, func(data json.RawMessage) error {
				var sample domain.HostTelemetry
				if err := json.Unmarshal(data, &sample); err != nil {
					return err
				}
				return receive(sample)
			})
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Callback/decoding errors and API refusals are not transport failures.
		var network net.Error
		if err == nil || (!errors.Is(err, io.EOF) && !errors.As(err, &network)) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}
