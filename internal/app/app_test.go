package app_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nixmox/internal/app"
	"nixmox/internal/cli"
	"nixmox/internal/client"
	"nixmox/internal/domain"
	"nixmox/internal/engine/platform"
	"nixmox/internal/engine/task"
	"nixmox/internal/runtime"
	"nixmox/internal/service"
)

func configuration(t *testing.T) runtime.Config {
	t.Helper()
	config := runtime.Defaults()
	config.DataDir = t.TempDir()
	var reserved []net.Listener
	for _, port := range []*int{&config.HTTP.Port, &config.NATS.ClientPort, &config.NATS.RoutePort, &config.NATS.LeafPort} {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		reserved = append(reserved, listener)
		*port = listener.Addr().(*net.TCPAddr).Port
	}
	for _, listener := range reserved {
		listener.Close()
	}
	return config
}

func start(t *testing.T, config runtime.Config) (*client.Client, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Serve(ctx, config) }()
	api, err := client.New(runtime.Endpoint("", "", &config), &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			t.Error("daemon shutdown timed out")
		}
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			stopped = true
			cancel()
			t.Fatalf("startup failed: %v", err)
		default:
		}
		if _, err = api.Identity(ctx); err == nil {
			return api, stop
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("daemon did not become ready")
	return nil, stop
}

func TestUnjoinedCLIAPIAndRestart(t *testing.T) {
	config := configuration(t)
	api, stop := start(t, config)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	identity, err := api.Identity(ctx)
	if err != nil || identity.NodeID == "" || identity.ClusterID != nil {
		t.Fatalf("identity: %+v %v", identity, err)
	}
	var output, diagnostics bytes.Buffer
	args := []string{"--endpoint", runtime.Endpoint("", "", &config), "--json", "network", "check", "--wait", "--command-id", "cmd-test"}
	if code := cli.Execute(ctx, args, &output, &diagnostics); code != 0 {
		t.Fatalf("CLI exit %d: %s", code, diagnostics.String())
	}
	var executed domain.Task
	if err = json.Unmarshal(output.Bytes(), &executed); err != nil || executed.State != domain.Succeeded || executed.Scope != domain.NodeLocal {
		t.Fatalf("task: %+v %v; %s", executed, err, output.String())
	}
	if len(executed.Checkpoints) != 1 || executed.Checkpoints[0].State != "observed" {
		t.Fatal("task observation not persisted")
	}
	chunk, err := api.Logs(ctx, executed.ID, 0, domain.LogReadDefault)
	if err != nil || !chunk.Complete || chunk.NextOffset == 0 {
		t.Fatalf("logs: %+v %v", chunk, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		sample, err := api.Telemetry(ctx, identity.NodeID)
		if err == nil && sample.Sequence > 0 && sample.MembershipEpoch == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("real telemetry missing: %+v %v", sample, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, path := range []string{"identity.json", "pki/daemon/key.pem", "pki/broker/key.pem"} {
		info, err := os.Stat(filepath.Join(config.DataDir, path))
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatalf("private state permissions: %s %v", path, err)
		}
	}
	// Unimplemented lifecycle work cannot accidentally execute as a local operation.
	_, err = api.Submit(ctx, domain.Command{ID: "cmd-cluster", Type: "cluster.leave", TargetID: identity.NodeID, Payload: json.RawMessage(`{}`)})
	if err == nil {
		t.Fatal("unfinished cluster leave was accepted")
	}
	stop()
	// Model a crash after durable admission but before a worker starts. Use the
	// real service/journal; the subsequent daemon must discover this queued task.
	journal, err := task.OpenJournal(config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	node := service.New()
	node.Identity, node.Journal = identity, journal
	node.Network = func(ctx context.Context) (domain.NetworkStatus, error) {
		return platform.NetworkStatus(ctx, identity.NodeID, nil)
	}
	if err = node.StartNode(ctx); err != nil {
		t.Fatal(err)
	}
	queued, err := node.Submit(ctx, domain.Command{ID: "cmd-before-crash", Type: "network.check", TargetID: identity.NodeID, Payload: json.RawMessage("{}")})
	if err != nil {
		t.Fatal(err)
	}
	if err = journal.Shutdown(); err != nil {
		t.Fatal(err)
	}
	api, _ = start(t, config)
	recovered, err := api.Wait(ctx, *queued.TaskID)
	if err != nil || recovered.State != domain.Succeeded {
		t.Fatalf("queued task not recovered: %+v %v", recovered, err)
	}
	restored, err := api.Identity(ctx)
	if err != nil || restored.NodeID != identity.NodeID || restored.PublicKey != identity.PublicKey {
		t.Fatalf("identity changed on restart: %+v %v", restored, err)
	}
	output.Reset()
	diagnostics.Reset()
	if code := cli.Execute(ctx, args, &output, &diagnostics); code != 0 {
		t.Fatalf("duplicate CLI exit %d: %s", code, diagnostics.String())
	}
	var duplicate domain.Task
	if err = json.Unmarshal(output.Bytes(), &duplicate); err != nil || duplicate.ID != executed.ID || duplicate.Version != executed.Version {
		t.Fatalf("duplicate task re-executed: %+v %v", duplicate, err)
	}
	page, err := api.Tasks(ctx, domain.PageRequest{})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("unexpected persisted tasks: %+v %v", page, err)
	}
}

func TestHTTPBoundariesAndTelemetryStream(t *testing.T) {
	config := configuration(t)
	api, _ := start(t, config)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	identity, err := api.Identity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	endpoint := runtime.Endpoint("", "", &config)
	for _, body := range []string{
		`{"id":"bad","type":"network.check","target_id":"` + string(identity.NodeID) + `","payload":{},"scope":"node_local"}`,
		`{"id":"bad","type":"network.check","target_id":"` + string(identity.NodeID) + `","payload":{}} {}`,
		strings.Repeat(" ", domain.CommandPayloadLimit+32<<10) + `{}`,
	} {
		response, err := http.Post(endpoint+"/api/v1/commands", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid command status: %d", response.StatusCode)
		}
	}
	request, _ := http.NewRequestWithContext(ctx, "GET", endpoint+"/api/v1/events?topics=hosts.telemetry&host_id="+string(identity.NodeID), nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	first, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(first, "event:host.telemetry") {
		t.Fatalf("SSE snapshot missing: %q %v", first, err)
	}
	// No plain or anonymous NATS access, including on loopback.
	connection, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", config.NATS.ClientPort), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	connection.SetDeadline(time.Now().Add(100 * time.Millisecond))
	io.WriteString(connection, "CONNECT {}\r\nPING\r\n")
	data := make([]byte, 128)
	n, _ := connection.Read(data)
	if bytes.Contains(data[:n], []byte("PONG")) || bytes.Contains(data[:n], []byte("INFO")) {
		t.Fatal("NATS permitted a plaintext session")
	}
}
