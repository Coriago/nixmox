package credentials

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"nixmox/internal/config"
	"nixmox/internal/domain"
	"nixmox/internal/gitstore"
	"nixmox/internal/state"
	"nixmox/internal/storage"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Vault struct {
	Root          string
	State         state.Store
	Declared      map[string]config.Credential
	Identity      func() domain.Identity
	Eligible      func(context.Context, string) bool
	GitCheck      func(context.Context, domain.Credential, string) error
	Receipt       func(string) (domain.Receipt, error)
	Submit        func(domain.Command) (domain.Receipt, error)
	ForwardImport func(context.Context, domain.SecretImport, io.Reader) (domain.Receipt, error)
	mu            sync.Mutex
}
type stageRecord struct {
	Request   domain.SecretImport `json:"request"`
	Digest    string              `json:"digest"`
	CreatedAt time.Time           `json:"created_at"`
	Claimed   bool                `json:"claimed"`
}
type localRecord struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	CommandID string `json:"command_id"`
}

func New(root string, st state.Store, declared map[string]config.Credential, identity func() domain.Identity, eligible func(context.Context, string) bool) (*Vault, error) {
	for _, dir := range []string{"", "staging", "managed"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			return nil, err
		}
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, domain.Err("CREDENTIAL_UNAVAILABLE", "Credential directory is not private.", 503)
	}
	return &Vault{Root: root, State: st, Declared: declared, Identity: identity, Eligible: eligible}, nil
}
func ID(name string) string  { return gitstore.StableID("cred_", name) }
func missing(err error) bool { var e *domain.Error; return errors.As(err, &e) && e.Code == "NOT_FOUND" }
func (v *Vault) own(ctx context.Context, node string) error {
	if node != v.Identity().NodeID || v.Eligible == nil || !v.Eligible(ctx, node) {
		return domain.Err("MEMBERSHIP_INELIGIBLE", "Credential storage and use require an available full member.", 403)
	}
	return nil
}

// Stage receives a bounded secret stream. The command assembled here contains
// only its opaque reference; neither the bytes nor their digest leave the vault.
func (v *Vault) Stage(ctx context.Context, request domain.SecretImport, reader io.Reader) (domain.Command, error) {
	var command domain.Command
	if !domain.SafeID.MatchString(request.ID) || !domain.SafeID.MatchString(request.Name) || !validKind(request.Kind) {
		return command, domain.Err("INVALID_PAYLOAD", "Invalid credential import metadata.", 422)
	}
	if err := v.own(ctx, request.StorageNodeID); err != nil {
		return command, err
	}
	if _, declared := v.Declared[request.Name]; declared {
		return command, domain.Err("DECLARED_RESOURCE", "A runtime-file credential cannot be overwritten by import.", 409)
	}
	bytes, err := io.ReadAll(io.LimitReader(reader, MaxSecretBytes+1))
	if err != nil {
		return command, unavailable()
	}
	defer func() {
		for i := range bytes {
			bytes[i] = 0
		}
	}()
	if _, err = Validate(request.Kind, bytes); err != nil {
		return command, err
	}
	sum := sha256.Sum256(bytes)
	digest := hex.EncodeToString(sum[:])
	ref := gitstore.StableID("stage_", request.ID)
	v.mu.Lock()
	defer v.mu.Unlock()
	store := &storage.Store{Root: v.Root}
	var old stageRecord
	err = store.Read("staging/"+ref+".json", &old)
	if err == nil {
		if old.Request != request || old.Digest != digest {
			return command, domain.Err("COMMAND_CONFLICT", "Import ID was already used for another value or destination.", 409)
		}
	} else if !os.IsNotExist(err) {
		return command, unavailable()
	} else {
		if err = v.cleanupStages(ctx); err != nil {
			return command, err
		}
		if err = store.WriteBytes("staging/"+ref+".secret", bytes); err != nil {
			return command, unavailable()
		}
		if err = store.Write("staging/"+ref+".json", stageRecord{Request: request, Digest: digest, CreatedAt: time.Now().UTC()}); err != nil {
			return command, unavailable()
		}
	}
	payload, _ := json.Marshal(map[string]string{"name": request.Name, "kind": request.Kind, "storage_node_id": request.StorageNodeID, "staging_ref": ref})
	command = domain.Command{ID: request.ID, Type: "credential.import", TargetID: request.StorageNodeID, Payload: payload}
	return command, nil
}
func (v *Vault) cleanupStages(ctx context.Context) error {
	entries, err := os.ReadDir(filepath.Join(v.Root, "staging"))
	if err != nil {
		return unavailable()
	}
	if len(entries) > 2*domain.MaxRecords {
		return domain.Err("STORAGE_LIMIT", "Credential import receipt retention bound reached.", 507)
	}
	count := 0
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var stage stageRecord
		if (&storage.Store{Root: v.Root}).Read("staging/"+entry.Name(), &stage) != nil {
			return unavailable()
		}
		if stage.Claimed {
			continue
		}
		count++
		if time.Since(stage.CreatedAt) > 10*time.Minute && v.Receipt != nil {
			_, receiptErr := v.Receipt(stage.Request.ID)
			if missing(receiptErr) {
				ref := strings.TrimSuffix(entry.Name(), ".json")
				_ = os.Remove(filepath.Join(v.Root, "staging", ref+".secret"))
				_ = os.Remove(filepath.Join(v.Root, "staging", ref+".json"))
				count--
			}
		}
	}
	if count >= 64 {
		return domain.Err("QUEUE_FULL", "Credential staging capacity reached.", 503)
	}
	return nil
}
func (v *Vault) Import(ctx context.Context, request domain.SecretImport, value io.Reader) (domain.Receipt, error) {
	if request.StorageNodeID != v.Identity().NodeID {
		if v.ForwardImport == nil {
			return domain.Receipt{}, domain.Err("CREDENTIAL_UNAVAILABLE", "Secret forwarding to the selected full member is unavailable.", 503)
		}
		return v.ForwardImport(ctx, request, value)
	}
	if v.Submit == nil {
		return domain.Receipt{}, unavailable()
	}
	command, err := v.Stage(ctx, request, value)
	if err != nil {
		return domain.Receipt{}, err
	}
	return v.Submit(command)
}
func readPrivate(path string) ([]byte, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(real) || strings.HasPrefix(real, "/nix/store/") {
		return nil, unavailable()
	}
	f, err := os.Open(real)
	if err != nil {
		return nil, unavailable()
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > MaxSecretBytes {
		return nil, unavailable()
	}
	value, err := io.ReadAll(io.LimitReader(f, MaxSecretBytes+1))
	if err != nil || len(value) > MaxSecretBytes {
		return nil, unavailable()
	}
	return value, nil
}
func (v *Vault) Secret(ctx context.Context, ref string) (string, []byte, error) {
	if err := v.own(ctx, v.Identity().NodeID); err != nil {
		return "", nil, err
	}
	for name, c := range v.Declared {
		if name == ref || ID(name) == ref {
			value, err := readPrivate(c.SecretFile)
			return c.Kind, value, err
		}
	}
	if !domain.SafeID.MatchString(ref) {
		return "", nil, unavailable()
	}
	id := ref
	if !strings.HasPrefix(ref, "cred_") {
		id = ID(ref)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	var local localRecord
	if err := (&storage.Store{Root: v.Root}).Read("managed/"+id+".json", &local); err != nil {
		return "", nil, unavailable()
	}
	value, err := readPrivate(filepath.Join(v.Root, "managed", id+".secret"))
	return local.Kind, value, err
}
func (v *Vault) Credential(ctx context.Context, ref string) (domain.Credential, error) {
	id := ref
	if !strings.HasPrefix(ref, "cred_") {
		id = ID(ref)
	}
	var c domain.Credential
	_, err := v.State.Get(ctx, "credential."+id, &c)
	if err != nil {
		return c, err
	}
	return c, nil
}
func (v *Vault) Credentials(ctx context.Context, limit, cursor string) (domain.Page[domain.Credential], error) {
	entries, err := v.State.List(ctx, "credential.")
	if err != nil {
		return domain.Page[domain.Credential]{}, err
	}
	rows := []domain.Credential{}
	for _, entry := range entries {
		var c domain.Credential
		if domain.StrictJSON(entry.Data, &c) != nil {
			return domain.Page[domain.Credential]{}, unavailable()
		}
		if c.State != "removed" {
			rows = append(rows, c)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return domain.Paginate(rows, "credentials", limit, cursor, func(c domain.Credential) string { return c.ID })
}
