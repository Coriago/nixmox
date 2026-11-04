package membership_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/cluster"
	"nixmox/internal/credentials"
	"nixmox/internal/domain"
	"nixmox/internal/identity"
	"nixmox/internal/installer"
	"nixmox/internal/installer/installers"
	"nixmox/internal/membership"
	"nixmox/internal/task"
	"nixmox/internal/testutil"
)

// TestPromotion exercises coordinator tasks, real SOPS re-encryption, and durable
// membership. The VM acceptance supplies the actual boot/server callbacks.
func TestPromotion(t *testing.T) {
	// --- Test setup ---
	// ------------------
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	js := testutil.JetStream(t)
	for _, bucket := range []string{"MEMBERS", "CREDENTIALS", "RECIPIENTS", "TASKS", "LOCKS"} {
		if _, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: bucket}); err != nil {
			t.Fatal(err)
		}
	}
	members, err := cluster.OpenStore[domain.Member](ctx, js, "MEMBERS")
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := cluster.OpenStore[credentials.Record](ctx, js, "CREDENTIALS")
	if err != nil {
		t.Fatal(err)
	}
	recipients, err := cluster.OpenStore[credentials.Recipients](ctx, js, "RECIPIENTS")
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
	keys := map[string]string{}
	public := map[string]string{}
	for _, id := range []string{"coordinator", "target"} {
		key, err := age.GenerateX25519Identity()
		if err != nil {
			t.Fatal(err)
		}
		keys[id] = filepath.Join(t.TempDir(), "age.key")
		public[id] = key.Recipient().String()
		if err = identity.WritePrivate(keys[id], []byte(key.String())); err != nil {
			t.Fatal(err)
		}
	}
	for _, member := range []domain.Member{
		{NodeID: "coordinator", ClusterID: "cluster", Status: "full-stable", AgePublicKey: public["coordinator"], Endpoints: []domain.Endpoint{{NATSRoute: "nats-route://127.0.0.1:6222"}}},
		{NodeID: "target", ClusterID: "cluster", Status: "pending", Environment: "installer", AgePublicKey: public["target"], ConfigurationID: "configuration", ConfigurationVersion: 1},
	} {
		if _, err = members.Create(ctx, member.NodeID, member); err != nil {
			t.Fatal(err)
		}
	}
	initial := credentials.Recipients{ClusterID: "cluster", Recipients: []credentials.Recipient{{NodeID: "coordinator", AgePublicKey: public["coordinator"]}}}
	if _, err = recipients.Create(ctx, "recipients", initial); err != nil {
		t.Fatal(err)
	}
	cipher := credentials.Cipher{KeyFile: keys["coordinator"]}
	ciphertext, err := cipher.Encrypt(ctx, []byte("signing-key-fixture"), initial.Recipients)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = secrets.Create(ctx, "signing", credentials.Record{ID: "signing", Content: ciphertext}); err != nil {
		t.Fatal(err)
	}
	system := "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-system"
	expected, _ := json.Marshal(installer.Preservation{NodeID: "target", ClusterID: "cluster", PreviousBootID: "installer", ExpectedSystem: system, ExpectedGeneration: 1})
	parent := domain.Task{ID: "target." + uuid.NewString(), HostID: "target", Type: "fresh-deployment", Status: domain.Running, Persist: true, Inputs: json.RawMessage(`{"configurationId":"configuration","configurationVersion":1}`), Context: map[string]json.RawMessage{"installation": expected, "validatedInstallation": json.RawMessage(`true`)}}
	if _, err = tasks.Create(ctx, parent.ID, parent); err != nil {
		t.Fatal(err)
	}
	manager := &task.Manager{HostID: "coordinator", Tasks: tasks, Resources: task.Resources{Store: locks}, Journal: &task.Journal{HostID: "coordinator", Directory: filepath.Join(t.TempDir(), "tasks")}}
	var mu sync.Mutex
	observed := membership.Info{Target: installers.Target{NodeID: "target", Environment: "installed", BootID: "installed", SystemPath: system}, Generation: 1, Endpoint: domain.Endpoint{HTTP: "http://target:8080", NATSClient: "tls://target:4222", NATSRoute: "nats-route://target:6222"}}
	replicas := 0
	service := &membership.Service{HostID: "coordinator", ClusterID: "cluster", Members: members, Credentials: secrets, Recipients: recipients, Tasks: manager, Cipher: cipher,
		Inspect: func(context.Context, string) (membership.Info, error) {
			mu.Lock()
			defer mu.Unlock()
			return observed, nil
		},
		Apply: func(_ context.Context, _ string, change membership.Change) error {
			mu.Lock()
			defer mu.Unlock()
			switch change.Role {
			case "full":
				observed.CacheReady = true
			case "prepare-server":
				observed.ServerCount = 1
			case "full-stable":
				observed.LocalClient = true
			}
			return nil
		},
		Certificate: func(context.Context, membership.Info) (string, error) { return "certificate", nil },
		Authorize:   func(context.Context) error { return nil },
		Replicate:   func(_ context.Context, count int) error { mu.Lock(); defer mu.Unlock(); replicas = count; return nil },
	}
	manager.Runners = map[string]task.Runner{membership.PromotionTask: service.Runner}
	lifetime, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- manager.Run(lifetime) }()
	defer func() { stop(); <-done }()
	wait := func(id string) domain.Task {
		t.Helper()
		completed := errors.New("completed")
		var result domain.Task
		err := tasks.Watch(ctx, id, func(record *cluster.Record[domain.Task], deleted bool) error {
			if record != nil && !deleted && record.Value.Status.Terminal() {
				result = record.Value
				return completed
			}
			return nil
		})
		if !errors.Is(err, completed) || result.Status != domain.Succeeded {
			t.Fatalf("promotion failed: %+v (%v)", result, err)
		}
		return result
	}

	// --- Usage ---
	// -------------
	submitted, err := service.Submit(ctx, membership.Input{NodeID: "target", ToStatus: "full", DeploymentTaskID: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	full := wait(submitted.ID)
	duplicate, err := service.Submit(ctx, membership.Input{NodeID: "target", ToStatus: "full", DeploymentTaskID: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := secrets.Get(ctx, "signing")
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := (credentials.Cipher{KeyFile: keys["target"]}).Decrypt(ctx, rotated.Value.Content)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(plaintext)
	submitted, err = service.Submit(ctx, membership.Input{NodeID: "target", ToStatus: "full-stable"})
	if err != nil {
		t.Fatal(err)
	}
	stable := wait(submitted.ID)
	member, err := members.Get(ctx, "target")
	if err != nil {
		t.Fatal(err)
	}

	// --- Assertions ---
	// ------------------
	if full.ID != duplicate.ID || full.Links.ParentTaskID != parent.ID {
		t.Fatal("automatic promotion was duplicated or detached")
	}
	if string(plaintext) != "signing-key-fixture" || rotated.Value.Content == ciphertext {
		t.Fatal("credential was not re-encrypted for the new member")
	}
	if member.Value.Status != "full-stable" || member.Value.Environment != "installed" {
		t.Fatal("promotion did not retain installed identity")
	}
	var previous string
	if err = json.Unmarshal(stable.Context["previousStatus"], &previous); err != nil || previous != "full" {
		t.Fatal("transition context was not retained")
	}
	mu.Lock()
	defer mu.Unlock()
	if replicas != 3 || !observed.LocalClient {
		t.Fatal("server promotion did not finish topology/connection changes")
	}
}
