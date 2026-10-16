package portal

import (
	"nixmox/internal/cluster"
	"nixmox/internal/jobs"
	"testing"
)

func TestRevisionStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		row  HostRow
		want string
	}{
		{"offline", HostRow{}, "Unknown · offline"},
		{"unpulled", HostRow{Online: true}, "Not pulled"},
		{"remote update", HostRow{Online: true, PulledSHA: "a", RemoteSHA: "b"}, "Repository update"},
		{"never", HostRow{Online: true, PulledSHA: "b"}, "Never applied"},
		{"pending", HostRow{Online: true, PulledSHA: "b", Latest: &cluster.Record{GitSHA: "a", Status: "success"}}, "Needs apply"},
		{"failed", HostRow{Online: true, PulledSHA: "b", Latest: &cluster.Record{GitSHA: "b", Status: "failed"}}, "Apply failed"},
		{"boot", HostRow{Online: true, PulledSHA: "b", Latest: &cluster.Record{GitSHA: "b", Status: "success", Mode: "boot"}}, "Staged for boot"},
		{"dry", HostRow{Online: true, PulledSHA: "b", Latest: &cluster.Record{GitSHA: "b", Status: "success", Mode: "dry-activate"}}, "Activation not applied"},
		{"rollback", HostRow{Online: true, PulledSHA: "b", Latest: &cluster.Record{GitSHA: "b", Status: "success", Kind: "rollback"}}, "Rolled back · review"},
		{"legacy", HostRow{Online: true, PulledSHA: "b", Latest: &cluster.Record{GitSHA: "b", Status: "success"}}, "Revision recorded"},
		{"applied", HostRow{Online: true, PulledSHA: "b", Latest: &cluster.Record{GitSHA: "b", Status: "success", Mode: "switch"}}, "Revision applied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.row.SyncLabel(); got != tc.want {
				t.Fatalf("%q != %q", got, tc.want)
			}
		})
	}
}

func TestDeploymentOverridesOldFailureAndApplyActions(t *testing.T) {
	r := HostRow{Online: true, PulledSHA: "new", RemoteSHA: "new", Latest: &cluster.Record{GitSHA: "new", Status: "success", Mode: "switch"}}
	if r.ApplyLabel() != "Reapply" || r.ApplyDisabled() || r.SyncClass() != "online" {
		t.Fatal(r)
	}
	r.RemoteSHA = "newer"
	if r.ApplyLabel() != "Pull update first" || !r.ApplyDisabled() {
		t.Fatal(r)
	}
	r.RemoteSHA = "new"
	r.Latest.Status = "failed"
	if r.ApplyLabel() != "Retry apply" || r.SyncClass() != "failed" {
		t.Fatal(r)
	}
	r.Active = &jobs.Job{State: "building"}
	if r.SyncLabel() != "Deploying" || !r.ApplyDisabled() {
		t.Fatal(r)
	}
	r.Unavailable = true
	if r.SyncLabel() != "Status unavailable" {
		t.Fatal(r)
	}
}
