package addhost

import (
	"context"
	"encoding/json"
	"fmt"
	agessh "github.com/Mic92/ssh-to-age"
	"golang.org/x/crypto/ssh"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Network struct {
	WiFi         bool     `json:"wifi"`
	Transferable bool     `json:"transferable"`
	Message      string   `json:"message"`
	Profiles     []string `json:"-"`
}

func InspectNetwork(ctx context.Context) Network {
	b, err := exec.CommandContext(ctx, "nmcli", "-t", "-f", "TYPE,UUID", "connection", "show", "--active").Output()
	if err != nil {
		return Network{Message: "Network manager could not be inspected. Confirm an alternative connection before installing."}
	}
	var active []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "802-11-wireless:") {
			active = append(active, strings.TrimPrefix(line, "802-11-wireless:"))
		}
	}
	if len(active) > 0 {
		profiles, err := activeWiFiProfiles("/etc/NetworkManager/system-connections", active)
		if err != nil {
			return Network{WiFi: true, Message: "The active Wi-Fi profile uses unavailable secrets or unsupported credentials. Confirm an alternative network connection before installing."}
		}
		return Network{WiFi: true, Transferable: true, Profiles: profiles, Message: "The active Wi-Fi profile can be copied locally if the installed configuration enables NetworkManager."}
	}
	return Network{Transferable: true, Message: "No active Wi-Fi connection detected; keep the wired connection attached."}
}

func activeWiFiProfiles(directory string, active []string) ([]string, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	wanted := map[string]bool{}
	for _, id := range active {
		wanted[id] = true
	}
	var profiles []string
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		values := map[string]string{}
		section := ""
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "[") {
				section = line
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			if ok {
				values[section+key] = value
			}
		}
		clear(data)
		id := values["[connection]uuid"]
		if !wanted[id] {
			continue
		}
		for key, value := range values {
			if strings.HasPrefix(key, "[802-1x]") || strings.HasSuffix(key, "psk-flags") && value != "0" {
				return nil, fmt.Errorf("unsupported Wi-Fi credentials")
			}
		}
		security := values["[wifi-security]key-mgmt"]
		if security != "" && security != "wpa-psk" && security != "sae" {
			return nil, fmt.Errorf("unsupported Wi-Fi security")
		}
		if security != "" && values["[wifi-security]psk"] == "" {
			return nil, fmt.Errorf("Wi-Fi credentials are not stored in the profile")
		}
		profiles = append(profiles, path)
		delete(wanted, id)
	}
	if len(wanted) != 0 {
		return nil, fmt.Errorf("active Wi-Fi profile is unavailable")
	}
	return profiles, nil
}

type InstallPlan struct {
	ID                 string `json:"id"`
	Repository         string `json:"repository"`
	Branch             string `json:"branch"`
	Revision           string `json:"revision"`
	Hostname           string `json:"hostname"`
	FlakeDirectory     string `json:"flake_directory"`
	Drive              Disk   `json:"drive"`
	ClusterAddress     string `json:"cluster_address"`
	AlternativeNetwork bool   `json:"alternative_network"`
	BootID             string `json:"boot_id"`
	Recipient          string `json:"recipient"`
}

// Verify the evaluated repository actually targets the confirmed disk, enables
// nixmox and joins the expected cluster. Evaluation is local on the installer;
// building and installation remain nixos-anywhere's responsibility.
func CheckInstall(ctx context.Context, checkout string, p InstallPlan) (bool, error) {
	if !hostname.MatchString(p.Hostname) || (p.FlakeDirectory != "" && !filepath.IsLocal(p.FlakeDirectory)) {
		return false, fmt.Errorf("invalid host or flake directory")
	}
	flake := "path:" + filepath.Join(checkout, p.FlakeDirectory) + "#nixosConfigurations." + p.Hostname + ".config"
	expression := `c: { enabled = c.services.nixmox.enable or false; installer = c.services.nixmox.installerMode or false; seed = c.services.nixmox.joinSeed or ""; hostname = c.networking.hostName; networkManager = c.networking.networkmanager.enable or false; disks = builtins.map (d: d.device) (builtins.attrValues (c.disko.devices.disk or {})); }`
	b, err := exec.CommandContext(ctx, "nix", "eval", "--no-write-lock-file", "--json", "--apply", expression, flake).Output()
	if err != nil {
		return false, evaluationError("evaluate selected host configuration", err)
	}
	var checked struct {
		Enabled        bool
		Installer      bool
		Seed           string
		Hostname       string
		NetworkManager bool
		Disks          []string
	}
	if err := json.Unmarshal(b, &checked); err != nil {
		return false, err
	}
	if !checked.Enabled || checked.Installer || checked.Seed != p.ClusterAddress || checked.Hostname != p.Hostname {
		return false, fmt.Errorf("selected configuration must enable nixmox, use the assigned hostname, and join the selected cluster")
	}
	if len(checked.Disks) != 1 || checked.Disks[0] != p.Drive.Path {
		return false, fmt.Errorf("disko must target only the confirmed whole drive %s", p.Drive.Path)
	}
	return checked.NetworkManager, nil
}

// LocalInstall creates a loopback-only SSH transport for nixos-anywhere. Its
// ephemeral authorization is never embedded in the ISO or shared with a peer.
func LocalInstall(ctx context.Context, checkout string, p InstallPlan, out io.Writer) error {
	nm, err := CheckInstall(ctx, checkout, p)
	if err != nil {
		return err
	}
	observation, err := InspectRetry(ctx, p.Drive.Path)
	if err != nil {
		return err
	}
	valid := false
	for _, d := range observation.Disks {
		if d == p.Drive {
			valid = true
		}
	}
	if !valid {
		return fmt.Errorf("selected disk changed or became busy; refresh and confirm before retrying")
	}
	network := InspectNetwork(ctx)
	if (!network.Transferable || network.WiFi && !nm) && !p.AlternativeNetwork {
		return fmt.Errorf("Wi-Fi cannot be transferred to this configuration; confirm an alternative connection plan")
	}
	dir, err := os.MkdirTemp("/run", "nixmox-local-install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	extra := filepath.Join(dir, "extra")
	if err := os.MkdirAll(filepath.Join(extra, "etc/ssh"), 0755); err != nil {
		return err
	}
	// Preserve only host identity, never the ephemeral loopback authorization.
	for _, name := range []string{"ssh_host_ed25519_key", "ssh_host_ed25519_key.pub"} {
		if err := copyPrivate("/etc/ssh/"+name, filepath.Join(extra, "etc/ssh", name)); err != nil {
			return err
		}
	}
	keyData, err := os.ReadFile(filepath.Join(extra, "etc/ssh/ssh_host_ed25519_key"))
	if err != nil {
		return err
	}
	signer, err := ssh.ParsePrivateKey(keyData)
	clear(keyData)
	if err != nil {
		return fmt.Errorf("invalid installer host identity")
	}
	recipient, err := agessh.SSHPublicKeyToAge(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	if err != nil || recipient == nil || *recipient != p.Recipient {
		return fmt.Errorf("installer host key changed after SOPS publication")
	}
	if network.WiFi && nm && network.Transferable {
		dest := filepath.Join(extra, "etc/NetworkManager/system-connections")
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return err
		}
		if err := os.MkdirAll(dest, 0700); err != nil {
			return err
		}
		for _, profile := range network.Profiles {
			if err := copyPrivate(profile, filepath.Join(dest, filepath.Base(profile))); err != nil {
				return err
			}
		}
		fmt.Fprintln(out, "Wi-Fi profiles staged locally with restricted permissions")
	} else if network.WiFi {
		fmt.Fprintln(out, "Wi-Fi transfer unavailable; using the explicitly confirmed alternative connection plan")
	}
	key := filepath.Join(dir, "transport")
	if err := exec.CommandContext(ctx, "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).Run(); err != nil {
		return fmt.Errorf("create local installation transport: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	conf := fmt.Sprintf("ListenAddress 127.0.0.1\nPort %d\nHostKey /etc/ssh/ssh_host_ed25519_key\nAuthorizedKeysFile %s.pub\nPermitRootLogin prohibit-password\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nUsePAM no\nStrictModes yes\nPidFile %s/sshd.pid\n", port, key, dir)
	configFile := filepath.Join(dir, "sshd_config")
	if err := os.WriteFile(configFile, []byte(conf), 0600); err != nil {
		return err
	}
	sshdPath, err := exec.LookPath("sshd")
	if err != nil {
		return err
	}
	sshd := exec.CommandContext(ctx, sshdPath, "-D", "-e", "-f", configFile)
	if err := sshd.Start(); err != nil {
		return fmt.Errorf("start loopback installation transport: %w", err)
	}
	defer func() { _ = sshd.Process.Kill(); _ = sshd.Wait() }()
	ready := false
	for i := 0; i < 30; i++ {
		conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), time.Second)
		if err == nil {
			conn.Close()
			ready = true
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !ready {
		return fmt.Errorf("loopback installation transport did not start")
	}
	flake := "path:" + filepath.Join(checkout, p.FlakeDirectory) + "#" + p.Hostname
	return runLocalAnywhere(ctx, flake, port, key, extra, out)
}

func runLocalAnywhere(ctx context.Context, flake string, port int, key, extra string, out io.Writer) error {
	// "remote" is the loopback target, not another cluster host. nixos-anywhere
	// partitions first, then builds in local?root=/mnt on the confirmed disk.
	// "local" would build the entire system in the live image's RAM-backed store.
	args := []string{"--flake", flake, "--target-host", "root@127.0.0.1", "--ssh-port", strconv.Itoa(port), "-i", key, "--build-on", "remote", "--phases", "disko,install", "--extra-files", extra}
	fmt.Fprintln(out, "Running nixos-anywhere against the confirmed disk: partition first, then build in the target store under /mnt; reboot follows successful installation")
	cmd := exec.CommandContext(ctx, "nixos-anywhere", args...)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("local nixos-anywhere installation failed: %w", err)
	}
	return nil
}

func copyPrivate(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("credential source must be a regular file")
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	defer clear(data)
	return os.WriteFile(destination, data, 0600)
}
