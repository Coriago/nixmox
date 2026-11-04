package daemon_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/client"
	"nixmox/internal/cluster"
	"nixmox/internal/domain"
	"nixmox/internal/enrollment"
)

func TestRequestedEnrollment(t *testing.T) {
	// --- Test setup ---
	// ------------------
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	contact, _, contactHTTP := openEnrollmentNode(t, ctx, "contact")
	joining, config, joiningHTTP := openEnrollmentNode(t, ctx, "installed")
	coordinator, err := client.New(contactHTTP.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer coordinator.Close()
	applicant, err := client.New(joiningHTTP.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer applicant.Close()
	var created domain.Cluster
	if err = coordinator.Do(ctx, "POST", "/cluster", map[string]string{"name": "requested-enrollment"}, &created); err != nil {
		t.Fatal(err)
	}
	services, err := contact.Services()
	if err != nil {
		t.Fatal(err)
	}

	// --- Usage ---
	// -------------
	var requested enrollment.Request
	if err = applicant.Do(ctx, "POST", "/enrollment/request", enrollment.Input{Address: contactHTTP.URL}, &requested); err != nil {
		t.Fatal(err)
	}
	_, beforeErr := services.Members.Get(ctx, joining.Health(ctx).NodeID)
	data, err := os.ReadFile(filepath.Join(config.StateDir, "enrollment.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved enrollment.Request
	if err = json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	var waiting enrollment.Request
	if err = applicant.Do(ctx, "GET", "/enrollment/"+requested.ID, nil, &waiting); err != nil {
		t.Fatal(err)
	}
	var approved enrollment.Request
	if err = coordinator.Do(ctx, "POST", "/enrollment/"+requested.ID+"/approve", nil, &approved); err != nil {
		t.Fatal(err)
	}
	finished := errors.New("enrollment finished")
	var completed enrollment.Request
	err = services.Enrollment.Requests.Watch(ctx, requested.ID, func(record *cluster.Record[enrollment.Request], deleted bool) error {
		if record != nil && !deleted && (record.Value.Status == "completed" || record.Value.Status == "failed") {
			completed = record.Value
			return finished
		}
		return nil
	})
	if !errors.Is(err, finished) {
		t.Fatal(err)
	}
	member, err := services.Members.Get(ctx, joining.Health(ctx).NodeID)
	if err != nil {
		t.Fatal(err)
	}

	// --- Assertions ---
	// ------------------
	if requested.Decision != nil || waiting.Status != "pending" || !errors.Is(beforeErr, jetstream.ErrKeyNotFound) {
		t.Fatal("joining request granted membership before approval")
	}
	if saved.ID != requested.ID || saved.Cluster.CACertificate != created.CA.Certificate {
		t.Fatal("joining node did not persist request trust")
	}
	if completed.Status != "completed" {
		t.Fatalf("enrollment failed: %s", completed.Error)
	}
	if member.Value.EnrollmentRequestID != requested.ID || member.Value.Status != "pending" || member.Value.Environment != "installed" {
		t.Fatal("unexpected pending membership")
	}
	health := joining.Health(ctx)
	if health.Checks["natsClient"].Status != "healthy" || health.Checks["natsServer"].Status != "not-applicable" {
		t.Fatalf("unexpected joined runtime: %+v", health)
	}
}
