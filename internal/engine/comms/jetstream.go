package comms

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	js "github.com/nats-io/nats.go/jetstream"

	"go.opentelemetry.io/otel/trace"
	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/internal/shared"
	"nixmox/internal/engine/telemetry"
)

const TaskStream = "nx_tasks"

var _ engine.ObjectStore = (*JetStream)(nil)

type JetStream struct {
	API           js.JetStream
	KV            js.KeyValue
	Objects       js.ObjectStore
	Cluster, Node domain.ID
}

type StorageLimits struct {
	Existing                 bool
	Commands, State, Objects int64
	Replicas                 int
}

func OpenJetStream(ctx context.Context, nc *nats.Conn, cluster, node domain.ID, limits StorageLimits) (*JetStream, error) {
	if cluster == "" || node == "" || (limits.Replicas != 1 && limits.Replicas != 3) || limits.Commands <= 0 || limits.State <= 0 || limits.Objects <= 0 {
		return nil, errors.New("positive storage limits and 1 or 3 replicas required")
	}
	api, err := js.New(nc)
	if err != nil {
		return nil, err
	}
	j := &JetStream{API: api, Cluster: cluster, Node: node}
	if limits.Existing {
		for _, name := range durableStreams() {
			if _, err = api.Stream(ctx, name); err != nil {
				return nil, err
			}
		}
		j.KV, err = api.KeyValue(ctx, domain.StateBucket)
		if err != nil {
			return nil, err
		}
		j.Objects, err = api.ObjectStore(ctx, domain.ObjectBucket)
		return j, err
	}
	prefix := "nx." + shared.Token(cluster) + "."
	for _, cfg := range []js.StreamConfig{
		{Name: TaskStream, Subjects: []string{prefix + "task.submit.*", prefix + "task.control.*"}, Storage: js.FileStorage, Retention: js.WorkQueuePolicy, Discard: js.DiscardNew, MaxBytes: limits.Commands, MaxMsgSize: 512 << 10, Replicas: limits.Replicas, MaxAge: domain.CommandTTL, Duplicates: domain.CommandTTL},
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
	j.Objects, err = api.CreateObjectStore(ctx, js.ObjectStoreConfig{Bucket: domain.ObjectBucket, Storage: js.FileStorage, Replicas: limits.Replicas, MaxBytes: limits.Objects})
	if err != nil {
		return nil, err
	}
	// Object storage must not evict old chunks at capacity. KV maintains one
	// authoritative revision per key; its own per-subject retention is intentional.
	stream, err := api.Stream(ctx, "OBJ_"+domain.ObjectBucket)
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

func (j *JetStream) Submit(ctx context.Context, e domain.Envelope) (result error) {
	ctx, span := messageSpan(ctx, "command", "publish", trace.SpanKindProducer)
	defer func() { telemetry.End(span, result) }()
	e.Trace = telemetry.Inject(ctx)
	if (e.Scope == domain.ClusterScope && (e.ClusterID == nil || *e.ClusterID != j.Cluster)) || e.Command.ID == "" || e.OwnerNodeID == "" || len(e.Command.Payload) > domain.CommandPayloadLimit || e.ExpiresAt.IsZero() {
		return errors.New("invalid command envelope")
	}
	if time.Now().After(e.ExpiresAt) {
		return &domain.Error{Code: "COMMAND_EXPIRED", Message: "Command expired before submission"}
	}
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	kind := "task.submit"
	if e.Command.Type == "task.cancel" || e.Command.Type == "task.retry" {
		kind = "task.control"
	}
	msg := nats.NewMsg(Subject(j.Cluster, kind, e.OwnerNodeID))
	msg.Data = data
	injectHeaders(ctx, msg.Header)
	ack, err := j.API.PublishMsg(ctx, msg, js.WithMsgID(string(e.Command.ID)))
	if err != nil {
		return &domain.Error{Code: "SUBMISSION_UNKNOWN", Message: "No durable publish acknowledgement; query the receipt before retrying", Retryable: true}
	}
	if ack.Duplicate {
		return &domain.Error{Code: "COMMAND_ALREADY_QUEUED", Message: "Command ID was already queued; its previous outcome may be unavailable. Review before submitting fresh work."}
	}
	return nil
}

// Consumer names bind pull and ack permissions to a single host.
func TaskConsumer(node domain.ID, control bool) string {
	prefix := "submit_"
	if control {
		prefix = "control_"
	}
	return prefix + shared.Token(node)
}

func (j *JetStream) PrepareTasks(ctx context.Context, node domain.ID) error {
	for _, control := range []bool{false, true} {
		kind := "task.submit"
		if control {
			kind = "task.control"
		}
		_, err := j.API.CreateConsumer(ctx, TaskStream, js.ConsumerConfig{Durable: TaskConsumer(node, control), FilterSubject: Subject(j.Cluster, kind, node), AckPolicy: js.AckExplicitPolicy, MaxAckPending: 8, MaxWaiting: 8, AckWait: 30 * time.Second})
		if err != nil {
			return err
		}
	}
	return nil
}

func (j *JetStream) Put(ctx context.Context, b domain.StoredObject, r io.Reader) error {
	if !shared.ValidStoredObject(b) {
		return errors.New("invalid object manifest")
	}
	// Spool/verify first: no unverified object becomes visible, bounded by manifest.
	f, err := os.CreateTemp("", "nixmox-object-*")
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
		return errors.New("object size/digest mismatch")
	}
	claim := []byte(b.SHA256)
	// Keep the durable claim namespace so existing object keys remain immutable.
	key := "artifact." + shared.Token(domain.ID(b.ObjectKey))
	if _, err = j.KV.Create(ctx, key, claim); err != nil {
		entry, e := j.KV.Get(ctx, key)
		if e != nil {
			return err
		}
		if string(entry.Value()) != string(claim) {
			return errors.New("immutable object key conflict")
		}
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err = j.Objects.Put(ctx, js.ObjectMeta{Name: b.ObjectKey, Opts: &js.ObjectMetaOptions{ChunkSize: domain.QueryChunkLimit}}, f)
	return err
}

func (j *JetStream) Open(ctx context.Context, b domain.StoredObject) (io.ReadCloser, error) {
	if !shared.ValidStoredObject(b) {
		return nil, errors.New("invalid object manifest")
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
		return nil, errors.New("stored object differs from manifest")
	}
	// ObjectResult validates chunk digest at EOF. Consumers must read through EOF.
	return obj, nil
}

func (j *JetStream) Delete(ctx context.Context, b domain.StoredObject) error {
	return j.Objects.Delete(ctx, b.ObjectKey)
}

// Quorum is an observation, never a substitute for the write acknowledgement.
func (j *JetStream) Quorum(ctx context.Context) (domain.Quorum, error) {
	s, err := j.API.Stream(ctx, TaskStream)
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
