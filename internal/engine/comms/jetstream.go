package comms

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	js "github.com/nats-io/nats.go/jetstream"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/internal/shared"
)

const commandStream = "nx_commands"

const resultStream = "nx_results"

var (
	_ engine.StateStore    = (*JetStream)(nil)
	_ engine.ArtifactStore = (*JetStream)(nil)
)

type JetStream struct {
	API           js.JetStream
	KV            js.KeyValue
	Objects       js.ObjectStore
	Cluster, Node domain.ID
}

type StorageLimits struct {
	Commands, Results, State, Git int64
	Replicas                      int
}

func OpenJetStream(ctx context.Context, nc *nats.Conn, cluster, node domain.ID, limits StorageLimits) (*JetStream, error) {
	if cluster == "" || node == "" || (limits.Replicas != 1 && limits.Replicas != 3) || limits.Commands <= 0 || limits.Results <= 0 || limits.State <= 0 || limits.Git <= 0 {
		return nil, errors.New("positive storage limits and 1 or 3 replicas required")
	}
	api, err := js.New(nc)
	if err != nil {
		return nil, err
	}
	j := &JetStream{API: api, Cluster: cluster, Node: node}
	prefix := "nx." + shared.Token(cluster) + "."
	for _, cfg := range []js.StreamConfig{
		{Name: commandStream, Subjects: []string{prefix + "command.*"}, Storage: js.FileStorage, Retention: js.WorkQueuePolicy, Discard: js.DiscardNew, MaxBytes: limits.Commands, MaxMsgSize: 512 << 10, Replicas: limits.Replicas, Duplicates: domain.CommandTTL},
		{Name: resultStream, Subjects: []string{prefix + "result.*"}, Storage: js.FileStorage, Discard: js.DiscardNew, MaxBytes: limits.Results, MaxMsgSize: 512 << 10, Replicas: limits.Replicas},
	} {
		// CreateStream is idempotent only for matching configuration: restart cannot
		// silently lower replicas or change retention after a topology change.
		if _, err = api.CreateStream(ctx, cfg); err != nil {
			return nil, err
		}
	}
	j.KV, err = api.CreateKeyValue(ctx, js.KeyValueConfig{Bucket: domain.StateBucket, Storage: js.FileStorage, Replicas: limits.Replicas, MaxBytes: limits.State, MaxValueSize: 512 << 10, History: 1})
	if err != nil {
		return nil, err
	}
	j.Objects, err = api.CreateObjectStore(ctx, js.ObjectStoreConfig{Bucket: domain.GitBucket, Storage: js.FileStorage, Replicas: limits.Replicas, MaxBytes: limits.Git})
	if err != nil {
		return nil, err
	}
	// Object storage must not evict old chunks at capacity. KV maintains one
	// authoritative revision per key; its own per-subject retention is intentional.
	stream, err := api.Stream(ctx, "OBJ_"+domain.GitBucket)
	if err != nil {
		return nil, err
	}
	info, err := stream.Info(ctx)
	if err != nil {
		return nil, err
	}
	if info.Config.Discard != js.DiscardNew {
		cfg := info.Config
		cfg.Discard = js.DiscardNew
		if _, err = api.UpdateStream(ctx, cfg); err != nil {
			return nil, err
		}
	}
	return j, nil
}

func (j *JetStream) Submit(ctx context.Context, e domain.Envelope) error {
	if e.Scope != domain.ClusterScope || e.ClusterID == nil || *e.ClusterID != j.Cluster || e.Command.ID == "" || e.OwnerNodeID == "" || len(e.Command.Payload) > domain.CommandPayloadLimit || e.ExpiresAt.IsZero() {
		return errors.New("invalid command envelope")
	}
	if time.Now().After(e.ExpiresAt) {
		return &domain.Error{Code: "COMMAND_EXPIRED", Message: "Command expired before submission"}
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = j.API.Publish(ctx, Subject(j.Cluster, "command", e.OwnerNodeID), data, js.WithMsgID(string(e.Command.ID)))
	if err != nil {
		return &domain.Error{Code: "SUBMISSION_UNKNOWN", Message: "No durable publish acknowledgement; query the receipt before retrying", Retryable: true}
	}
	return nil
}

func (j *JetStream) Commands(ctx context.Context, node domain.ID) (engine.Subscription[engine.Delivery], error) {
	c, err := j.API.CreateConsumer(ctx, commandStream, js.ConsumerConfig{Durable: "exec_" + shared.Token(node), FilterSubject: Subject(j.Cluster, "command", node), AckPolicy: js.AckExplicitPolicy, MaxAckPending: 32, MaxWaiting: 8, BackOff: []time.Duration{5 * time.Second, 30 * time.Second, time.Minute}})
	if err != nil {
		return nil, err
	}
	life, cancel := context.WithCancel(ctx)
	return &commandSubscription{consumer: c, ctx: life, cancel: cancel, cluster: j.Cluster, node: node}, nil
}

type commandSubscription struct {
	consumer      js.Consumer
	ctx           context.Context
	cancel        context.CancelFunc
	cluster, node domain.ID
}

func (s *commandSubscription) Close() error { s.cancel(); return nil }

func (s *commandSubscription) Next(ctx context.Context) (engine.Delivery, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	msg, err := s.consumer.Next(js.FetchContext(ctx))
	if err != nil {
		return nil, err
	}
	var e domain.Envelope
	if err = json.Unmarshal(msg.Data(), &e); err != nil {
		return nil, err
	}
	if e.ClusterID == nil || *e.ClusterID != s.cluster || e.OwnerNodeID != s.node || e.Scope != domain.ClusterScope {
		return nil, errors.New("command subject/owner mismatch")
	}
	// Expiry is evaluated by admission after consulting the durable receipt. An
	// expired redelivery of accepted work must still reconcile/ack that receipt.
	return &commandDelivery{msg: msg, envelope: e}, nil
}

type commandDelivery struct {
	msg      js.Msg
	envelope domain.Envelope
}

func (d *commandDelivery) Envelope() domain.Envelope { return d.envelope }

func (d *commandDelivery) Ack(ctx context.Context) error { return d.msg.DoubleAck(ctx) }

func (d *commandDelivery) Retry(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return d.msg.NakWithDelay(time.Second)
}

func (j *JetStream) Result(ctx context.Context, t domain.Task) error {
	if t.OwnerNodeID != j.Node {
		return errors.New("result owner mismatch")
	}
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = j.API.Publish(ctx, Subject(j.Cluster, "result", j.Node), data, js.WithMsgID(fmt.Sprintf("%s:%d", t.ID, t.Version)))
	return err
}

func (j *JetStream) Get(ctx context.Context, key string) (engine.StateEntry, error) {
	e, err := j.KV.Get(ctx, key)
	if err != nil {
		return engine.StateEntry{}, err
	}
	return stateEntry(e), nil
}

func (j *JetStream) CompareAndSwap(ctx context.Context, key string, rev engine.Revision, value json.RawMessage) (engine.Revision, error) {
	var n uint64
	var err error
	if rev == 0 {
		n, err = j.KV.Create(ctx, key, value)
	} else {
		n, err = j.KV.Update(ctx, key, value, uint64(rev))
	}
	return engine.Revision(n), err
}

func stateEntry(e js.KeyValueEntry) engine.StateEntry {
	if e == nil {
		return engine.StateEntry{SnapshotComplete: true}
	}
	return engine.StateEntry{Key: e.Key(), Revision: engine.Revision(e.Revision()), Value: e.Value(), Deleted: e.Operation() != js.KeyValuePut}
}

func (j *JetStream) Watch(ctx context.Context, key string) (engine.Subscription[engine.StateEntry], error) {
	w, err := j.KV.Watch(ctx, key)
	if err != nil {
		return nil, err
	}
	return &stateWatcher{w}, nil
}

type stateWatcher struct{ watch js.KeyWatcher }

func (w *stateWatcher) Close() error { return w.watch.Stop() }

func (w *stateWatcher) Next(ctx context.Context) (engine.StateEntry, error) {
	select {
	case <-ctx.Done():
		return engine.StateEntry{}, ctx.Err()
	case e, ok := <-w.watch.Updates():
		if !ok {
			return engine.StateEntry{}, io.EOF
		}
		return stateEntry(e), nil
	}
}

func (j *JetStream) Put(ctx context.Context, b domain.Bundle, r io.Reader) error {
	if !shared.ValidBundle(b) {
		return errors.New("invalid bundle manifest")
	}
	// Spool/verify first: no unverified object becomes visible, bounded by manifest.
	f, err := os.CreateTemp("", "nixmox-bundle-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(r, b.Bytes+1))
	if err != nil {
		return err
	}
	if n != b.Bytes || hex.EncodeToString(h.Sum(nil)) != string(b.SHA256) {
		return errors.New("bundle size/digest mismatch")
	}
	claim := []byte(b.SHA256)
	key := "artifact." + shared.Token(domain.ID(b.ObjectKey))
	if _, err = j.KV.Create(ctx, key, claim); err != nil {
		entry, e := j.KV.Get(ctx, key)
		if e != nil {
			return err
		}
		if string(entry.Value()) != string(claim) {
			return errors.New("immutable artifact key conflict")
		}
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err = j.Objects.Put(ctx, js.ObjectMeta{Name: b.ObjectKey, Opts: &js.ObjectMetaOptions{ChunkSize: domain.QueryChunkLimit}}, f)
	return err
}

func (j *JetStream) Open(ctx context.Context, b domain.Bundle) (io.ReadCloser, error) {
	if !shared.ValidBundle(b) {
		return nil, errors.New("invalid bundle manifest")
	}
	obj, err := j.Objects.Get(ctx, b.ObjectKey)
	if err != nil {
		return nil, err
	}
	info, err := obj.Info()
	if err != nil {
		obj.Close()
		return nil, err
	}
	raw, _ := hex.DecodeString(string(b.SHA256))
	if info.Size != uint64(b.Bytes) || info.Digest != "SHA-256="+base64.URLEncoding.EncodeToString(raw) {
		obj.Close()
		return nil, errors.New("stored artifact differs from manifest")
	}
	// ObjectResult validates chunk digest at EOF. Git import must consume through EOF.
	return obj, nil
}

func (j *JetStream) Delete(ctx context.Context, b domain.Bundle) error {
	return j.Objects.Delete(ctx, b.ObjectKey)
}

// Quorum is an observation, never a substitute for the write acknowledgement.
func (j *JetStream) Quorum(ctx context.Context) (domain.Quorum, error) {
	s, err := j.API.Stream(ctx, commandStream)
	if err != nil {
		return domain.Quorum{}, err
	}
	i, err := s.Info(ctx)
	if err != nil {
		return domain.Quorum{}, err
	}
	q := domain.Quorum{Replicas: i.Config.Replicas, Required: i.Config.Replicas/2 + 1}
	if i.Cluster == nil {
		q.Available = i.Config.Replicas == 1
		return q, nil
	}
	current := 0
	if i.Cluster.Leader != "" {
		current++
	}
	for _, p := range i.Cluster.Replicas {
		if p.Current && !p.Offline {
			current++
		}
	}
	q.Available = current >= q.Required
	return q, nil
}
