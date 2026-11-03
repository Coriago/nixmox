package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"nixmox/internal/api"
	"nixmox/internal/cli"
	"nixmox/internal/cluster"
	"nixmox/internal/installer"
	"nixmox/internal/installer/installers"
	"nixmox/internal/source"
	"nixmox/internal/testutil"
)

func TestInstallerCatalogCLI(t *testing.T) {
	// --- Test setup ---
	// ------------------
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	js := testutil.JetStream(t)
	if _, err := js.CreateKeyValue(ctx, jetstream.KeyValueConfig{Bucket: "INSTALLERS"}); err != nil {
		t.Fatal(err)
	}
	definitions, err := cluster.OpenStore[installers.Definition](ctx, js, "INSTALLERS")
	if err != nil {
		t.Fatal(err)
	}
	service := &installer.Service{
		HostID: "node1", Definitions: definitions,
		Repository: func(context.Context, string) (source.Repository, error) { return source.Repository{ID: "nixmox"}, nil },
		Inspect: func(context.Context) (installers.Target, error) {
			return installers.Target{NodeID: "node1", System: "x86_64-linux", Environment: "installed", Virtualization: "kvm", KexecAvailable: true}, nil
		},
	}
	server := httptest.NewServer((api.Server{Services: func() (*api.Services, error) {
		return &api.Services{Installers: service}, nil
	}}).Handler())
	defer server.Close()
	run := func(input string, args ...string) ([]byte, error) {
		var output bytes.Buffer
		command := cli.NewCommand(strings.NewReader(input), &output, io.Discard)
		command.SetArgs(append([]string{"--endpoint", server.URL, "--json"}, args...))
		err := command.ExecuteContext(ctx)
		return output.Bytes(), err
	}

	// --- Usage ---
	// -------------
	pin := source.Pin{ID: "nixmox", Ref: "main", SHA: strings.Repeat("1", 40)}
	registration, _ := json.Marshal(installer.Registration{Kind: "vm", System: "x86_64-linux", TargetRepo: pin})
	file := filepath.Join(t.TempDir(), "installer.json")
	if err = os.WriteFile(file, registration, 0600); err != nil {
		t.Fatal(err)
	}
	data, err := run("", "installer", "register", "--file", file)
	if err != nil {
		t.Fatal(err)
	}
	var registered installers.Definition
	if err = json.Unmarshal(data, &registered); err != nil {
		t.Fatal(err)
	}
	data, err = run("", "installer", "checks", "--host", "node1")
	if err != nil {
		t.Fatal(err)
	}
	var checks installer.Checks
	if err = json.Unmarshal(data, &checks); err != nil {
		t.Fatal(err)
	}
	_, launchErr := run("", "installer", "launch", "--host", "node1")
	_, remoteErr := run("", "installer", "checks", "--host", "node2")
	record, err := definitions.Get(ctx, registered.ID)
	if err != nil {
		t.Fatal(err)
	}

	// --- Assertions ---
	// ------------------
	if checks.Selected == nil || checks.Selected.Installer.ID != registered.ID || checks.Selected.Prepare.ID != "prepare-kexec" {
		t.Fatal("automatic selection did not retain the registered installer/prepare pair")
	}
	if record.Value.Build.TargetRepo != pin {
		t.Fatal("registered build pin was not retained in NATS")
	}
	if checks.ExecutionReady || checks.ExecutionBlocker == "" || launchErr == nil {
		t.Fatal("live launch was not gated on deferred runtime/task support")
	}
	if remoteErr == nil {
		t.Fatal("remote host check silently used local observations")
	}
}
