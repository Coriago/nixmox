package deployment

import (
	"context"
	"fmt"
	"nixmox/internal/domain"
	"nixmox/internal/nixstore"
	"nixmox/internal/process"
	"nixmox/internal/storage"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type SystemSnapshot struct {
	Running     string              `json:"running"`
	Profile     string              `json:"profile"`
	Generation  int                 `json:"generation"`
	Generations []domain.Generation `json:"generations"`
	ObservedAt  time.Time           `json:"observed_at"`
}
type TargetSystem interface {
	Inspect(context.Context) (SystemSnapshot, error)
	Select(context.Context, string) (SystemSnapshot, error)
	Start(context.Context, string, string, string) error
	Activation(context.Context, string) (string, error)
	Ready(context.Context) bool
}

// LocalSystem never evaluates a flake, partitions disks, or resolves a selector.
// Its transient activation unit survives replacement of the management daemon.
type LocalSystem struct {
	ManagementReady func() bool
	StateRoot       string
}

func (l LocalSystem) Inspect(ctx context.Context) (SystemSnapshot, error) {
	s := SystemSnapshot{ObservedAt: time.Now().UTC(), Generations: []domain.Generation{}}
	if ctx.Err() != nil {
		return s, ctx.Err()
	}
	running, err := filepath.EvalSymlinks("/run/current-system")
	if err != nil || !nixstore.PathPattern.MatchString(running) {
		return s, domain.Err("APPLY_UNAVAILABLE", "Target has no running NixOS system.", 503)
	}
	s.Running = running
	profile := "/nix/var/nix/profiles/system"
	s.Profile, err = filepath.EvalSymlinks(profile)
	if err != nil || !nixstore.PathPattern.MatchString(s.Profile) {
		return s, domain.Err("OBSERVATION_UNAVAILABLE", "System profile is unavailable.", 503)
	}
	link, err := os.Readlink(profile)
	if err != nil {
		return s, err
	}
	_, _ = fmt.Sscanf(filepath.Base(link), "system-%d-link", &s.Generation)
	if s.Generation < 1 {
		return s, domain.Err("OBSERVATION_UNAVAILABLE", "System profile generation is unavailable.", 503)
	}
	entries, err := os.ReadDir(filepath.Dir(profile))
	if err != nil {
		return s, err
	}
	if len(entries) > 10000 {
		return s, domain.Err("STORAGE_LIMIT", "System profile catalog exceeds its bound.", 507)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "system-") || !strings.HasSuffix(name, "-link") {
			continue
		}
		number, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(name, "system-"), "-link"))
		if err != nil || number < 1 {
			continue
		}
		path, err := filepath.EvalSymlinks(filepath.Join(filepath.Dir(profile), name))
		if err != nil || !nixstore.PathPattern.MatchString(path) {
			continue
		}
		_, statErr := os.Stat(path)
		s.Generations = append(s.Generations, domain.Generation{Number: number, OutputPath: path, Available: statErr == nil, Running: path == s.Running, ObservedAt: s.ObservedAt})
	}
	return s, nil
}
func (l LocalSystem) Select(ctx context.Context, output string) (SystemSnapshot, error) {
	if !nixstore.PathPattern.MatchString(output) {
		return SystemSnapshot{}, domain.Err("OUTPUT_INVALID", "Invalid system output.", 422)
	}
	s, err := l.Inspect(ctx)
	if err != nil {
		return s, err
	}
	if s.Profile == output {
		return s, nil
	}
	_, err = process.Run(ctx, process.Options{Timeout: 30 * time.Second, MaxOutput: 64 << 10}, "nix-env", "--profile", "/nix/var/nix/profiles/system", "--set", output)
	if err != nil {
		return s, domain.Err("PROFILE_UNKNOWN", "System profile selection requires reconciliation.", 503)
	}
	s, err = l.Inspect(ctx)
	if err != nil {
		return s, err
	}
	if s.Profile != output {
		return s, domain.Err("PROFILE_UNKNOWN", "System profile does not select the intended output.", 503)
	}
	return s, nil
}
func activationUnit(task string) (string, error) {
	if !domain.SafeID.MatchString(task) {
		return "", domain.Err("INVALID_ID", "Invalid activation task.", 422)
	}
	return "nixmox-activation-" + task + ".service", nil
}
func (l LocalSystem) Start(ctx context.Context, task, output, mode string) error {
	unit, err := activationUnit(task)
	if err != nil {
		return err
	}
	if !nixstore.PathPattern.MatchString(output) || (mode != "switch" && mode != "boot") {
		return domain.Err("OUTPUT_INVALID", "Invalid activation parameters.", 422)
	}
	if os.Geteuid() != 0 || !filepath.IsAbs(l.StateRoot) {
		return domain.Err("APPLY_UNAVAILABLE", "Activation requires root and durable receipt storage.", 503)
	}
	state, err := l.Activation(ctx, task)
	if err != nil {
		return err
	}
	if state != "not_started" {
		return domain.Err("ACTIVATION_UNKNOWN", "Existing activation must be reconciled before dispatch.", 409)
	}
	dir := filepath.Join(l.StateRoot, "activation", task)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	store := &storage.Store{Root: dir}
	var old activationReceipt
	if err = store.Read(task+".json", &old); err == nil {
		if old.TaskID != task || old.Output != output || old.Mode != mode || old.State != "planned" {
			return domain.Err("ACTIVATION_UNKNOWN", "An existing activation receipt must be reconciled.", 409)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	receipt := activationReceipt{TaskID: task, Output: output, Mode: mode, State: "planned", UpdatedAt: time.Now().UTC()}
	if err = store.Write(task+".json", receipt); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	_, err = process.Run(ctx, process.Options{Timeout: 15 * time.Second, MaxOutput: 64 << 10}, "systemd-run", "--unit="+unit, "--no-block", "--property=Type=oneshot", "--property=RemainAfterExit=yes", "--property=TimeoutStartSec=930", "--property=StandardOutput=null", "--property=StandardError=null", executable, "activation-receipt", dir, task)
	if err != nil {
		return domain.Err("ACTIVATION_UNKNOWN", "Activation dispatch requires receipt reconciliation.", 503)
	}
	return nil
}
func (l LocalSystem) Activation(ctx context.Context, task string) (string, error) {
	unit, err := activationUnit(task)
	if err != nil {
		return "unknown", err
	}
	neverStarted := false
	if filepath.IsAbs(l.StateRoot) {
		var receipt activationReceipt
		err := (&storage.Store{Root: filepath.Join(l.StateRoot, "activation", task)}).Read(task+".json", &receipt)
		if err == nil && receipt.TaskID == task {
			if receipt.State == "succeeded" || receipt.State == "failed" {
				return receipt.State, nil
			}
			neverStarted = receipt.State == "planned"
		} else if os.IsNotExist(err) {
			neverStarted = true
		} else {
			return "unknown", domain.Err("OBSERVATION_UNAVAILABLE", "Durable activation receipt is unreadable.", 503)
		}
	}
	r, err := process.Run(ctx, process.Options{Timeout: 5 * time.Second, MaxOutput: 64 << 10}, "systemctl", "show", unit, "--property=LoadState,ActiveState,SubState,Result,ExecMainStatus")
	if err != nil {
		return "unknown", domain.Err("OBSERVATION_UNAVAILABLE", "Activation unit state is unavailable.", 503)
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(r.Stdout), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			values[k] = v
		}
	}
	if values["LoadState"] == "not-found" {
		if neverStarted {
			return "not_started", nil
		}
		return "unknown", nil
	}
	if values["ActiveState"] == "active" && values["SubState"] == "exited" && values["Result"] == "success" && values["ExecMainStatus"] == "0" {
		return "succeeded", nil
	}
	if values["ActiveState"] == "failed" {
		return "failed", nil
	}
	if values["ActiveState"] == "activating" {
		return "running", nil
	}
	return "unknown", nil
}
func (l LocalSystem) Ready(ctx context.Context) bool {
	return ctx.Err() == nil && l.ManagementReady != nil && l.ManagementReady()
}
