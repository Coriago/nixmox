package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nixmox/internal/domain"
)

type TelemetryUpdate struct {
	Sample domain.HostTelemetry
	Error  error
}

func (c *Client) Telemetry(ctx context.Context, host string) (domain.HostTelemetry, error) {
	var sample domain.HostTelemetry
	err := c.Read(ctx, "/api/v1/hosts/"+url.PathEscape(host)+"/telemetry", &sample)
	return sample, err
}
func (c *Client) TelemetryEvents(ctx context.Context, host string) <-chan TelemetryUpdate {
	out := make(chan TelemetryUpdate, 1)
	put := func(value TelemetryUpdate) {
		select {
		case out <- value:
		default:
			select {
			case <-out:
			default:
			}
			select {
			case out <- value:
			default:
			}
		}
	}
	go func() {
		defer close(out)
		transport := *c.HTTP
		transport.Timeout = 0
		for ctx.Err() == nil {
			streamCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			req, err := http.NewRequestWithContext(streamCtx, "GET", c.Endpoint+"/api/v1/events?topics=hosts.telemetry&host_id="+url.QueryEscape(host), nil)
			if err != nil {
				cancel()
				put(TelemetryUpdate{Error: err})
				return
			}
			resp, err := transport.Do(req)
			if err == nil {
				if resp.StatusCode == 200 {
					scanner := bufio.NewScanner(resp.Body)
					scanner.Buffer(make([]byte, 4096), domain.MaxCommandBytes+4096)
					kind := ""
					for scanner.Scan() {
						line := scanner.Text()
						if strings.HasPrefix(line, "event: ") {
							kind = strings.TrimPrefix(line, "event: ")
							continue
						}
						if !strings.HasPrefix(line, "data: ") {
							continue
						}
						data := []byte(strings.TrimPrefix(line, "data: "))
						var sample domain.HostTelemetry
						if kind == "snapshot" {
							var snapshot struct {
								Telemetry domain.HostTelemetry `json:"hosts.telemetry"`
							}
							if json.Unmarshal(data, &snapshot) != nil {
								continue
							}
							sample = snapshot.Telemetry
						} else if kind == "host.telemetry" {
							if json.Unmarshal(data, &sample) != nil {
								continue
							}
						} else {
							continue
						}
						if sample.HostID == host && sample.SchemaVersion == 1 {
							put(TelemetryUpdate{Sample: sample})
						} else {
							put(TelemetryUpdate{Error: errors.New("host telemetry unavailable")})
						}
					}
				}
				resp.Body.Close()
			}
			cancel()
			if ctx.Err() != nil {
				return
			}
			put(TelemetryUpdate{Error: errors.New("telemetry disconnected; displayed observations are stale")})
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}()
	return out
}
