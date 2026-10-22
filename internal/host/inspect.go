// Package host collects operating-system evidence without changing system state.
package host

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"nixmox/internal/config"
	"nixmox/internal/domain"
	"nixmox/internal/network"
	"nixmox/internal/process"
)

type Inspector struct {
	Config      config.Config
	BrokerReady func() bool
	CacheReady  func(context.Context) bool
}

func (i Inspector) Inspect(ctx context.Context, node string) (domain.HostSample, error) {
	now := time.Now().UTC()
	name, err := os.Hostname()
	if err != nil {
		return domain.HostSample{}, domain.Err("OBSERVATION_UNAVAILABLE", "Host name is unavailable.", 503)
	}
	system := runtime.GOARCH + "-" + runtime.GOOS
	if runtime.GOARCH == "amd64" {
		system = "x86_64-" + runtime.GOOS
	}
	if runtime.GOARCH == "arm64" {
		system = "aarch64-" + runtime.GOOS
	}
	sample := domain.HostSample{SchemaVersion: 1, HostID: node, Name: name, System: system, Environment: "bootstrap", ObservedAt: now, Freshness: "live", Health: domain.Health{Status: "healthy", ObservedAt: &now, Reasons: []domain.HealthReason{}}, Endpoints: []domain.Endpoint{}}
	if i.CacheReady != nil {
		sample.Capabilities.CacheServe = i.CacheReady(ctx)
	}
	if running, err := filepath.EvalSymlinks("/run/current-system"); err == nil && strings.HasPrefix(running, "/nix/store/") {
		sample.RunningOutput = &running
		if info, err := os.Stat(filepath.Join(running, "bin/switch-to-configuration")); err == nil && info.Mode()&0111 != 0 {
			sample.Capabilities.Apply = true
		}
	}
	if _, err := exec.LookPath("nix"); err == nil {
		_, err := process.Run(ctx, process.Options{Timeout: 3 * time.Second, MaxOutput: 64 << 10}, "nix", "store", "ping", "--json")
		sample.Capabilities.Build = err == nil
	}
	issue := func(status, code, resource, message string) {
		sample.Health.Reasons = append(sample.Health.Reasons, domain.HealthReason{Code: code, Resource: resource, Message: message})
		if sample.Health.Status != "unhealthy" && (status == "unhealthy" || sample.Health.Status != "unknown") {
			sample.Health.Status = status
		}
	}
	if i.BrokerReady == nil || !i.BrokerReady() {
		issue("unhealthy", "BROKER_UNAVAILABLE", "nats", "The local authenticated broker is unavailable.")
	}
	if err := writable(i.Config.DataDir); err != nil {
		issue("unhealthy", "STATE_NOT_WRITABLE", "state", "Nixmox state storage is not writable.")
	}
	options := process.Options{MaxOutput: 64 << 10, Timeout: 3 * time.Second}
	failed, err := process.Run(ctx, options, "systemctl", "--no-pager", "--plain", "--no-legend", "--failed", "list-units", "--all")
	if err != nil {
		issue("unknown", "SYSTEMD_UNAVAILABLE", "systemd", "Failed-unit status could not be collected.")
	} else {
		for index, line := range strings.Split(strings.TrimSpace(string(failed.Stdout)), "\n") {
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			if index >= 256 {
				issue("unknown", "CHECK_LIMIT", "systemd", "Failed-unit results exceeded the collection limit.")
				break
			}
			unit := fields[0]
			if len(unit) > 256 {
				continue
			}
			status := "degraded"
			if unit == "nixmox.service" {
				status = "unhealthy"
			}
			issue(status, "SYSTEMD_UNIT_FAILED", unit, "Systemd unit failed.")
		}
	}
	// The marker identifies the managed image; PID evidence distinguishes its
	// permanent agent from a temporary bootstrap process on that same machine.
	marker, err := os.ReadFile("/etc/nixmox/environment")
	if err == nil && sample.RunningOutput != nil {
		environment := strings.TrimSpace(string(marker))
		if environment == "installed" || environment == "installer" {
			pid, pidErr := process.Run(ctx, options, "systemctl", "--no-pager", "show", "nixmox.service", "--property=MainPID", "--value")
			if pidErr == nil && strings.TrimSpace(string(pid.Stdout)) == strconv.Itoa(os.Getpid()) {
				sample.Environment = environment
			}
		}
	}
	addresses, err := network.BindAddresses(i.Config)
	if err != nil {
		issue("degraded", "MANAGEMENT_UNAVAILABLE", "network", "Configured management endpoints are unavailable.")
	} else {
		for _, address := range addresses {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				continue
			}
			sample.Endpoints = append(sample.Endpoints, domain.Endpoint{Service: "api", Transport: "http", Address: address}, domain.Endpoint{Service: "machine", Transport: "https", Address: net.JoinHostPort(host, strconv.Itoa(i.Config.Machine.Port))})
			if len(sample.Endpoints) >= 64 {
				break
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return domain.HostSample{}, err
	}
	return sample, nil
}
func writable(dir string) error {
	f, err := os.CreateTemp(dir, ".health-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write([]byte{1}); err != nil {
		return err
	}
	return f.Sync()
}
