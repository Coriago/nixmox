package hostinfo

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agessh "github.com/Mic92/ssh-to-age"
	"golang.org/x/crypto/ssh"
)

func TestAgeRecipientFromHostPrivateKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ssh_host_ed25519_key")
	var previous string
	for i := 0; i < 2; i++ {
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		block, err := ssh.MarshalPrivateKey(key, "test")
		if err != nil {
			t.Fatal(err)
		}
		data := pem.EncodeToMemory(block)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		// A stale .pub must never override the actual private host identity.
		if err := os.WriteFile(path+".pub", []byte("stale public key"), 0644); err != nil {
			t.Fatal(err)
		}
		_, expected, err := agessh.SSHPrivateKeyToAge(data, nil)
		if err != nil {
			t.Fatal(err)
		}
		got, problem := readAgeRecipient(path)
		if problem != "" || got != *expected || !strings.HasPrefix(got, "age1") || got == previous {
			t.Fatalf("recipient mismatch or rotation ignored: %q %q", got, problem)
		}
		previous = got
	}
}

func TestAgeRecipientUnavailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if recipient, problem := readAgeRecipient(path); recipient != "" || !strings.Contains(problem, "missing") {
		t.Fatalf("missing: %q %q", recipient, problem)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{[]byte("malformed PRIVATE DATA"), pem.EncodeToMemory(block)} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		recipient, problem := readAgeRecipient(path)
		if recipient != "" || problem == "" || strings.Contains(problem, "PRIVATE DATA") {
			t.Fatalf("invalid key not handled safely: %q %q", recipient, problem)
		}
	}
}
