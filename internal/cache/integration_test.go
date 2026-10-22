package cache

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"nixmox/internal/broker"
	"nixmox/internal/config"
	"nixmox/internal/credentials"
	"nixmox/internal/domain"
	"nixmox/internal/pki"
	"nixmox/internal/state"
	"nixmox/internal/storage"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func cacheState(t *testing.T) state.Store {
	t.Helper()
	ns, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true})
	if err != nil {
		t.Fatal(err)
	}
	ns.Start()
	if !ns.ReadyForConnections(3 * time.Second) {
		t.Fatal("NATS unavailable")
	}
	t.Cleanup(ns.Shutdown)
	nc, err := nats.Connect(ns.ClientURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := nc.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	kv, err := js.CreateKeyValue(&nats.KeyValueConfig{Bucket: "nx_state", Storage: nats.FileStorage, MaxValueSize: domain.MaxCommandBytes})
	if err != nil {
		t.Fatal(err)
	}
	return state.Store{Acquire: func() (*broker.Stores, error) { return &broker.Stores{State: kv, JS: js}, nil }}
}

type cacheHosts []domain.Host

func (h cacheHosts) Hosts(context.Context, string, string) (domain.Page[domain.Host], error) {
	return domain.Page[domain.Host]{Items: h}, nil
}
func (h cacheHosts) Host(_ context.Context, id string) (domain.Host, error) {
	for _, v := range h {
		if v.ID == id {
			return v, nil
		}
	}
	return domain.Host{}, domain.Err("NOT_FOUND", "Host missing.", 404)
}
func cacheKeys(t *testing.T) pki.Keys {
	t.Helper()
	st, err := storage.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	keys, err := pki.Open(st)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func TestSignedPeerStreamImportsIntoIsolatedNixStore(t *testing.T) {
	if _, err := os.Stat("/nix/var/nix/daemon-socket/socket"); err != nil {
		t.Skip("requires real Nix daemon")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	root, _ := filepath.Abs("../..")
	quoted, _ := json.Marshal(root)
	expression := `let flake = builtins.getFlake ` + string(quoted) + `; pkgs = flake.inputs.nixpkgs.legacyPackages.${builtins.currentSystem}; in pkgs.runCommand "nixmox-signed-cache-fixture" {} ''mkdir -p "$out"; printf 'signed streaming fixture\n' > "$out/value"''`
	output, err := exec.CommandContext(ctx, "nix", "build", "--impure", "--no-link", "--print-out-paths", "--option", "substituters", "https://cache.nixos.org", "--expr", expression).Output()
	if err != nil {
		t.Fatal("fixture build", err)
	}
	path := strings.TrimSpace(string(output))
	storeHash := filepath.Base(path)[:32]
	st := cacheState(t)
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "signer")
	value := []byte("fixture:" + base64.StdEncoding.EncodeToString(private) + "\n")
	if err = os.WriteFile(keyPath, value, 0600); err != nil {
		t.Fatal(err)
	}
	ref := "signer"
	full := func(context.Context, string) bool { return true }
	vault, err := credentials.New(filepath.Join(t.TempDir(), "vault"), st, map[string]config.Credential{ref: {Kind: "nix-cache-signing-key", SecretFile: keyPath}}, func() domain.Identity { return domain.Identity{NodeID: "n_producer"} }, full)
	if err != nil {
		t.Fatal(err)
	}
	if err = vault.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	producer := New(config.Cache{Enable: true, Serve: true, SigningCredentialRef: &ref, MaxConcurrentTransfers: 2}, 63170)
	producer.Identity, producer.Full, producer.Vault, producer.State = vault.Identity, full, vault, st
	if err = producer.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	producerKeys, receiverKeys := cacheKeys(t), cacheKeys(t)
	peer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { producer.ServePeer(w, r, "n_receiver", true) }))
	peer.TLS = pki.MutualServer(producerKeys.Daemon, pki.Pool(receiverKeys.Daemon.Leaf), pki.OnlyKey(receiverKeys.Daemon.Leaf))
	peer.StartTLS()
	defer peer.Close()
	_, port, _ := net.SplitHostPort(peer.Listener.Addr().String())
	receiver := New(config.Cache{Enable: true, Serve: true, MaxConcurrentTransfers: 2}, 63170)
	receiver.Identity = func() domain.Identity { return domain.Identity{NodeID: "n_receiver"} }
	receiverVault, err := credentials.New(filepath.Join(t.TempDir(), "vault"), st, nil, receiver.Identity, full)
	if err != nil {
		t.Fatal(err)
	}
	receiver.Full, receiver.Vault, receiver.State = full, receiverVault, st
	receiver.Hosts = cacheHosts{{ID: "n_producer", Membership: "full", Endpoints: []domain.Endpoint{{Service: "machine", Transport: "https", Address: net.JoinHostPort("192.0.2.1", port)}}}}
	receiver.Client = func(context.Context, string) (*http.Client, error) {
		transport := &http.Transport{TLSClientConfig: pki.MutualClient(receiverKeys.Daemon, pki.Pool(producerKeys.Daemon.Leaf), producerKeys.Daemon.Leaf.DNSNames[0], pki.OnlyKey(producerKeys.Daemon.Leaf)), DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, peer.Listener.Addr().String())
		}, ResponseHeaderTimeout: time.Second, DisableCompression: true}
		return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
	}
	gateway := httptest.NewServer(http.HandlerFunc(receiver.ServeLocal))
	direct, _ := receiver.Client(ctx, "n_producer")
	probe, probeErr := direct.Get("https://192.0.2.1:" + port + "/store-cache/nix-cache-info")
	if probeErr != nil {
		t.Fatal("mutual TLS cache probe", probeErr)
	}
	probe.Body.Close()
	direct.CloseIdleConnections()
	defer gateway.Close()
	request := httptest.NewRequest("GET", "http://localhost/cache/nix-cache-info", nil)
	request.RemoteAddr = "192.0.2.5:1234"
	request.Header.Set("X-Forwarded-For", "127.0.0.1")
	record := httptest.NewRecorder()
	receiver.ServeLocal(record, request)
	if record.Code != 403 {
		t.Fatal("spoofed loopback accepted")
	}
	response, err := http.Get(gateway.URL + "/cache/" + storeHash + ".narinfo")
	if err != nil {
		t.Fatal(err)
	}
	metadata, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode, string(metadata))
	}
	keys, err := receiver.trustedKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	info, err := validateMetadata(metadata, storeHash, keys)
	if err != nil {
		t.Fatal(err)
	}
	before := producer.bytesExported.Load()
	request, _ = http.NewRequest("HEAD", gateway.URL+"/cache/"+info.URL, nil)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 || producer.bytesExported.Load() != before {
		t.Fatal("HEAD exported bytes")
	}
	copyTo := func(trust string) ([]byte, error) {
		isolated := t.TempDir()
		t.Cleanup(func() {
			_ = filepath.WalkDir(isolated, func(path string, entry os.DirEntry, err error) error {
				if err == nil && entry.IsDir() {
					return os.Chmod(path, 0700)
				}
				return err
			})
		})
		destination := "local?root=" + isolated
		return exec.CommandContext(ctx, "nix", "copy", "--from", gateway.URL+"/cache/", "--to", destination, "--option", "require-sigs", "true", "--option", "trusted-public-keys", trust, path).CombinedOutput()
	}
	if out, err := copyTo(""); err == nil {
		t.Fatal("unsigned trust imported input-addressed output", string(out))
	}
	if out, err := copyTo(keys[0].String()); err != nil {
		t.Fatal("verified Nix import failed", err, string(out))
	}
	if receiver.bytesReceived.Load() == 0 || producer.bytesExported.Load() == 0 {
		t.Fatal("no archive bytes streamed")
	}
	status, _ := receiver.Status(ctx)
	if status.Serving || status.GatewayState != "ready" {
		t.Fatal("receiver needed signer", status)
	}
	if err = os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	status, _ = producer.Status(ctx)
	if status.Serving || status.ServingReason == nil || *status.ServingReason != "SIGNING_CREDENTIAL_UNAVAILABLE" {
		t.Fatal("key loss retained serving", status)
	}
	response, err = http.Get(gateway.URL + "/cache/" + storeHash + ".narinfo")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 503 {
		t.Fatal("offline signer was reported as miss", response.StatusCode)
	}
	request = httptest.NewRequest("GET", "https://localhost/store-cache/"+storeHash+".narinfo", nil)
	record = httptest.NewRecorder()
	producer.ServePeer(record, request, "n_pending", false)
	if record.Code != 403 {
		t.Fatal("pending closure access was not restricted")
	}
	// The listener itself requires a pinned client certificate.
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pki.Pool(producerKeys.Daemon.Leaf), ServerName: producerKeys.Daemon.Leaf.DNSNames[0]}}}
	if reply, err := client.Get(peer.URL + "/store-cache/nix-cache-info"); err == nil {
		reply.Body.Close()
		t.Fatal("unauthenticated peer admitted")
	}
}
