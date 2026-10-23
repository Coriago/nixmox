package comms

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/nats-io/jsm.go"
	"github.com/nats-io/jsm.go/api"
	"github.com/nats-io/jsm.go/backup"
	js "github.com/nats-io/nats.go/jetstream"
)

const transitionTag = "nixmox.store_transition"

type consumerImage struct {
	Config      js.ConsumerConfig `json:"config"`
	Delivered   js.SequenceInfo   `json:"delivered"`
	AckFloor    js.SequenceInfo   `json:"ack_floor"`
	AckPending  int               `json:"ack_pending"`
	Pending     uint64            `json:"pending"`
	Redelivered int               `json:"redelivered"`
}
type streamImage struct {
	Config    js.StreamConfig          `json:"config"`
	State     js.StreamState           `json:"state"`
	Consumers map[string]consumerImage `json:"consumers"`
}
type streamSnapshot struct {
	Image  streamImage `json:"image"`
	SHA256 string      `json:"sha256"`
}

func normalizeEmptyState(state js.StreamState) js.StreamState {
	if state.Msgs == 0 {
		state.FirstTime, state.LastTime = time.Time{}, time.Time{}
		// A never-written stream reports first=0 before restore and first=1
		// afterward; both have the same next sequence and no stored messages.
		if state.FirstSeq == 0 && state.LastSeq == 0 {
			state.FirstSeq = 1
		}
	}
	return state
}
func imageStream(ctx context.Context, api js.JetStream, name string) (streamImage, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var image streamImage
	stream, err := api.Stream(ctx, name)
	if err != nil {
		return image, err
	}
	info, err := stream.Info(ctx)
	if err != nil {
		return image, err
	}
	image.Config, image.State = info.Config, info.State
	// An empty restored stream retains sequence watermarks, but has no last
	// message whose timestamp NATS can reconstruct.
	image.State = normalizeEmptyState(image.State)
	delete(image.Config.Metadata, transitionTag)
	image.Consumers = map[string]consumerImage{}
	consumers := stream.ListConsumers(ctx)
	for consumer := range consumers.Info() {
		// Ordered KV watchers are disposable memory consumers, not task state.
		if consumer.Config.Durable == "" && consumer.Config.MemoryStorage {
			continue
		}
		if consumer.Config.Durable == "" || consumer.Config.MemoryStorage {
			return image, errors.New("store handoff requires file-backed durable consumers")
		}
		// Activity timestamps describe live observation, not the durable positions.
		consumer.Delivered.Last, consumer.AckFloor.Last = nil, nil
		image.Consumers[consumer.Name] = consumerImage{consumer.Config, consumer.Delivered, consumer.AckFloor, consumer.NumAckPending, consumer.NumPending, consumer.NumRedelivered}
	}
	image.State.Consumers = len(image.Consumers)
	return image, consumers.Err()
}
func readyAPI(ctx context.Context, r *Runtime) (js.JetStream, *jsm.Manager, func(), error) {
	nc, manager, close, err := r.Broker.admin(ctx, r.options.NodeID, r.clientTLS, false)
	if err != nil {
		return nil, nil, nil, err
	}
	api, err := js.New(nc)
	if err == nil {
		err = await(ctx, func() error {
			attempt, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			_, err := api.AccountInfo(attempt)
			return err
		})
	}
	if err != nil {
		close()
		return nil, nil, nil, err
	}
	return api, manager, close, nil
}
func (t *StoreTransition) snapshot(ctx context.Context, source *Runtime) error {
	api, manager, close, err := readyAPI(ctx, source)
	if err != nil {
		return err
	}
	defer close()
	if err = checkStreamSet(ctx, api); err != nil {
		return err
	}
	if t.record.To == StandaloneStore {
		info, err := source.Broker.Diagnostics()
		if err != nil {
			return err
		}
		if info.Meta == nil || info.Meta.Size != 1 || info.Meta.Leader == "" {
			return errors.New("standalone handoff requires committed removal of every other metadata peer")
		}
	}
	images := map[string]streamImage{}
	for _, name := range durableStreams() {
		image, err := imageStream(ctx, api, name)
		if err != nil {
			return err
		}
		if image.Config.Storage != js.FileStorage || image.Config.Replicas != 1 {
			return errors.New("store-mode handoff requires file storage at R1; perform planned replica changes first")
		}
		images[name] = image
	}
	t.record.Streams = map[string]streamSnapshot{}
	for _, name := range durableStreams() {
		if err = ctx.Err(); err != nil {
			return err
		}
		directory := filepath.Join(t.directory, "snapshots", name)
		// Only incomplete preparation files owned by this transition are replaced.
		// The source broker store is never moved, deleted, or used by the target.
		if err = os.RemoveAll(directory); err != nil {
			return err
		}
		stream, err := manager.LoadStream(name)
		if err != nil {
			return err
		}
		if _, err = stream.SnapshotToDirectory(ctx, directory, jsm.SnapshotConsumers(), jsm.SnapshotHealthCheck(), jsm.SnapshotWindowSize(1<<20)); err != nil {
			return err
		}
		if _, err = backup.Verify(directory); err != nil {
			return err
		}
		digest, err := snapshotDigest(directory, true)
		if err != nil {
			return err
		}
		t.record.Streams[name] = streamSnapshot{Image: images[name], SHA256: digest}
	}
	// Catch a missing coordinator fence, including consumption/acks or writes
	// between individual stream snapshots. Never cut over an inconsistent image.
	for _, name := range durableStreams() {
		image, err := imageStream(ctx, api, name)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(image, images[name]) {
			return fmt.Errorf("%s changed while snapshotted; all participants must be quiesced", name)
		}
	}
	return syncDirectory(filepath.Join(t.directory, "snapshots"))
}
func snapshotDigest(directory string, sync bool) (string, error) {
	hash := sha256.New()
	for _, name := range []string{jsm.SnapshotMetaFile, jsm.SnapshotDataFile} {
		file, err := os.Open(filepath.Join(directory, name))
		if err != nil {
			return "", err
		}
		info, err := file.Stat()
		if err != nil {
			file.Close()
			return "", err
		}
		fmt.Fprintf(hash, "%s:%d:", name, info.Size())
		_, err = io.Copy(hash, file)
		if err == nil && sync {
			err = file.Sync()
		}
		err = errors.Join(err, file.Close())
		if err != nil {
			return "", err
		}
	}
	if sync {
		if err := syncDirectory(directory); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// Restore preserves message sequences (including KV revisions), stream policy,
// object chunks and consumer delivery/ack state through NATS's own backup API.
// Unknown existing streams are never overwritten. A lost restore reply is
// reconciled using its transition marker and full snapshot image, not replayed.
func (t *StoreTransition) Restore(ctx context.Context, target *Runtime) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := t.matches(target); err != nil {
		return err
	}
	if !target.options.Maintenance || (t.record.Phase != TransitionRestoring && t.record.Phase != TransitionRestored) {
		return errors.New("target is not fenced for restoration")
	}
	apiClient, manager, close, err := readyAPI(ctx, target)
	if err != nil {
		return err
	}
	defer close()
	if err = checkStreamSet(ctx, apiClient); err != nil {
		return err
	}
	for _, name := range durableStreams() {
		snapshot, ok := t.record.Streams[name]
		if !ok {
			return errors.New("snapshot manifest is missing a durable stream")
		}
		directory := filepath.Join(t.directory, "snapshots", name)
		digest, err := snapshotDigest(directory, false)
		if err != nil {
			return err
		}
		if digest != snapshot.SHA256 {
			return errors.New("snapshot integrity mismatch")
		}
		_, err = apiClient.Stream(ctx, name)
		if errors.Is(err, js.ErrStreamNotFound) {
			var metadata struct {
				Config api.StreamConfig `json:"config"`
			}
			data, err := os.ReadFile(filepath.Join(directory, jsm.SnapshotMetaFile))
			if err != nil {
				return err
			}
			if err = json.Unmarshal(data, &metadata); err != nil {
				return err
			}
			if metadata.Config.Name != name || metadata.Config.Replicas != 1 {
				return errors.New("snapshot configuration mismatch")
			}
			if metadata.Config.Metadata == nil {
				metadata.Config.Metadata = map[string]string{}
			}
			metadata.Config.Metadata[transitionTag] = string(t.record.ID)
			if _, _, err = manager.RestoreSnapshotFromDirectory(ctx, name, directory, jsm.RestoreConfiguration(metadata.Config)); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if err = await(ctx, func() error { return t.verifyStream(ctx, apiClient, name) }); err != nil {
			return err
		}
	}
	return t.advance(TransitionRestored)
}
func (t *StoreTransition) verifyStream(ctx context.Context, api js.JetStream, name string) error {
	stream, err := api.Stream(ctx, name)
	if err != nil {
		return err
	}
	info, err := stream.Info(ctx)
	if err != nil {
		return err
	}
	if info.Config.Metadata[transitionTag] != string(t.record.ID) {
		return errors.New("target stream belongs to another store history")
	}
	image, err := imageStream(ctx, api, name)
	if err != nil {
		return err
	}
	expected := t.record.Streams[name].Image
	expected.State = normalizeEmptyState(expected.State)
	// Clustered restore creates consumer Raft assignments asynchronously.
	if len(image.Consumers) < len(expected.Consumers) {
		return fmt.Errorf("%s consumers: %w", name, errNotReady)
	}
	if !reflect.DeepEqual(image.Config, expected.Config) {
		return fmt.Errorf("%s: restored stream configuration differs", name)
	}
	if !reflect.DeepEqual(image.State, expected.State) {
		return fmt.Errorf("%s: restored stream state differs: got %+v, expected %+v", name, image.State, expected.State)
	}
	if !reflect.DeepEqual(image.Consumers, expected.Consumers) {
		for consumer, want := range expected.Consumers {
			got, exists := image.Consumers[consumer]
			if !exists {
				return fmt.Errorf("%s: restored consumer %s missing", name, consumer)
			}
			if !reflect.DeepEqual(got.Config, want.Config) {
				return fmt.Errorf("%s: restored consumer %s config differs: got %+v, expected %+v", name, consumer, got.Config, want.Config)
			}
			if !reflect.DeepEqual(got, want) {
				return fmt.Errorf("%s: restored consumer %s positions differ: got delivered=%+v ack=%+v pending=%d/%d redelivered=%d; expected delivered=%+v ack=%+v pending=%d/%d redelivered=%d", name, consumer, got.Delivered, got.AckFloor, got.AckPending, got.Pending, got.Redelivered, want.Delivered, want.AckFloor, want.AckPending, want.Pending, want.Redelivered)
			}
		}
		return fmt.Errorf("%s: unexpected restored consumers", name)
	}
	return nil
}
func checkStreamSet(ctx context.Context, api js.JetStream) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	known := map[string]bool{}
	for _, name := range durableStreams() {
		known[name] = true
	}
	streams := api.StreamNames(ctx)
	for name := range streams.Name() {
		if !known[name] {
			return fmt.Errorf("unmanaged stream %s prevents store handoff", name)
		}
	}
	return streams.Err()
}
func (t *StoreTransition) verifyTarget(ctx context.Context, target *Runtime) error {
	api, _, close, err := readyAPI(ctx, target)
	if err != nil {
		return err
	}
	defer close()
	if err = checkStreamSet(ctx, api); err != nil {
		return err
	}
	for _, name := range durableStreams() {
		if err = await(ctx, func() error { return t.verifyStream(ctx, api, name) }); err != nil {
			return err
		}
	}
	return nil
}
