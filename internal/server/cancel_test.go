package server

import (
	"context"
	"io"
	"net/http/httptest"
	"nixmox/internal/events"
	"nixmox/internal/jobs"
	"nixmox/internal/messaging"
	"nixmox/internal/natsx"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCancelInstallerTaskOverNATS(t *testing.T) {
	coordinator, installer := operationServer(t), operationServer(t)
	installer.Config.Hostname = "installer"
	installer.Config.EnableCustomTasks = true
	t.Cleanup(installer.Runner.Shutdown)
	address := installerBus(t, coordinator, installer.Mux)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var j jobs.Job
	if err := coordinator.installerRequest(ctx, "installer", address, "POST", "/api/v1/hosts/installer/jobs", map[string]any{"kind": "command", "command": "sh", "args": []string{"-c", "echo ready; sleep 60"}}, &j); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/hosts/installer/jobs/" + j.ID
	for {
		var current jobs.Job
		if err := coordinator.installerRequest(ctx, "installer", address, "GET", path, nil, &current); err != nil {
			t.Fatal(err)
		}
		if current.State == "building" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Cancel through the same durable NATS request path used by the browser.
	if err := coordinator.installerRequest(ctx, "installer", address, "POST", path+"/cancel", nil, nil); err != nil {
		t.Fatal(err)
	}
	installer.Runner.Stop()
	got, _ := installer.Store.Get(j.ID)
	if got.State != "cancelled" {
		t.Fatal(got)
	}
	if w := call(installer, "POST", path+"/cancel", `{}`); w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call(installer, "GET", "/hosts/installer/jobs/"+j.ID, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "cancelled") || strings.Contains(w.Body.String(), "data-cancel-job=") {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call(installer, "POST", "/api/v1/jobs/missing/cancel", `{}`); w.Code != 404 {
		t.Fatal(w.Code)
	}

	// Cancelling the coordinator's wait must also cancel the remote child task.
	var child jobs.Job
	if err := coordinator.installerRequest(ctx, "installer", address, "POST", "/api/v1/hosts/installer/jobs", map[string]any{"kind": "command", "command": "sh", "args": []string{"-c", "sleep 60"}}, &child); err != nil {
		t.Fatal(err)
	}
	parentCtx, stop := context.WithCancelCause(ctx)
	stop(jobs.ErrCancelled)
	if err := coordinator.waitInstallerJob(parentCtx, "installer", address, child.ID, io.Discard); err == nil {
		t.Fatal("cancelled wait succeeded")
	}
	got, _ = installer.Store.Get(child.ID)
	if got.State != "cancelled" {
		t.Fatal("remote child still active", got)
	}
	done, err := installer.Runner.Submit(jobs.Job{Host: "installer", Kind: "echo"})
	if err != nil {
		t.Fatal(err)
	}
	installer.Runner.Stop()
	if w := call(installer, "POST", "/api/v1/jobs/"+done.ID+"/cancel", `{}`); w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestTaskCancellationBrowser(t *testing.T) {
	if os.Getenv("NIXMOX_BROWSER_TEST") != "1" {
		t.Skip("set NIXMOX_BROWSER_TEST=1")
	}
	s := operationServer(t)
	s.Config.EnableCustomTasks = true
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	opts, _ := natsx.NewOptions("local", 0, 0, "127.0.0.1", t.TempDir(), natsx.ClusterSpec{})
	hub, err := natsx.Start(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(hub.Stop)
	bus, err := messaging.New(ctx, "local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.SetBus(bus)
	bus.Rearm(ctx, hub)
	t.Cleanup(bus.Stop)
	s.Updates = &events.Signal{}
	s.Runner.Shutdown()
	s.Store = jobs.ObservableStore{Store: s.Store, Changed: s.Updates.Notify, LogAppended: bus.PublishLog}
	s.Runner = jobs.NewRunner(s.Store)
	t.Cleanup(s.Runner.Shutdown)
	httpServer := httptest.NewServer(s.transport(s.Mux))
	defer httpServer.Close()
	browser, err := exec.LookPath("chromium")
	if err != nil {
		t.Fatal(err)
	}
	artifacts := os.Getenv("NIXMOX_TEST_ARTIFACTS")
	if artifacts == "" {
		artifacts = t.TempDir()
	}
	script, _ := filepath.Abs("../../e2e/cancellation_browser.py")
	cmd := exec.Command("python3", script, httpServer.URL, artifacts)
	cmd.Env = append(os.Environ(), "NIXMOX_E2E_CHROMIUM="+browser)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, output)
	}
}
