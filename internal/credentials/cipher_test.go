package credentials_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"nixmox/internal/credentials"
)

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
