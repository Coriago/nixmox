package addhost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEvaluatedInstallGuards(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	p := InstallPlan{Hostname: "newhost", ClusterAddress: "node1:8080", Drive: Disk{Path: "/dev/vda", Whole: true}}
	for name, result := range map[string]string{
		"valid":         `{"enabled":true,"seed":"node1:8080","hostname":"newhost","networkManager":true,"disks":["/dev/vda"]}`,
		"wrong-disk":    `{"enabled":true,"seed":"node1:8080","hostname":"newhost","disks":["/dev/vdb"]}`,
		"extra-disk":    `{"enabled":true,"seed":"node1:8080","hostname":"newhost","disks":["/dev/vda","/dev/vdb"]}`,
		"no-agent":      `{"enabled":false,"seed":"node1:8080","hostname":"newhost","disks":["/dev/vda"]}`,
		"wrong-cluster": `{"enabled":true,"seed":"other:8080","hostname":"newhost","disks":["/dev/vda"]}`,
		"wrong-name":    `{"enabled":true,"seed":"node1:8080","hostname":"other","disks":["/dev/vda"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(bin, "nix"), []byte("#!/bin/sh\nprintf '%s' '"+result+"'\n"), 0700); err != nil {
				t.Fatal(err)
			}
			nm, err := CheckInstall(context.Background(), "/checkout", p)
			if name == "valid" {
				if err != nil || !nm {
					t.Fatal(nm, err)
				}
			} else if err == nil {
				t.Fatal("unsafe evaluated configuration accepted")
			}
		})
	}
}
func TestPrivateCredentialCopy(t *testing.T) {
	dir := t.TempDir()
	source, dest := filepath.Join(dir, "source"), filepath.Join(dir, "dest")
	if err := os.WriteFile(source, []byte("private credential"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := copyPrivate(source, dest); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dest)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	if err := copyPrivate(link, dest); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatal("followed a credential symlink")
	}
}

func TestWiFiProfilesTransferOnlyActiveSelfContainedCredentials(t *testing.T) {
	dir := t.TempDir()
	profile := filepath.Join(dir, "active.nmconnection")
	valid := "[connection]\nuuid=active-id\n[wifi-security]\nkey-mgmt=wpa-psk\npsk=private-wifi-password\npsk-flags=0\n"
	if err := os.WriteFile(profile, []byte(valid), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "other.nmconnection"), []byte("[connection]\nuuid=other-id\n"), 0600); err != nil {
		t.Fatal(err)
	}
	files, err := activeWiFiProfiles(dir, []string{"active-id"})
	if err != nil || len(files) != 1 || files[0] != profile {
		t.Fatal(files, err)
	}
	for name, content := range map[string]string{"agent-secret": strings.Replace(valid, "psk-flags=0", "psk-flags=1", 1), "external-cert": valid + "[802-1x]\nclient-cert=/run/secrets/cert\n", "missing-psk": strings.Replace(valid, "psk=private-wifi-password\n", "", 1)} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(profile, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := activeWiFiProfiles(dir, []string{"active-id"}); err == nil {
				t.Fatal("silently accepted unsupported Wi-Fi credentials")
			}
		})
	}
}
