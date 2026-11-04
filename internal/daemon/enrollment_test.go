package daemon_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"golang.org/x/crypto/ssh"

	"nixmox/internal/client"
	"nixmox/internal/cluster"
	"nixmox/internal/daemon"
	"nixmox/internal/discovery"
	"nixmox/internal/domain"
	"nixmox/internal/enrollment"
	"nixmox/internal/host"
	"nixmox/internal/identity"
)

func openEnrollmentNode(t *testing.T, ctx context.Context, name string) (*daemon.Runtime, daemon.Config, *httptest.Server) {
	t.Helper()
	root := t.TempDir()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "test")
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(root, "host.key")
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewUnstartedServer(nil)
	config := daemon.Config{StateDir: filepath.Join(root, "state"), Name: name, SSHHostKey: keyPath, AdvertiseURL: "http://" + httpServer.Listener.Addr().String(), NATSHost: "127.0.0.1", NATSPort: -1, RoutePort: -1, BootstrapPort: -1, BootstrapRoutePort: -1}
	if name == "installer" {
		config.Environment = "installer"
	}
	runtime, err := daemon.Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { httpServer.Close(); runtime.Close() })
	httpServer.Config.Handler = runtime.Handler()
	httpServer.Start()
	return runtime, config, httpServer
}

// TestInstallerEnrollment exercises public contact-owned enrollment, restricted
// pending membership and a cold installer restart over real mutual-TLS NATS.
func TestInstallerEnrollment(t *testing.T) {
	// --- Test setup ---
	// ------------------
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	contact, _, contactHTTP := openEnrollmentNode(t, ctx, "contact")
	pending, pendingConfig, pendingHTTP := openEnrollmentNode(t, ctx, "installer")
	api, err := client.New(contactHTTP.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()

	var created domain.Cluster
	if err = api.Do(ctx, "POST", "/cluster", map[string]string{"name": "enrollment-example"}, &created); err != nil {
		t.Fatal(err)
	}
	// The discovery transport is covered separately. Seed its observed hint with
	// an unreachable/wrong first network endpoint to exercise identity-safe fallback.
	keyData, err := os.ReadFile(filepath.Join(pendingConfig.StateDir, "node.key"))
	if err != nil {
		t.Fatal(err)
	}
	keyBlock, _ := pem.Decode(keyData)
	parsedKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := identity.Thumbprint(parsedKey.(ed25519.PrivateKey).Public())
	if err != nil {
		t.Fatal(err)
	}
	contactServices, err := contact.Services()
	if err != nil {
		t.Fatal(err)
	}
	candidate := discovery.Record{ID: "installer-observation", ObservedByHostID: contact.Health(ctx).NodeID, LastSeenAt: time.Now(), Advertisement: discovery.Advertisement{NodeID: pending.Health(ctx).NodeID, Name: "installer", Endpoints: []string{contactHTTP.URL, pendingHTTP.URL}, Identity: discovery.Identity{Kind: "node", PublicKeyThumbprint: fingerprint}}}
	if _, err = contactServices.Discovery.Create(ctx, candidate.ID, candidate); err != nil {
		t.Fatal(err)
	}
	// --- Usage ---
	// -------------
	var joined enrollment.Request
	if err = api.Do(ctx, "POST", "/enrollment/request", enrollment.Input{DiscoveryID: candidate.ID, ContactHostID: contact.Health(ctx).NodeID}, &joined); err != nil {
		t.Fatal(err)
	}
	before := pending.Health(ctx)
	// A missing configuration fails before any host mutation. The owning pending
	// daemon still must journal, execute and finish this remotely submitted task.
	work, err := contactServices.Tasks.Submit(ctx, domain.Task{HostID: before.NodeID, Type: "configuration-build", Persist: true, Inputs: json.RawMessage(`{"configurationId":"missing","configurationVersion":1}`), Concurrency: domain.TaskConcurrency{ExclusiveResources: []string{"host:" + before.NodeID + ":system"}}})
	if err != nil {
		t.Fatal(err)
	}
	finished := errors.New("task finished")
	var handled domain.Task
	waitCtx, waitCancel := context.WithTimeout(ctx, 10*time.Second)
	err = contactServices.Tasks.Tasks.Watch(waitCtx, work.ID, func(record *cluster.Record[domain.Task], deleted bool) error {
		if record != nil && !deleted && record.Value.Status.Terminal() {
			handled = record.Value
			return finished
		}
		return nil
	})
	waitCancel()
	if !errors.Is(err, finished) || handled.Status != domain.Failed {
		t.Fatalf("pending task execution: %v, %+v", err, handled)
	}
	t.Run("remote disk inventory", func(t *testing.T) {
		if _, err := os.Stat("/sys/dev/block"); errors.Is(err, os.ErrNotExist) {
			t.Skip("block-device sysfs is unavailable in the build sandbox; VM acceptance requires real inventory")
		} else if err != nil {
			t.Fatal(err)
		}
		var disks []host.Disk
		if err := api.Do(ctx, "GET", "/hosts/"+before.NodeID+"/disks", nil, &disks); err != nil {
			t.Fatal(err)
		}
		if disks == nil {
			t.Fatal("remote inventory did not return a disk list")
		}
	})
	svc, err := pending.Services()
	if err != nil {
		t.Fatal(err)
	}
	member, err := svc.Members.Get(ctx, before.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	mutation, stop := context.WithTimeout(ctx, 250*time.Millisecond)
	attempted := member.Value
	attempted.Status = "full-stable"
	_, mutationErr := svc.Members.Update(mutation, before.NodeID, attempted, member.Revision)
	stop()
	if err = pending.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := daemon.Open(ctx, pendingConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	after := restored.Health(ctx)
	var authoritative domain.Member
	if err = api.Do(ctx, "GET", "/hosts/"+before.NodeID, nil, &authoritative); err != nil {
		t.Fatal(err)
	}

	// --- Assertions ---
	// ------------------
	if joined.Status != "completed" || authoritative.Status != "pending" || authoritative.Environment != "installer" {
		t.Fatalf("unexpected enrollment: %+v, %+v", joined, authoritative)
	}
	if before.Checks["natsClient"].Status != "healthy" || before.Checks["natsServer"].Status != "not-applicable" {
		t.Fatalf("pending runtime: %+v", before)
	}
	if mutationErr == nil {
		t.Fatal("pending node changed membership")
	}
	if after.NodeID != before.NodeID || after.Checks["natsClient"].Status != "healthy" || after.Checks["natsServer"].Status != "not-applicable" {
		t.Fatalf("pending restart: %+v", after)
	}
	if _, err = os.Stat(filepath.Join(pendingConfig.StateDir, "nats")); !os.IsNotExist(err) {
		t.Fatal("pending node created NATS server storage")
	}
}

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
