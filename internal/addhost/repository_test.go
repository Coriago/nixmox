package addhost

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testCommand(t *testing.T, dir string, env []string, name string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v: %s", name, args, err, data)
	}
	return data
}
func TestPublishSOPSAndGit(t *testing.T) {
	for _, name := range []string{"git", "sops", "age-keygen"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip("run in nix develop for Git/SOPS integration")
		}
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	oldKey, newKey := filepath.Join(root, "old.age"), filepath.Join(root, "new.age")
	testCommand(t, root, nil, "age-keygen", "-o", oldKey)
	testCommand(t, root, nil, "age-keygen", "-o", newKey)
	oldRecipient := strings.TrimSpace(string(testCommand(t, root, nil, "age-keygen", "-y", oldKey)))
	newRecipient := strings.TrimSpace(string(testCommand(t, root, nil, "age-keygen", "-y", newKey)))
	rules := "creation_rules:\n  - path_regex: '^secrets.yaml$'\n    age: " + oldRecipient + "\n"
	if err := os.WriteFile(filepath.Join(source, ".sops.yaml"), []byte(rules), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "secrets.yaml"), []byte("password: test-private-value\n"), 0600); err != nil {
		t.Fatal(err)
	}
	encrypted := testCommand(t, source, nil, "sops", "--encrypt", "secrets.yaml")
	if err := os.WriteFile(filepath.Join(source, "secrets.yaml"), encrypted, 0600); err != nil {
		t.Fatal(err)
	}
	testCommand(t, source, nil, "git", "init", "-b", "main")
	testCommand(t, source, nil, "git", "add", ".")
	testCommand(t, source, nil, "git", "-c", "user.name=Test", "-c", "user.email=test@example.test", "commit", "-m", "seed")
	remote := filepath.Join(root, "remote.git")
	testCommand(t, root, nil, "git", "clone", "--bare", source, remote)
	c := Config{SOPS: &SOPSConfig{RulesFile: ".sops.yaml", Files: []string{"secrets.yaml"}, RulePathRegex: "^secrets.yaml$"}}
	repo := Repository{URL: remote, Branch: "main", Directory: filepath.Join(root, "prepared"), AgeKey: oldKey}
	var log bytes.Buffer
	sha, err := repo.Publish(context.Background(), c, Result{Path: "hosts/newhost.nix", Content: "{ ... }: {}"}, newRecipient, &log)
	if err != nil {
		t.Fatal(err)
	}
	remoteSHA := strings.TrimSpace(string(testCommand(t, root, nil, "git", "--git-dir", remote, "rev-parse", "main")))
	if sha != remoteSHA {
		t.Fatal("published revision mismatch")
	}
	cleartext := testCommand(t, repo.Directory, []string{"SOPS_AGE_KEY_FILE=" + newKey, "SOPS_AGE_KEY="}, "sops", "--decrypt", "secrets.yaml")
	if !strings.Contains(string(cleartext), "test-private-value") {
		t.Fatal("new installer identity cannot decrypt")
	}
	committed := testCommand(t, root, nil, "git", "--git-dir", remote, "show", "main:secrets.yaml")
	if bytes.Contains(committed, []byte("test-private-value")) || strings.Contains(log.String(), "AGE-SECRET-KEY") {
		t.Fatal("secret leaked")
	}
	sourceRules, _ := os.ReadFile(filepath.Join(source, ".sops.yaml"))
	if string(sourceRules) != rules {
		t.Fatal("mutated live checkout")
	}
	for _, failure := range []string{"unauthorized-age", "rejected-push"} {
		t.Run(failure, func(t *testing.T) {
			failed := repo
			failed.Directory = filepath.Join(root, failure)
			recipient := newRecipient
			if failure == "unauthorized-age" {
				third := filepath.Join(root, "third.age")
				testCommand(t, root, nil, "age-keygen", "-o", third)
				failed.AgeKey = third
				recipient = strings.TrimSpace(string(testCommand(t, root, nil, "age-keygen", "-y", third)))
			} else {
				if err := os.WriteFile(filepath.Join(remote, "hooks", "pre-receive"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			_, err := failed.Publish(context.Background(), c, Result{Path: "hosts/" + failure + ".nix", Content: "{}"}, recipient, &log)
			if err == nil {
				t.Fatal("expected stop on failure")
			}
			after := strings.TrimSpace(string(testCommand(t, root, nil, "git", "--git-dir", remote, "rev-parse", "main")))
			if after != sha {
				t.Fatal("failure advanced remote")
			}
		})
	}
}
func TestRulesKeepOtherRecipientsAndRejectWrongRule(t *testing.T) {
	repo := t.TempDir()
	path := filepath.Join(repo, ".sops.yaml")
	original := "keys: &shared age1old\ncreation_rules:\n  - path_regex: '^one.yaml$'\n    age: *shared\n  - path_regex: '^two.yaml$'\n    age: *shared\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	c := SOPSConfig{RulesFile: ".sops.yaml", Files: []string{"one.yaml"}, RulePathRegex: "^one.yaml$"}
	if err := UpdateRules(repo, c, "age1new"); err != nil {
		t.Fatal(err)
	}
	result, _ := os.ReadFile(path)
	if bytes.Count(result, []byte("age1new")) != 1 || bytes.Count(result, []byte("age1old")) != 3 {
		t.Fatal(string(result))
	}
	c.Files = []string{"two.yaml"}
	if err := UpdateRules(repo, c, "age1wrong"); err == nil {
		t.Fatal("updated wrong rule")
	}
}
func TestDiskInventoryExcludesInstallerAndPartitions(t *testing.T) {
	disks, err := ParseDisks([]byte(`{"blockdevices":[{"path":"/dev/sda","type":"disk","size":100,"children":[{"type":"part","mountpoints":["/iso"]}]},{"path":"/dev/nvme0n1","type":"disk","size":200,"model":"Drive","serial":"serial"},{"path":"/dev/sdb1","type":"part","size":100},{"path":"/dev/sdc","type":"disk","ro":true,"size":100}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(disks) != 1 || disks[0].Path != "/dev/nvme0n1" || disks[0].Serial != "serial" {
		t.Fatalf("unsafe disk selection: %+v", disks)
	}
}

func TestRetryDiskInventoryOnlyAllowsRecordedInstallMounts(t *testing.T) {
	data := []byte(`{"blockdevices":[{"path":"/dev/vda","type":"disk","size":100,"children":[{"type":"part","mountpoints":["/mnt"]}]},{"path":"/dev/vdb","type":"disk","size":100,"children":[{"type":"part","mountpoints":["/"]}]},{"path":"/dev/vdc","type":"disk","size":100,"fstype":"iso9660"}]}`)
	disks, err := ParseDisks(data)
	if err != nil || len(disks) != 0 {
		t.Fatal(disks, err)
	}
	disks, err = parseDisks(data, "/dev/vda")
	if err != nil || len(disks) != 1 || disks[0].Path != "/dev/vda" {
		t.Fatal(disks, err)
	}
	disks, err = parseDisks(data, "/dev/vdb")
	if err != nil || len(disks) != 0 {
		t.Fatal("retry exposed live system disk", disks, err)
	}
}

func TestSOPSRulesMatchRelativeToRulesDirectory(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, "secrets"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "secrets/.sops.yaml"), []byte("creation_rules:\n  - path_regex: '^host.yaml$'\n    age: age1old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := UpdateRules(repo, SOPSConfig{RulesFile: "secrets/.sops.yaml", Files: []string{"secrets/host.yaml"}, RulePathRegex: "^host.yaml$"}, "age1new"); err != nil {
		t.Fatal(err)
	}
}
