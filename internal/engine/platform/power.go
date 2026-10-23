package platform

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"

	"nixmox/internal/engine"
)

// BootID identifies an OS boot, not a daemon/sampler restart.
func BootID() (string, error) {
	data, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	id := strings.TrimSpace(string(data))
	if len(id) != 36 {
		return "", errors.New("OS boot identity unavailable")
	}
	return id, nil
}

// The one-minute systemd shutdown delay leaves time to persist its scheduling
// receipt. It is NOT proof of a completed power transition.
func PowerPlan(kind string) (engine.CommandLine, error) {
	flag := ""
	switch kind {
	case "host.reboot":
		flag = "--reboot"
	case "host.shutdown":
		flag = "--poweroff"
	default:
		return engine.CommandLine{}, errors.New("unknown power operation")
	}
	program, err := exec.LookPath("shutdown")
	if err != nil {
		return engine.CommandLine{}, err
	}
	return engine.CommandLine{Program: program, Args: []string{flag, "+1", "Nixmox requested power transition"}}, nil
}
func SchedulePower(ctx context.Context, kind string, e engine.Execution, runner engine.Runner) error {
	plan, err := PowerPlan(kind)
	if err != nil {
		return err
	}
	result, err := e.Runner(runner).Run(ctx, plan, e.Log(), e.Log())
	if err == nil && result.ExitCode != 0 {
		return errors.New("shutdown scheduling did not report success")
	}
	return err
}
