package addhost

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise real Nix signing in a disposable store; never sign the test host store.
func TestTemporaryStoreSigning(t *testing.T) {
	path := os.Getenv("NIXMOX_TEST_UNSIGNED_STORE_PATH")
	if path == "" {
		t.Skip("set NIXMOX_TEST_UNSIGNED_STORE_PATH to an input-addressed store output")
	}
	nix, err := exec.LookPath("nix")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	source := "local?root=" + filepath.Join(root, "source")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(nix, args...)
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v %s", args, err, b)
		}
	}
	run("copy", "--no-check-sigs", "--to", source, path)
	cmd := exec.Command(nix, "--store", source, "store", "verify", "--no-contents", "--sigs-needed", "1", "--option", "trusted-public-keys", "", path)
	if err := cmd.Run(); err == nil {
		t.Fatal("test input needs no signature; use an input-addressed output")
	}
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	// Redirect only the helper's signing operation into the disposable store.
	script := "#!/bin/sh\nexec \"$NIXMOX_TEST_REAL_NIX\" --store \"$NIXMOX_TEST_STORE\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "nix"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NIXMOX_TEST_REAL_NIX", nix)
	t.Setenv("NIXMOX_TEST_STORE", source)
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	var log strings.Builder
	public, err := prepareLocalStoreTrust(context.Background(), root, &log)
	if err != nil {
		t.Fatal(err, log.String())
	}
	run("--store", source, "store", "verify", "--no-contents", "--sigs-needed", "1", "--option", "trusted-public-keys", public, path)
	target := "local?root=" + filepath.Join(root, "target")
	run("copy", "--from", source, "--to", target, "--option", "trusted-public-keys", public, path)
	run("--store", target, "store", "verify", "--no-contents", "--sigs-needed", "1", "--option", "trusted-public-keys", public, path)
	if strings.Contains(log.String(), "nixmox-installer:") {
		t.Fatal("key material in logs")
	}
	info, err := os.Stat(filepath.Join(root, "store-signing-key"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private key permissions", err)
	}
}
