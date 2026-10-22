package deployment

import (
	"context"
	"nixmox/internal/domain"
	"nixmox/internal/nixstore"
	"nixmox/internal/process"
	"nixmox/internal/storage"
	"os"
	"path/filepath"
	"time"
)

type activationReceipt struct {
	TaskID    string    `json:"task_id"`
	Output    string    `json:"output"`
	Mode      string    `json:"mode"`
	State     string    `json:"state"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RunActivationReceipt is a private, root-only subprocess entry point. The
// persisted plan is created by the approved task, never by command payloads.
// It runs in its own systemd unit so changing nixmox.service cannot kill its
// receipt writer along with the console daemon.
func RunActivationReceipt(ctx context.Context, root, task string) error {
	if os.Geteuid() != 0 || !filepath.IsAbs(root) || !domain.SafeID.MatchString(task) {
		return domain.Err("FORBIDDEN", "Activation receipt requires its private root-owned plan.", 403)
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return domain.Err("FORBIDDEN", "Activation receipt directory is not private.", 403)
	}
	store, err := storage.Open(root)
	if err != nil {
		return err
	}
	defer store.Close()
	var receipt activationReceipt
	if err = store.Read(task+".json", &receipt); err != nil {
		return err
	}
	if receipt.TaskID != task || receipt.State != "planned" || !nixstore.PathPattern.MatchString(receipt.Output) || (receipt.Mode != "switch" && receipt.Mode != "boot") {
		return domain.Err("INVALID_STATE", "Activation plan is not eligible for first execution.", 409)
	}
	receipt.State = "running"
	receipt.UpdatedAt = time.Now().UTC()
	if err = store.Write(task+".json", receipt); err != nil {
		return err
	}
	_, activationErr := process.Run(ctx, process.Options{Timeout: 15 * time.Minute, MaxOutput: 4 << 20, StderrLine: func([]byte) {}}, filepath.Join(receipt.Output, "bin/switch-to-configuration"), receipt.Mode)
	receipt.State = "succeeded"
	if activationErr != nil {
		receipt.State = "failed"
	}
	receipt.UpdatedAt = time.Now().UTC()
	if err = store.Write(task+".json", receipt); err != nil {
		return err
	}
	return activationErr
}
