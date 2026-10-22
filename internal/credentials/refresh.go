package credentials

import (
	"context"
	"nixmox/internal/domain"
	"nixmox/internal/storage"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (v *Vault) Start() func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			round, stop := context.WithTimeout(ctx, 10*time.Second)
			_ = v.Reconcile(round)
			_ = v.reconcileManaged(round)
			stop()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func() { cancel(); <-done }
}
func (v *Vault) reconcileManaged(ctx context.Context) error {
	if err := v.own(ctx, v.Identity().NodeID); err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.cleanupStages(ctx); err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(v.Root, "managed"))
	if err != nil {
		return unavailable()
	}
	if len(entries) > 2*domain.MaxRecords {
		return domain.Err("STORAGE_LIMIT", "Credential replica bound exceeded.", 507)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var local localRecord
		if (&storage.Store{Root: v.Root}).Read("managed/"+entry.Name(), &local) != nil {
			return unavailable()
		}
		value, err := readPrivate(filepath.Join(v.Root, "managed", strings.TrimSuffix(entry.Name(), ".json")+".secret"))
		available := false
		var public *string
		if err == nil {
			check, err := Validate(local.Kind, value)
			if err == nil {
				available = true
				public = check.PublicKey
			}
			for i := range value {
				value[i] = 0
			}
		}
		if _, err = v.publishReplica(ctx, local.Name, local.Kind, "managed", available, public, false); err != nil {
			return err
		}
	}
	return nil
}
