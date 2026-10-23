package comms

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"nixmox/internal/domain"
)

const fenceFile = ".nixmox-transition.json"

type StoreMode string

const (
	StandaloneStore StoreMode = "standalone"
	RoutedStore     StoreMode = "routed"
)

type TransitionPhase string

const (
	TransitionPreparing TransitionPhase = "preparing"
	TransitionPrepared  TransitionPhase = "prepared"
	TransitionRestoring TransitionPhase = "restoring"
	TransitionRestored  TransitionPhase = "restored"
	TransitionCommitted TransitionPhase = "committed"
)

type storeFence struct {
	ID      domain.ID `json:"id"`
	Node    domain.ID `json:"node"`
	Cluster domain.ID `json:"cluster"`
}
type TransitionStatus struct {
	ID              domain.ID       `json:"id"`
	Node            domain.ID       `json:"node"`
	Cluster         domain.ID       `json:"cluster"`
	From            StoreMode       `json:"from"`
	To              StoreMode       `json:"to"`
	SourceDirectory string          `json:"source_directory"`
	TargetDirectory string          `json:"target_directory"`
	Phase           TransitionPhase `json:"phase"`
}
type transitionRecord struct {
	TransitionStatus
	Streams map[string]streamSnapshot `json:"streams"`
}

// StoreTransition is one checkpointed store handoff, not membership policy.
// The coordinator must serialize topology operations and fence ALL participating
// gateways/executors before preparing a snapshot. Quiesce fences each broker's
// ordinary clients/leaves while keeping Core handshakes and Raft connected.
// Keep source storage and backups after commit; this engine never deletes them.
type StoreTransition struct {
	mu        sync.Mutex
	directory string
	record    transitionRecord
}

func mode(options BrokerOptions) StoreMode {
	if options.RouteAddress != "" {
		return RoutedStore
	}
	return StandaloneStore
}
func (t *StoreTransition) Status() TransitionStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.record.TransitionStatus
}
func (t *StoreTransition) save() error {
	return atomicJSON(filepath.Join(t.directory, "transition.json"), t.record)
}
func (t *StoreTransition) advance(phase TransitionPhase) error {
	previous := t.record.Phase
	t.record.Phase = phase
	if err := t.save(); err != nil {
		t.record.Phase = previous
		return err
	}
	return nil
}
func (t *StoreTransition) fence() storeFence {
	return storeFence{t.record.ID, t.record.Node, t.record.Cluster}
}

// A mode stamp prevents accidentally opening standalone Raft metadata as a
// cluster (or vice versa). Legacy standalone data is adopted without moving it;
// an unstamped nonempty routed store needs explicit operator reconciliation.
func ensureStoreMode(options BrokerOptions) error {
	path := filepath.Join(options.DataDir, ".nixmox-store-mode.json")
	data, err := os.ReadFile(path)
	if err == nil {
		var stored StoreMode
		if json.Unmarshal(data, &stored) != nil || stored != mode(options) {
			return errors.New("broker store mode mismatch; use a prepared store transition")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if mode(options) == RoutedStore {
		entries, err := os.ReadDir(options.DataDir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for _, entry := range entries {
			if entry.Name() != fenceFile {
				return errors.New("cannot reuse an unstamped store for routed JetStream")
			}
		}
	}
	return atomicJSON(path, mode(options))
}

func storeFenced(directory string, node, cluster domain.ID) (bool, error) {
	var fence storeFence
	data, err := os.ReadFile(filepath.Join(directory, fenceFile))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err = json.Unmarshal(data, &fence); err != nil {
		return false, err
	}
	if fence.ID == "" || fence.Node != node || fence.Cluster != cluster {
		return false, errors.New("store transition fence identity mismatch")
	}
	return true, nil
}
func writeFence(directory string, fence storeFence) error {
	path := filepath.Join(directory, fenceFile)
	data, err := os.ReadFile(path)
	if err == nil {
		var old storeFence
		if json.Unmarshal(data, &old) != nil || old != fence {
			return errors.New("store belongs to another transition")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return atomicJSON(path, fence)
}

// Quiesce commits its fence BEFORE replacing the transport generation. A crash
// cannot reopen the source for ordinary writes. Retain the returned runtime even
// when later snapshot steps fail; unfreezing on an error could invalidate backups.
func (r *Runtime) Quiesce(ctx context.Context, id domain.ID) (*Runtime, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if id == "" || r.options.Pending {
		return nil, errors.New("store quiescence requires a durable broker and transition ID")
	}
	if err := writeFence(r.options.DataDir, storeFence{id, r.options.NodeID, r.options.ClusterID}); err != nil {
		r.Close() // A rename followed by a failed fsync has an uncertain outcome.
		return nil, err
	}
	if r.options.Maintenance {
		return r, nil
	}
	options := r.options
	options.Maintenance = true
	r.Close()
	return StartRuntime(r.parent, options, r.clientTLS)
}

// PrepareStoreTransition snapshots all application streams, including consumers.
// Use a unique directory outside the source store for each command ID. Repeating
// an interrupted preparation is safe only while the same source remains fenced.
func PrepareStoreTransition(ctx context.Context, source *Runtime, directory string, id domain.ID, to StoreMode) (*StoreTransition, error) {
	if id == "" || !source.options.Maintenance || source.options.Pending || (to != StandaloneStore && to != RoutedStore) || mode(source.options) == to {
		return nil, errors.New("a fenced broker and a different valid store mode are required")
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	sourceDir, err := filepath.Abs(source.options.DataDir)
	if err != nil {
		return nil, err
	}
	relative, err := filepath.Rel(sourceDir, directory)
	if err != nil || filepath.IsLocal(relative) {
		return nil, errors.New("transition directory must be outside the source store")
	}
	expected := TransitionStatus{ID: id, Node: source.options.NodeID, Cluster: source.options.ClusterID, From: mode(source.options), To: to, SourceDirectory: sourceDir, TargetDirectory: filepath.Join(directory, "store"), Phase: TransitionPreparing}
	t, err := OpenStoreTransition(directory)
	if errors.Is(err, os.ErrNotExist) {
		t = &StoreTransition{directory: directory, record: transitionRecord{TransitionStatus: expected, Streams: map[string]streamSnapshot{}}}
		if err = t.save(); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	got := t.record.TransitionStatus
	got.Phase = expected.Phase
	if got != expected {
		return nil, errors.New("transition ID/directory is bound to another handoff")
	}
	if err = writeFence(sourceDir, t.fence()); err != nil {
		return nil, err
	}
	if t.record.Phase != TransitionPreparing {
		return t, nil
	}
	if err = t.snapshot(ctx, source); err != nil {
		return nil, err
	}
	return t, t.advance(TransitionPrepared)
}
func OpenStoreTransition(directory string) (*StoreTransition, error) {
	directory, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(directory, "transition.json"))
	if err != nil {
		return nil, err
	}
	t := &StoreTransition{directory: directory}
	if err = json.Unmarshal(data, &t.record); err != nil {
		return nil, err
	}
	r := t.record
	if r.ID == "" || r.Node == "" || r.Cluster == "" || r.TargetDirectory != filepath.Join(directory, "store") || !filepath.IsAbs(r.SourceDirectory) || r.SourceDirectory == r.TargetDirectory || r.From == r.To || (r.From != StandaloneStore && r.From != RoutedStore) || (r.To != StandaloneStore && r.To != RoutedStore) {
		return nil, errors.New("invalid store transition checkpoint")
	}
	switch r.Phase {
	case TransitionPreparing, TransitionPrepared, TransitionRestoring, TransitionRestored, TransitionCommitted:
	default:
		return nil, errors.New("unknown store transition phase")
	}
	if r.Phase != TransitionPreparing && len(r.Streams) != len(durableStreams()) {
		return nil, errors.New("incomplete snapshot manifest")
	}
	return t, nil
}

// StartTarget intentionally does not await JetStream readiness. The coordinator
// must already have staged the approved topology on every participant, allowing
// all routes to start before anyone waits for a metadata election. It must close
// the source generation first. Recovery uses this same checkpoint and directory.
func (t *StoreTransition) StartTarget(ctx context.Context, options BrokerOptions, certificate *tls.Config) (*Runtime, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.record.Phase == TransitionPreparing || t.record.Phase == TransitionCommitted {
		return nil, errors.New("transition is not awaiting target startup")
	}
	if options.NodeID != t.record.Node || options.ClusterID != t.record.Cluster || mode(options) != t.record.To || options.Pending {
		return nil, errors.New("target broker does not match the prepared handoff")
	}
	if err := validateRoutes(options); err != nil {
		return nil, err
	}
	if t.record.Phase == TransitionPrepared {
		entries, err := os.ReadDir(t.record.TargetDirectory)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		for _, entry := range entries {
			if entry.Name() != fenceFile {
				return nil, errors.New("target store is not fresh")
			}
		}
		if err = writeFence(t.record.TargetDirectory, t.fence()); err != nil {
			return nil, err
		}
		if err = t.advance(TransitionRestoring); err != nil {
			return nil, err
		}
	}
	options.DataDir, options.Maintenance = t.record.TargetDirectory, true
	return StartRuntime(ctx, options, certificate)
}
func (t *StoreTransition) matches(target *Runtime) error {
	path, err := filepath.Abs(target.options.DataDir)
	if err != nil {
		return err
	}
	if path != t.record.TargetDirectory || target.options.NodeID != t.record.Node || target.options.ClusterID != t.record.Cluster || mode(target.options) != t.record.To {
		return errors.New("runtime does not belong to this store transition")
	}
	return nil
}

// Activate records the verified destination before releasing its write fence.
// Restart recovery consults Status().TargetDirectory, never falls back to source.
// The returned generation exposes Core; attach storage and rearm consumers only
// after ConnectStorage(ctx, StorageLimits{Existing:true,...}) succeeds.
func (t *StoreTransition) Activate(ctx context.Context, target *Runtime) (*Runtime, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := t.matches(target); err != nil {
		return nil, err
	}
	if t.record.Phase != TransitionRestored && t.record.Phase != TransitionCommitted {
		return nil, errors.New("restoration is not verified")
	}
	if t.record.Phase == TransitionRestored {
		if err := t.verifyTarget(ctx, target); err != nil {
			return nil, err
		}
		if err := t.advance(TransitionCommitted); err != nil {
			return nil, err
		}
	}
	path := filepath.Join(t.record.TargetDirectory, fenceFile)
	if data, err := os.ReadFile(path); err == nil {
		var fence storeFence
		if json.Unmarshal(data, &fence) != nil || fence != t.fence() {
			return nil, errors.New("target fence changed")
		}
		if err = os.Remove(path); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := syncDirectory(t.record.TargetDirectory); err != nil {
		return nil, err
	}
	if !target.options.Maintenance {
		return target, nil
	}
	options := target.options
	options.Maintenance = false
	target.Close()
	return StartRuntime(target.parent, options, target.clientTLS)
}

func atomicJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	_, statErr := os.Stat(directory)
	if err = os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	if errors.Is(statErr, os.ErrNotExist) {
		// Persist newly created ancestor links, not just the checkpoint file.
		for path := directory; ; path = filepath.Dir(path) {
			if err = syncDirectory(path); err != nil {
				return err
			}
			if filepath.Dir(path) == path {
				break
			}
		}
	}
	file, err := os.CreateTemp(directory, ".checkpoint-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	return syncDirectory(directory)
}
func syncDirectory(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}
	return errors.Join(file.Sync(), file.Close())
}
