package media

import (
	"crypto/ed25519"
	"crypto/rand"
	"golang.org/x/crypto/ssh"
	"strings"
	"testing"
)

func TestInstallerBuildArgs(t *testing.T) {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := NormalizeKey(string(ssh.MarshalAuthorizedKey(key)))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"secret-key", normalized + "\n" + normalized, `command="evil" ` + normalized} {
		if _, err := NormalizeKey(bad); err == nil {
			t.Fatal("accepted", bad)
		}
	}
	args := BuildArgs("github:Coriago/nixmox/revision#installer-iso", normalized, "/tmp/image")
	expr := args[len(args)-1]
	if !strings.Contains(expr, `system = "x86_64-linux"`) || !strings.Contains(expr, "sshKeys") || strings.Contains(expr, "#installer-iso") {
		t.Fatal(args)
	}
	plain := BuildArgs("path:/trusted#iso", "", "/tmp/image")
	if plain[len(plain)-1] != "path:/trusted#iso" {
		t.Fatal(plain)
	}
	if got := nixString(`${builtins.abort "bad"}`); !strings.Contains(got, `\${`) {
		t.Fatal(got)
	}
}
