package credentials

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"net/http"
	"net/http/httptest"
	"nixmox/internal/broker"
	"nixmox/internal/config"
	"nixmox/internal/domain"
	"nixmox/internal/gitstore"
	"nixmox/internal/service"
	"nixmox/internal/state"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func credentialVault(t *testing.T) *Vault {
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
	js, _ := nc.JetStream()
	kv, err := js.CreateKeyValue(&nats.KeyValueConfig{Bucket: "nx_state", Storage: nats.FileStorage})
	if err != nil {
		t.Fatal(err)
	}
	vault, err := New(filepath.Join(t.TempDir(), "vault"), state.Store{Acquire: func() (*broker.Stores, error) { return &broker.Stores{State: kv}, nil }}, map[string]config.Credential{}, func() domain.Identity { return domain.Identity{NodeID: "n_holder"} }, func(context.Context, string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	return vault
}
func TestSecretIngressIdentityPrivateStorageAndPublicBindings(t *testing.T) {
	ctx := context.Background()
	v := credentialVault(t)
	secret := "fixture-token-do-not-publish"
	request := domain.SecretImport{ID: "cmd_import", Name: "token", Kind: "git-http-token", StorageNodeID: "n_holder"}
	command, err := v.Stage(ctx, request, strings.NewReader(secret))
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(command)
	if bytes.Contains(encoded, []byte(secret)) {
		t.Fatal("secret in durable command")
	}
	if _, err = v.Stage(ctx, request, strings.NewReader("different-value")); err == nil {
		t.Fatal("changed secret reused command ID")
	}
	result, err := v.importCredential(ctx, &service.Execution{Command: command})
	if err != nil {
		t.Fatal(err)
	}
	credential := result.(domain.Credential)
	if _, err = v.importCredential(ctx, &service.Execution{Command: command}); err != nil {
		t.Fatal("import reconciliation", err)
	}
	if _, err = v.Stage(ctx, request, strings.NewReader(secret)); err != nil {
		t.Fatal("exact ingress retry", err)
	}
	if err = v.Bind(ctx, credential.ID, "repository", "r_test"); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]string{"credential_id": credential.ID})
	current, _ := v.Credential(ctx, credential.ID)
	if _, err = v.removeCredential(ctx, &service.Execution{Command: domain.Command{ID: "cmd_remove", TargetID: "n_holder", Payload: data, ExpectedVersions: map[string]int64{"credential:" + credential.ID: current.Version}}}); err == nil {
		t.Fatal("removed bound credential")
	}
	if err = v.PruneBinding(ctx, nil, "repository", "r_test"); err != nil {
		t.Fatal(err)
	}
	current, _ = v.Credential(ctx, credential.ID)
	if len(current.Scope.RepositoryIDs) != 0 {
		t.Fatal("stale dependency pin")
	}
	entries, err := v.State.List(ctx, "credential.")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if bytes.Contains(entry.Data, []byte(secret)) {
			t.Fatal("secret in NATS")
		}
	}
	info, err := os.Stat(filepath.Join(v.Root, "managed", credential.ID+".secret"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("secret permissions", err)
	}
	staged, err := filepath.Glob(filepath.Join(v.Root, "staging", "*.secret"))
	if err != nil || len(staged) != 0 {
		t.Fatal("claimed staging bytes retained")
	}
	declared := filepath.Join(t.TempDir(), "declared")
	if err = os.WriteFile(declared, []byte(secret), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = readPrivate(declared); err == nil {
		t.Fatal("public secret file accepted")
	}
	v.Eligible = func(context.Context, string) bool { return false }
	if _, _, err = v.Secret(ctx, credential.ID); err == nil {
		t.Fatal("ineligible node could read managed secret")
	}
}
func TestManagedTokenReadsRealHTTPSGitWithoutPersistingSecret(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	v := credentialVault(t)
	token := "fixture-git-token"
	request := domain.SecretImport{ID: "cmd_git", Name: "git", Kind: "git-http-token", StorageNodeID: "n_holder"}
	command, err := v.Stage(ctx, request, strings.NewReader(token))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = v.importCredential(ctx, &service.Execution{Command: command}); err != nil {
		t.Fatal(err)
	}
	remote := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = remote
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@localhost", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@localhost")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatal(err, string(output))
		}
	}
	git("init", "-b", "main")
	os.WriteFile(filepath.Join(remote, "file"), []byte("fixture"), 0600)
	git("add", "file")
	git("commit", "-m", "fixture")
	git("update-server-info")
	var authenticated atomic.Int64
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, password, ok := r.BasicAuth()
		if !ok || password != token {
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			w.WriteHeader(401)
			return
		}
		authenticated.Add(1)
		http.FileServer(http.Dir(filepath.Join(remote, ".git"))).ServeHTTP(w, r)
	}))
	defer peer.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: peer.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	g, err := gitstore.New(filepath.Join(t.TempDir(), "git"))
	if err != nil {
		t.Fatal(err)
	}
	g.Authorize = func(ctx context.Context, ref, remote string) ([]string, func(), error) {
		env, cleanup, err := v.GitEnvironment(ctx, ref, remote)
		return append(env, "GIT_SSL_CAINFO="+ca), cleanup, err
	}
	if err = g.CheckRemote(ctx, peer.URL, "main", "git"); err != nil {
		t.Fatal("managed read check", err)
	}
	ref := "git"
	if _, err = g.Fetch(ctx, "r_private", peer.URL, "main", &ref); err != nil {
		t.Fatal("managed fetch", err)
	}
	if authenticated.Load() < 2 {
		t.Fatal("Git credential was not used")
	}
	if err = filepath.WalkDir(g.Root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if bytes.Contains(data, []byte(token)) {
				t.Fatal("secret persisted in repository")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	entries, _ := filepath.Glob(filepath.Join(v.Root, "git-use-*"))
	if len(entries) != 0 {
		t.Fatal("temporary Git secrets retained")
	}
	if _, _, err = v.GitEnvironment(ctx, "git", "http://example.invalid/repo"); err == nil {
		t.Fatal("token permitted over plaintext")
	}
}
