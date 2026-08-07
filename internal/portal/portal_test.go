package portal

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nixmox/internal/hostinfo"
)

func TestPortal(t *testing.T) {
	p, err := New()
	if err != nil {
		t.Fatalf("failed to create portal: %v", err)
	}

	hosts := []hostinfo.HostInfo{
		{
			Hostname:     "test-host",
			NixOSVersion: "23.11",
			Uptime:       time.Hour,
			AgentVersion: "dev",
		},
	}

	t.Run("index", func(t *testing.T) {
		rr := httptest.NewRecorder()
		p.Index(rr, hosts)

		body := rr.Body.String()
		if !strings.Contains(body, "test-host") {
			t.Errorf("expected hostname in output")
		}
		if !strings.Contains(body, "23.11") {
			t.Errorf("expected nixos version in output")
		}
		if !strings.Contains(body, "online") {
			t.Errorf("expected status badge in output")
		}
	})
}
