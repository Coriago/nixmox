package credentials_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"golang.org/x/crypto/ssh"
	"nixmox/internal/credentials"
)

// A copied bootstrap binary derives the same host identity without helper tools.
func TestBootstrapIdentity(t *testing.T) {
	// --- Test setup ---
	// ------------------
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	directory := t.TempDir()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "bootstrap-example")
	if err != nil {
		t.Fatal(err)
	}
	hostKey := filepath.Join(directory, "host.key")
	if err = os.WriteFile(hostKey, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	legacy, err := exec.CommandContext(ctx, "ssh-to-age", "-private-key", "-i", hostKey).Output()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(legacy)
	t.Setenv("PATH", t.TempDir()) // Restored automatically; no executable can be used.

	// --- Usage ---
	// -------------
	destination := filepath.Join(directory, "age.key")
	public, err := credentials.DeriveIdentity(ctx, hostKey, destination)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(persisted)
	identity, err := age.ParseX25519Identity(strings.TrimSpace(string(legacy)))
	if err != nil {
		t.Fatal(err)
	}

	// --- Assertions ---
	// ------------------
	if string(persisted) != string(legacy) || public != identity.Recipient().String() {
		t.Fatal("in-process conversion changed the existing host identity")
	}
}

func TestSOPSRecipients(t *testing.T) {
	// --- Test setup ---
	// ------------------
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	makeHost := func(name string) (credentials.Cipher, credentials.Recipient) {
		t.Helper()
		dir := t.TempDir()
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		block, err := ssh.MarshalPrivateKey(key, "integration")
		if err != nil {
			t.Fatal(err)
		}
		hostKey := filepath.Join(dir, "ssh_host_ed25519_key")
		if err = os.WriteFile(hostKey, pem.EncodeToMemory(block), 0600); err != nil {
			t.Fatal(err)
		}
		ageKey := filepath.Join(dir, "age.key")
		public, err := credentials.DeriveIdentity(ctx, hostKey, ageKey)
		if err != nil {
			t.Fatal(err)
		}
		return credentials.Cipher{KeyFile: ageKey}, credentials.Recipient{NodeID: name, AgePublicKey: public}
	}
	first, a := makeHost("cedar")
	second, b := makeHost("birch")

	// --- Usage ---
	// -------------
	encrypted, err := first.Encrypt(ctx, []byte("example-secret"), []credentials.Recipient{a})
	if err != nil {
		t.Fatal(err)
	}
	shared, err := first.Rekey(ctx, encrypted, []credentials.Recipient{a, b})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := second.Decrypt(ctx, shared)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(plain)
	revoked, err := second.Rekey(ctx, shared, []credentials.Recipient{b})
	if err != nil {
		t.Fatal(err)
	}
	_, denied := first.Decrypt(ctx, revoked)

	// --- Assertions ---
	// ------------------
	if strings.Contains(encrypted, "example-secret") || !strings.Contains(encrypted, `"sops"`) {
		t.Fatal("not a complete encrypted SOPS document")
	}
	if string(plain) != "example-secret" {
		t.Fatal("promoted host could not read original secret")
	}
	if denied == nil {
		t.Fatal("removed recipient can still decrypt new document")
	}
}
