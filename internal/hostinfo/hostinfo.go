package hostinfo

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"strings"
	"time"

	"nixmox/internal/config"
)

type HostInfo struct {
	Hostname     string        `json:"hostname"`
	NixOSVersion string        `json:"nixos_version"`
	Kernel       string        `json:"kernel"`
	Uptime       time.Duration `json:"uptime"`
	AgentVersion string        `json:"agent_version"`
	CurrentTime  time.Time     `json:"current_time"`
}

var (
	osReleasePath = "/etc/os-release"
	uptimePath    = "/proc/uptime"
)

func Get(cfg *config.Config) HostInfo {
	hostname := cfg.Hostname
	if hostname == "" {
		hostname, _ = os.Hostname()
	}
	if hostname == "" {
		hostname = "unknown"
	}

	nixosVersion := "unknown"
	if data, err := os.ReadFile(osReleasePath); err == nil {
		scanner := bufio.NewScanner(bytes.NewReader(data))
		isNixOS := false
		version := ""
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "ID=nixos") || strings.HasPrefix(line, "ID=\"nixos\"") {
				isNixOS = true
			}
			if strings.HasPrefix(line, "VERSION=") {
				version = strings.Trim(strings.TrimPrefix(line, "VERSION="), "\"")
			}
		}
		if isNixOS && version != "" {
			nixosVersion = version
		}
	}

	kernel := "unknown"
	if out, err := exec.Command("uname", "-r").Output(); err == nil {
		kernel = strings.TrimSpace(string(out))
	}

	var uptime time.Duration
	if data, err := os.ReadFile(uptimePath); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) > 0 {
			if d, err := time.ParseDuration(fields[0] + "s"); err == nil {
				uptime = d
			}
		}
	}

	return HostInfo{
		Hostname:     hostname,
		NixOSVersion: nixosVersion,
		Kernel:       kernel,
		Uptime:       uptime,
		AgentVersion: config.Version,
		CurrentTime:  time.Now(),
	}
}
