package httpapi

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"nixmox/internal/telemetry"
)

func TestTelemetryReadStreamAndHTML(t *testing.T) {
	s, server, c := fixture(t)
	sampler := telemetry.Start(s.Identity, 250*time.Millisecond, time.Second)
	t.Cleanup(sampler.Close)
	if err := s.SetTelemetry(sampler); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	id := s.Identity().NodeID
	events := c.TelemetryEvents(ctx, id)
	var previous uint64
	for {
		select {
		case <-ctx.Done():
			t.Fatal("live telemetry events unavailable")
		case update := <-events:
			if update.Error != nil {
				t.Fatal(update.Error)
			}
			if update.Sample.Sequence == 0 {
				continue
			}
			if previous == 0 {
				previous = update.Sample.Sequence
				continue
			}
			if update.Sample.Sequence <= previous {
				continue
			}
			if update.Sample.Memory == nil || update.Sample.HostID != id || update.Sample.Freshness != "live" {
				t.Fatalf("invalid streamed sample: %+v", update.Sample)
			}
			goto observed
		}
	}
observed:
	for _, path := range []string{"/api/v1/events?topics=hosts.telemetry", "/api/v1/events?host_id=" + id, "/api/v1/events?topics=hosts.telemetry&host_id=" + id + "&host_id=" + id} {
		response, err := server.Client().Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 400 {
			t.Fatalf("selector accepted: %s", path)
		}
	}
	response, err := server.Client().Get(server.URL + "/ui/hosts/" + id + "/telemetry")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || !strings.Contains(string(body), "CPU usage") || !strings.Contains(string(body), "Memory") || !strings.Contains(string(body), "data-host-id=\""+id+"\"") {
		t.Fatalf("missing HTML telemetry: %s", body)
	}
}
