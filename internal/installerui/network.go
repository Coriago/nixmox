// Package installerui owns the installer's interactive network setup.
package installerui

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

type Device struct{ Name, Kind string }
type Network struct{ Active, BSSID, SSID, Signal, Security, Device string }

type Backend interface {
	Wait(context.Context)
	Devices(context.Context) ([]Device, error)
	Ready(Device) bool
	Scan(context.Context) ([]Network, error)
	Connect(context.Context, Network, io.Reader, io.Writer) error
	EnableWiFi(context.Context) error
}

type NetworkManager struct{}

func output(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "nmcli", args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	data, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("NetworkManager command failed; check that NetworkManager is running: %w", err)
	}
	return data, nil
}

func (NetworkManager) Wait(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 21*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "nm-online", "--quiet", "--wait-for-startup", "--timeout=20").Run()
}

// nmcli escapes colons and backslashes in terse output, including BSSIDs/SSIDs.
func fields(line string) []string {
	var out []string
	var field strings.Builder
	escaped := false
	for _, ch := range line {
		if escaped {
			field.WriteRune(ch)
			escaped = false
			continue
		}
		if ch == '\\' {
			escaped = true
			continue
		}
		if ch == ':' {
			out = append(out, field.String())
			field.Reset()
			continue
		}
		field.WriteRune(ch)
	}
	if escaped {
		field.WriteRune('\\')
	}
	return append(out, field.String())
}

func (NetworkManager) Devices(ctx context.Context) ([]Device, error) {
	data, err := output(ctx, "--terse", "--escape", "yes", "--fields", "DEVICE,TYPE", "device", "status")
	if err != nil {
		return nil, err
	}
	var devices []Device
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		f := fields(line)
		if len(f) == 2 {
			devices = append(devices, Device{f[0], f[1]})
		}
	}
	return devices, nil
}

func (NetworkManager) Ready(d Device) bool {
	if d.Kind != "ethernet" && d.Kind != "wifi" {
		return false
	}
	i, err := net.InterfaceByName(d.Name)
	if err != nil || i.Flags&net.FlagUp == 0 || i.Flags&net.FlagRunning == 0 {
		return false
	}
	addresses, err := i.Addrs()
	if err != nil {
		return false
	}
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err == nil && ip.To4() != nil && ip.IsGlobalUnicast() {
			return true
		}
	}
	return false
}

func (NetworkManager) Scan(ctx context.Context) ([]Network, error) {
	data, err := output(ctx, "--wait", "20", "--terse", "--escape", "yes", "--fields", "IN-USE,BSSID,SSID,SIGNAL,SECURITY,DEVICE", "device", "wifi", "list", "--rescan", "yes")
	if err != nil {
		return nil, err
	}
	var networks []Network
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		f := fields(line)
		if len(f) != 6 || f[2] == "" {
			continue
		}
		if mac, err := net.ParseMAC(f[1]); err != nil || len(mac) != 6 {
			continue
		}
		networks = append(networks, Network{f[0], f[1], f[2], f[3], f[4], f[5]})
	}
	return networks, nil
}

func (NetworkManager) Connect(ctx context.Context, network Network, in io.Reader, out io.Writer) error {
	// NetworkManager owns secret entry directly on the terminal. No password
	// is collected by nixmox, stored in arguments, or sent to the daemon log.
	cmd := exec.CommandContext(ctx, "nmcli", "--ask", "--colors", "no", "--wait", "90", "device", "wifi", "connect", network.BSSID, "ifname", network.Device, "private", "no")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, out
	return cmd.Run()
}

func (NetworkManager) EnableWiFi(ctx context.Context) error {
	_, err := output(ctx, "radio", "wifi", "on")
	return err
}
