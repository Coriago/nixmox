package daemon_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	apiclient "nixmox/internal/client"
	"nixmox/internal/configuration"
	"nixmox/internal/daemon"
	"nixmox/internal/domain"
	"nixmox/internal/recipe"
)

// TestClusterLifecycle validates the public create/health/read flow and a cold
// restart of both persistent JetStream peers, without touching host services.
func TestClusterLifecycle(t *testing.T) {
	// --- Test setup ---
	// ------------------
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	root := t.TempDir()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "test")
	if err != nil {
		t.Fatal(err)
	}
	sshKey := filepath.Join(root, "host.key")
	if err = os.WriteFile(sshKey, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	config := daemon.Config{StateDir: filepath.Join(root, "state"), Name: "cedar", NATSHost: "127.0.0.1", NATSPort: -1, RoutePort: -1, BootstrapPort: -1, BootstrapRoutePort: -1, SSHHostKey: sshKey}
	runtime, err := daemon.Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if runtime != nil {
			runtime.Close()
		}
	}()
	server := httptest.NewServer(runtime.Handler())
	defer server.Close()
	client := &http.Client{Timeout: 40 * time.Second}

	// --- Usage ---
	// -------------
	unjoined := runtime.Health(ctx)
	response, err := client.Post(server.URL+"/cluster", "application/json", bytes.NewBufferString(`{"name":"homelab"}`))
	if err != nil {
		t.Fatal(err)
	}
	var created domain.Cluster
	if err = json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		_, retryErr := runtime.CreateCluster(ctx, "homelab")
		t.Fatalf("cluster creation status %d; retry: %v", response.StatusCode, retryErr)
	}
	online := runtime.Health(ctx)
	api, err := apiclient.New(server.URL, client)
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	var definition recipe.Version
	var draft, observed configuration.Version
	input := json.RawMessage(`{"sourceDefinition":{"flakeDir":".","flakeConfiguration":"cedar","observations":{"hosts":{"type":"clusterHosts"}}}}`)
	if err = api.Do(ctx, "POST", "/recipes", input, &definition); err != nil {
		t.Fatal(err)
	}
	configurationInput := map[string]any{"recipe": recipe.Reference{ID: definition.ID, Version: definition.Version}, "targetRepo": map[string]string{"id": "target", "ref": "main", "sha": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	if err = api.Do(ctx, "POST", "/configurations", configurationInput, &draft); err != nil {
		t.Fatal(err)
	}
	if err = api.Do(ctx, "POST", "/configurations/"+draft.ID+"/versions/1/observe", map[string]string{"name": "hosts"}, &observed); err != nil {
		t.Fatal(err)
	}
	if err = api.Do(ctx, "GET", "/configurations", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err = runtime.Close(); err != nil {
		t.Fatal(err)
	}
	runtime = nil
	server.Close()
	runtime, err = daemon.Open(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := runtime.Services()
	if err != nil {
		t.Fatal(err)
	}
	record, err := restored.Cluster.Get(ctx, "identity")
	if err != nil {
		t.Fatal(err)
	}

	restarted := httptest.NewServer(runtime.Handler())
	defer restarted.Close()
	restoredAPI, err := apiclient.New(restarted.URL, client)
	if err != nil {
		t.Fatal(err)
	}
	defer restoredAPI.Close()
	var persisted configuration.Version
	if err = restoredAPI.Do(ctx, "GET", "/configurations/"+draft.ID+"/versions/2", nil, &persisted); err != nil {
		t.Fatal(err)
	}
	if err = restoredAPI.Do(ctx, "GET", "/configurations", nil, nil); err != nil {
		t.Fatal(err)
	}

	// --- Assertions ---
	// ------------------
	if !observed.CanRender || persisted.ID != draft.ID || persisted.Version != 2 || len(persisted.Collected.Observations) != 1 {
		t.Fatal("configuration observation/version did not survive restart")
	}
	if restored.Registry == nil || restored.Configurations.Observer == nil {
		t.Fatal("source services were not restored")
	}
	if unjoined.Checks["natsServer"].Status != "not-applicable" {
		t.Fatal("unjoined node started a NATS server")
	}
	if online.Checks["natsServer"].Status != "healthy" || online.Checks["natsClient"].Status != "healthy" {
		t.Fatal("cluster not healthy")
	}
	if record.Value.ID != created.ID || runtime.Health(ctx).NodeID != unjoined.NodeID {
		t.Fatal("cluster/node identity changed on restart")
	}
}
