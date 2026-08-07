package portal

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nixmox/internal/cluster"
	"nixmox/internal/hostinfo"
)

func TestPortal(t *testing.T) {
	p, err := New()
	if err != nil {
		t.Fatalf("failed to create portal: %v", err)
	}
	p.Hostname = "test-host"

	rows := []HostRow{
		{
			Online: true,
			Host: hostinfo.HostInfo{
				Hostname:     "test-host",
				NixOSVersion: "23.11",
				Uptime:       time.Hour,
				AgentVersion: "dev",
			},
			Latest: &cluster.Record{
				Timestamp: time.Now(),
				Status:    "success",
				GitSHA:    "0123456789abcdef",
				GenBefore: 41,
				GenAfter:  42,
				JobID:     "job-1",
			},
		},
	}

	t.Run("index clustered", func(t *testing.T) {
		rr := httptest.NewRecorder()
		p.Index(rr, rows, cluster.ClusterState{Status: "clustered", ClusterName: "homelab", MemberCount: 1})

		body := rr.Body.String()
		for _, want := range []string{"test-host", "23.11", "online", "01234567", "Apply", "Rollback"} {
			if !strings.Contains(body, want) {
				t.Errorf("index missing %q", want)
			}
		}
	})

	t.Run("index unclustered renders setup", func(t *testing.T) {
		rr := httptest.NewRecorder()
		p.Index(rr, rows, cluster.ClusterState{Status: "unclustered"})

		body := rr.Body.String()
		for _, want := range []string{"Create new cluster", "Join existing cluster"} {
			if !strings.Contains(body, want) {
				t.Errorf("setup screen missing %q", want)
			}
		}
	})

	t.Run("host detail", func(t *testing.T) {
		rr := httptest.NewRecorder()
		p.HostDetail(rr, HostDetailView{
			NodeID:  "test-host",
			Host:    rows[0].Host,
			Latest:  rows[0].Latest,
			History: []cluster.Record{*rows[0].Latest},
		})

		body := rr.Body.String()
		for _, want := range []string{"test-host", "success", "01234567", "42", "job-1"} {
			if !strings.Contains(body, want) {
				t.Errorf("host detail missing %q", want)
			}
		}
	})
}
