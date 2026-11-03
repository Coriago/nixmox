package installer_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"nixmox/internal/cluster"
	"nixmox/internal/domain"
	"nixmox/internal/installer"
	"nixmox/internal/installer/installers"
	"nixmox/internal/source"
	"nixmox/internal/task"
	"nixmox/internal/testutil"
)

// This tests the runner's return checkpoint, not the deferred daemon/task resume
// mechanism. Queueing a checkpoint here does not simulate an actual reboot.
func TestLaunchReturnCheckpoint(t *testing.T) {
	// --- Test setup ---
	// ------------------
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	js := testutil.JetStream(t)
	if _, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "TASKS"}); err != nil {
		t.Fatal(err)
	}
	tasks, err := cluster.OpenStore[domain.Task](ctx, js, "TASKS")
	if err != nil {
		t.Fatal(err)
	}
	system := "/nix/store/" + strings.Repeat("a", 32) + "-nixos-system-installer"
	service := &installer.Service{HostID: "node1", Inspect: func(context.Context) (installers.Target, error) {
		return installers.Target{NodeID: "node1", System: "x86_64-linux", Environment: "installer", BootID: "new-boot", SystemPath: system}, nil
	}}
	if _, err = js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "LOCKS"}); err != nil {
		t.Fatal(err)
	}
	locks, err := cluster.OpenStore[map[string]string](ctx, js, "LOCKS")
	if err != nil {
		t.Fatal(err)
	}
	manager := &task.Manager{HostID: "node1", Tasks: tasks, Resources: task.Resources{Store: locks}, Runners: map[string]task.Runner{installer.LaunchTask: service.Runner}}
	definition, err := (installers.General{}).Definition("x86_64-linux", source.Pin{ID: "nixmox", Ref: "main", SHA: strings.Repeat("1", 40)}, ".")
	if err != nil {
		t.Fatal(err)
	}
	definition.ID = "general"
	input, _ := json.Marshal(installer.LaunchPlan{Installer: definition, PrepareID: "prepare-kexec"})

	// --- Usage ---
	// -------------
	for id, previousBoot := range map[string]string{"returned": "old-boot", "unchanged": "new-boot"} {
		checkpoint, _ := json.Marshal(map[string]string{"previousBootId": previousBoot, "systemPath": system})
		_, err = tasks.Create(ctx, id, domain.Task{ID: id, HostID: "node1", Type: installer.LaunchTask, Status: domain.Queued, Inputs: input,
			Context: map[string]json.RawMessage{"installerTransition": checkpoint}})
		if err != nil {
			t.Fatal(err)
		}
	}
	life, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- manager.Run(life) }()
	defer func() {
		stop()
		if err := <-done; err != nil && !errors.Is(err, context.Canceled) {
			t.Error(err)
		}
	}()
	observed := map[string]domain.Task{}
	reached := errors.New("both completed")
	err = tasks.Watch(ctx, "*", func(record *cluster.Record[domain.Task], deleted bool) error {
		if record != nil && !deleted && record.Value.Status.Terminal() {
			observed[record.Key] = record.Value
		}
		if len(observed) == 2 {
			return reached
		}
		return nil
	})

	// --- Assertions ---
	// ------------------
	if !errors.Is(err, reached) {
		t.Fatal(err)
	}
	if observed["returned"].Status != domain.Succeeded || observed["unchanged"].Status != domain.Failed {
		t.Fatalf("incorrect return validation: %s / %s", observed["returned"].Status, observed["unchanged"].Status)
	}
	var result installer.LaunchResult
	if err = json.Unmarshal(observed["returned"].Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.InstallerID != "general" || result.Environment != "installer" || result.BootID != "new-boot" {
		t.Fatal("incorrect launch result")
	}
}
