package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/api"
	"nixmox/internal/client"
	"nixmox/internal/cluster"
	"nixmox/internal/configuration"
	"nixmox/internal/deployment"
	"nixmox/internal/domain"
	"nixmox/internal/source"
	"nixmox/internal/task"
	"nixmox/internal/testutil"
)

// TestRemoteInstallationDispatch documents the contact's HTTP assignment and
// durable submission to a different, pending owner; it never executes disk work.
func TestRemoteInstallationDispatch(t *testing.T) {
	// --- Test setup ---
	// ------------------
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	js := testutil.JetStream(t)
	for _, bucket := range []string{"MEMBERS", "CONFIGURATIONS", "TASKS", "LOCKS"} {
		if _, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket}); err != nil {
			t.Fatal(err)
		}
	}
	members, err := cluster.OpenStore[domain.Member](ctx, js, "MEMBERS")
	if err != nil {
		t.Fatal(err)
	}
	records, err := cluster.OpenStore[configuration.Version](ctx, js, "CONFIGURATIONS")
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := cluster.OpenStore[domain.Task](ctx, js, "TASKS")
	if err != nil {
		t.Fatal(err)
	}
	locks, err := cluster.OpenStore[map[string]string](ctx, js, "LOCKS")
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range []domain.Member{
		{NodeID: "contact", ClusterID: "cluster", Status: "full-stable", Environment: "installed"},
		{NodeID: "target", ClusterID: "cluster", Status: "pending", Environment: "installer"},
	} {
		if _, err = members.Create(ctx, member.NodeID, member); err != nil {
			t.Fatal(err)
		}
	}
	configurations := &configuration.Service{Repository: configuration.Repository{Records: records}}
	configured, err := configurations.Create(ctx, configuration.Create{Name: "target", TargetRepo: source.Pin{ID: "repo", Ref: "main", SHA: strings.Repeat("a", 40)}, FlakeDir: ".", FlakeConfiguration: "target"})
	if err != nil {
		t.Fatal(err)
	}
	manager := &task.Manager{HostID: "contact", Tasks: tasks, Resources: task.Resources{Store: locks}}
	service := &deployment.Service{HostID: "contact", Environment: "installed", Members: members, Configurations: configurations, Tasks: manager}
	server := httptest.NewServer((api.Server{Services: func() (*api.Services, error) { return &api.Services{Deployments: service, Tasks: manager}, nil }}).Handler())
	defer server.Close()
	remote, err := client.New(server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer remote.Close()

	// --- Usage ---
	// -------------
	input := deployment.Input{ConfigurationID: configured.ID, ConfigurationVersion: configured.Version, AutoApprove: true}
	var assigned domain.Member
	if err = remote.Do(ctx, http.MethodPut, "/hosts/target/configuration", input, &assigned); err != nil {
		t.Fatal(err)
	}
	var submitted domain.Task
	if err = remote.Do(ctx, http.MethodPost, "/hosts/target/deployments", input, &submitted); err != nil {
		t.Fatal(err)
	}
	retained, err := tasks.Get(ctx, submitted.ID)
	if err != nil {
		t.Fatal(err)
	}

	// --- Assertions ---
	// ------------------
	if assigned.ConfigurationID != configured.ID || assigned.ConfigurationVersion != configured.Version {
		t.Fatal("remote assignment was not retained")
	}
	if retained.Value.HostID != "target" || retained.Value.Type != deployment.InstallationTask || !retained.Value.Persist || retained.Value.Status != domain.Queued {
		t.Fatal("installation was not dispatched to its persistent target owner")
	}
	if len(retained.Value.Concurrency.ExclusiveResources) != 1 || retained.Value.Concurrency.ExclusiveResources[0] != "host:target:system" {
		t.Fatal("installation does not own the target system resource")
	}
}
