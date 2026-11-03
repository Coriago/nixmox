package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"nixmox/internal/host"
	"nixmox/internal/installer/installers"
)

// InspectLocal is called only after the service verifies the requested host ID.
// It probes capabilities, not whether any particular kernel can be loaded.
func InspectLocal(ctx context.Context, nodeID, environment string) (installers.Target, error) {
	target := installers.Target{NodeID: nodeID, Environment: environment, Firmware: "bios"}
	switch runtime.GOARCH {
	case "amd64":
		target.System = "x86_64-linux"
	case "arm64":
		target.System = "aarch64-linux"
		target.Firmware = "unknown"
	default:
		target.System = runtime.GOARCH + "-linux"
	}
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return target, err
	}
	target.BootID = strings.TrimSpace(string(data))
	target.SystemPath, err = filepath.EvalSymlinks("/run/current-system")
	if err != nil {
		return target, err
	}
	if _, err = os.Stat("/sys/firmware/efi"); err == nil {
		target.Firmware = "uefi"
	} else if !errors.Is(err, os.ErrNotExist) {
		return target, err
	}
	for _, mode := range []string{"--container", "--vm"} {
		data, err = (host.Process{}).Run(ctx, "", nil, "systemd-detect-virt", mode)
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				return target, fmt.Errorf("detect virtualization: %w", err)
			}
			continue
		}
		if mode == "--container" {
			target.Container = true
		} else {
			target.Virtualization = strings.TrimSpace(string(data))
		}
	}
	disabled, err := os.ReadFile("/proc/sys/kernel/kexec_load_disabled")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return target, err
	}
	_, toolErr := exec.LookPath("kexec")
	target.KexecAvailable = err == nil && strings.TrimSpace(string(disabled)) == "0" && toolErr == nil && os.Geteuid() == 0
	lockdown, err := os.ReadFile("/sys/kernel/security/lockdown")
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return target, err
	}
	if err == nil && !strings.Contains(string(lockdown), "[none]") {
		target.KexecAvailable = false
	}
	return target, nil
}
