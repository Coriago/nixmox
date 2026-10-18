package addhost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Run the actual upstream argument parser and phase functions, intercepting
// SSH and Nix operations so this contract test cannot touch disks or install.
func TestAnywhereBuildsInTargetStoreAfterPartitioning(t *testing.T) {
	upstream := os.Getenv("NIXMOX_TEST_ANYWHERE_SOURCE")
	if upstream == "" {
		t.Skip("set NIXMOX_TEST_ANYWHERE_SOURCE to the packaged nixos-anywhere script")
	}
	source, err := os.ReadFile(upstream)
	if err != nil {
		t.Fatal(err)
	}
	const main = `main "$@"`
	body := strings.TrimSpace(string(source))
	if !strings.HasSuffix(body, main) {
		t.Fatal("upstream entry point changed; review target-store behavior")
	}
	body = strings.TrimSuffix(body, main)
	dir := t.TempDir()
	trace := filepath.Join(dir, "trace")
	t.Setenv("NIXMOX_TEST_TRACE", trace)
	harness := body + `
parseArgs "$@"
test "$sshConnection" = root@127.0.0.1
test "$buildOn" = remote
test "${phases[disko]}" = 1
test "${phases[install]}" = 1
test "${phases[reboot]}" = 0
nixBuild() {
  printf 'build:%s\n' "$*" >> "$NIXMOX_TEST_TRACE"
  case "$*" in
    *system.build.toplevel*) printf '/nix/store/test-system\n';;
    *) printf '/nix/store/test-disko\n';;
  esac
}
nixCopy() { printf 'copy:%s\n' "$*" >> "$NIXMOX_TEST_TRACE"; }
runSsh() {
  printf 'ssh:%s\n' "$*" >> "$NIXMOX_TEST_TRACE"
  case "$*" in sh|tar*) cat >/dev/null;; esac
}
runDisko ""
nixosInstall ""
`
	if err := os.WriteFile(filepath.Join(dir, "nixos-anywhere"), []byte(harness), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	extra := filepath.Join(dir, "extra")
	if err := os.Mkdir(extra, 0700); err != nil {
		t.Fatal(err)
	}
	var log strings.Builder
	if err := runLocalAnywhere(context.Background(), "path:/checkout#test", 12345, "/unused-key", extra, &log); err != nil {
		t.Fatal(err, log.String())
	}
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	partition, build := -1, -1
	for i, line := range lines {
		if line == "ssh:/nix/store/test-disko" {
			partition = i
		}
		if strings.HasPrefix(line, "build:") && strings.Contains(line, "system.build.toplevel") {
			build = i
			if !strings.Contains(line, "remote-store=local%3Froot=%2Fmnt") {
				t.Fatal("system built outside target store", line)
			}
		}
	}
	if partition < 0 || build <= partition {
		t.Fatal("system built before target disk was prepared", string(data))
	}
}
