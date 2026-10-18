// Package messaging carries host commands and queries over the embedded NATS
// connection. Command delivery is durable; execution receipts prevent replay of
// external effects after acknowledgement loss or process interruption.
package messaging

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/natsx"
	"nixmox/internal/statefile"
)

const Stream = "nixmox_commands"
const maxBody = 512 << 10

var validID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)

type Request struct {
	ID          string    `json:"id"`
	Target      string    `json:"target"`
	Method      string    `json:"method"`
	Path        string    `json:"path"`
	ContentType string    `json:"content_type,omitempty"`
	Body        []byte    `json:"body,omitempty"`
	Expires     time.Time `json:"expires"`
}
type Response struct {
	Status int         `json:"status"`
	Header http.Header `json:"header,omitempty"`
	Body   []byte      `json:"body,omitempty"`
}
type Receipt struct {
	ID       string    `json:"id"`
	Target   string    `json:"target"`
	Method   string    `json:"method"`
	Path     string    `json:"path"`
	Hash     string    `json:"hash"`
	State    string    `json:"state"`
	Updated  time.Time `json:"updated"`
	Response Response  `json:"response"`
}
type Handler func(context.Context, Request) Response

type Bus struct {
	node, dir    string
	lifetime     context.Context
	Handle       Handler
	Changed      func()
	mu           sync.Mutex
	emb          *natsx.EmbeddedServer
	readyContext context.Context
	cancel       context.CancelFunc
	active       map[string]bool
	workers      sync.WaitGroup
	closed       bool
}

func New(ctx context.Context, node, dataDir string) (*Bus, error) {
	dir := filepath.Join(dataDir, "commands")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return &Bus{node: node, dir: dir, lifetime: ctx, active: map[string]bool{}}, nil
}
func NewID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		panic(err)
	}
	return "cmd-" + hex.EncodeToString(id[:])
}
func token(node string) string          { return hex.EncodeToString([]byte(node)) }
func commandSubject(node string) string { return "nixmox.command." + token(node) }
func querySubject(node string) string   { return "nixmox.query." + token(node) }
func resultSubject(req Request) string {
	return "nixmox.result." + token(req.Target) + "." + req.ID + "." + fingerprint(req)
}
func (r Receipt) Matches(req Request) bool { return r.Hash == fingerprint(req) }
func fingerprint(req Request) string {
	req.Expires = time.Time{}
	b, _ := json.Marshal(req)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (b *Bus) connection() *natsx.EmbeddedServer { b.mu.Lock(); defer b.mu.Unlock(); return b.emb }
func (b *Bus) receiptPath(id string) string      { return filepath.Join(b.dir, id+".json") }
func (b *Bus) Receipt(id string) (Receipt, error) {
	if !validID.MatchString(id) {
		return Receipt{}, fmt.Errorf("invalid command ID")
	}
	data, err := os.ReadFile(b.receiptPath(id))
	if err != nil {
		return Receipt{}, err
	}
	var receipt Receipt
	err = json.Unmarshal(data, &receipt)
	return receipt, err
}
func (b *Bus) save(r Receipt) error { return statefile.Write(b.receiptPath(r.ID), r) }
func response(status int, message string) Response {
	return Response{Status: status, Header: http.Header{"Content-Type": []string{"text/plain; charset=utf-8"}}, Body: []byte(message)}
}

// Rearm follows NATS topology changes. Running handlers use daemon lifetime,
// not connection lifetime: a join/leave must survive its own NATS restart.
func (b *Bus) Rearm(ctx context.Context, emb *natsx.EmbeddedServer) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	if b.cancel != nil {
		b.cancel()
	}
	b.emb = nil
	if emb == nil {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	b.cancel = cancel
	b.workers.Add(1)
	go func() { defer b.workers.Done(); b.run(ctx, emb) }()
}
func (b *Bus) run(ctx context.Context, emb *natsx.EmbeddedServer) {
	for ctx.Err() == nil {
		if err := b.serve(ctx, emb); err != nil && ctx.Err() == nil {
			log.Printf("command transport: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}
func (b *Bus) serve(ctx context.Context, emb *natsx.EmbeddedServer) error {
	stream, err := emb.JS.Stream(ctx, Stream)
	if errors.Is(err, jetstream.ErrStreamNotFound) {
		stream, err = emb.JS.CreateStream(ctx, jetstream.StreamConfig{Name: Stream, Subjects: []string{"nixmox.command.*"}, Storage: jetstream.FileStorage, Retention: jetstream.LimitsPolicy, MaxAge: 30 * 24 * time.Hour, MaxBytes: 64 << 20, Discard: jetstream.DiscardNew, Duplicates: 10 * time.Minute, Replicas: 1})
	}
	if err != nil {
		return err
	}
	consumer, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{Durable: "host_" + token(b.node), FilterSubject: commandSubject(b.node), AckPolicy: jetstream.AckExplicitPolicy, AckWait: 30 * time.Second, BackOff: []time.Duration{10 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute}, MaxAckPending: 32})
	if err != nil {
		return err
	}
	sub, err := emb.NC.Subscribe(querySubject(b.node), func(msg *nats.Msg) {
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			return
		}
		b.workers.Add(1)
		b.mu.Unlock()
		go func() { defer b.workers.Done(); b.query(ctx, msg) }()
	})
	if err != nil {
		return err
	}
	defer sub.Unsubscribe()
	receipts, err := emb.NC.Subscribe("nixmox.receipt."+token(b.node), func(msg *nats.Msg) {
		r, e := b.Receipt(string(msg.Data))
		if e != nil {
			msg.Respond([]byte(`{}`))
			return
		}
		data, _ := json.Marshal(r)
		msg.Respond(data)
	})
	if err != nil {
		return err
	}
	defer receipts.Unsubscribe()
	if err = emb.NC.Flush(); err != nil {
		return err
	}
	b.mu.Lock()
	if ctx.Err() == nil && !b.closed {
		b.emb = emb
		b.readyContext = ctx
	}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		if b.readyContext == ctx {
			b.emb = nil
		}
		b.mu.Unlock()
	}()
	consume, err := consumer.Consume(func(msg jetstream.Msg) {
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			msg.NakWithDelay(time.Second)
			return
		}
		b.workers.Add(1)
		b.mu.Unlock()
		go func() { defer b.workers.Done(); b.deliver(msg) }()
	}, jetstream.PullMaxMessages(8))
	if err != nil {
		return err
	}
	defer consume.Stop()
	<-ctx.Done()
	return nil
}
func (b *Bus) deliver(msg jetstream.Msg) {
	var req Request
	if err := json.Unmarshal(msg.Data(), &req); err != nil || !validID.MatchString(req.ID) || req.Target != b.node {
		msg.Term()
		return
	}
	hash := fingerprint(req)
	b.mu.Lock()
	if b.active[req.ID] {
		b.mu.Unlock()
		msg.NakWithDelay(time.Second)
		return
	}
	old, err := b.Receipt(req.ID)
	if err == nil {
		if old.Hash != hash {
			b.mu.Unlock()
			b.reply(req, response(409, "command ID already used for another request"))
			msg.Ack()
			return
		}
		if old.State == "executing" {
			old.State = "interrupted"
			old.Response = response(409, "command execution interrupted; inspect effects before issuing a new command")
			old.Updated = time.Now().UTC()
			if err = b.save(old); err != nil {
				b.mu.Unlock()
				msg.NakWithDelay(time.Second)
				return
			}
		}
		b.mu.Unlock()
		b.reply(req, old.Response)
		msg.Ack()
		return
	}
	if !errors.Is(err, os.ErrNotExist) {
		b.mu.Unlock()
		msg.NakWithDelay(time.Second)
		return
	}
	receipt := Receipt{ID: req.ID, Target: req.Target, Method: req.Method, Path: req.Path, Hash: hash, State: "executing", Updated: time.Now().UTC()}
	if err = b.save(receipt); err != nil {
		b.mu.Unlock()
		msg.NakWithDelay(time.Second)
		return
	}
	b.active[req.ID] = true
	b.mu.Unlock()
	defer func() { b.mu.Lock(); delete(b.active, req.ID); b.mu.Unlock() }()
	done := make(chan struct{})
	defer close(done)
	go func() {
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				msg.InProgress()
			}
		}
	}()
	if req.Expires.IsZero() || time.Now().After(req.Expires) {
		receipt.State = "expired"
		receipt.Response = response(409, "queued command expired; submit a new command")
	} else {
		receipt.Response = b.Handle(b.lifetime, req)
		receipt.State = "completed"
	}
	receipt.Updated = time.Now().UTC()
	if err = b.save(receipt); err != nil {
		log.Printf("command receipt %s: %v", req.ID, err)
		msg.NakWithDelay(time.Second)
		return
	}
	b.reply(req, receipt.Response)
	if b.Changed != nil {
		b.Changed()
	}
	msg.Ack()
}
func (b *Bus) reply(req Request, res Response) {
	if emb := b.connection(); emb != nil {
		data, _ := json.Marshal(res)
		emb.NC.Publish(resultSubject(req), data)
	}
}

// Command returns the target's durable receipt, not an optimistic local result.
// Callers retain the ID across retries; publishing never bypasses JetStream.
func (b *Bus) Command(ctx context.Context, req Request) (Response, error) {
	if req.ID == "" {
		req.ID = NewID()
	}
	if !validID.MatchString(req.ID) {
		return Response{}, fmt.Errorf("invalid command ID")
	}
	if len(req.Body) > maxBody {
		return Response{}, fmt.Errorf("command body exceeds 512 KiB")
	}
	if req.Expires.IsZero() {
		req.Expires = time.Now().Add(2 * time.Minute)
	}
	data, _ := json.Marshal(req)
	var last error
	for ctx.Err() == nil {
		// A topology command can shut down its own broker before its result is
		// published. Its durable local receipt completes the HTTP handshake
		// needed to establish the next topology. This never executes a command.
		if req.Target == b.node {
			if r, err := b.Receipt(req.ID); err == nil {
				if !r.Matches(req) {
					return response(409, "command ID already used for another request"), nil
				}
				if r.State != "executing" {
					return r.Response, nil
				}
			}
		}
		emb := b.connection()
		if emb != nil {
			// Check retained receipts first: broker duplicate suppression deliberately
			// does not redeliver an already acknowledged message to its consumer.
			check, cancel := context.WithTimeout(ctx, time.Second)
			m, err := emb.NC.RequestWithContext(check, "nixmox.receipt."+token(req.Target), []byte(req.ID))
			cancel()
			if err == nil {
				var r Receipt
				if json.Unmarshal(m.Data, &r) == nil && r.ID != "" {
					if !r.Matches(req) {
						return response(409, "command ID already used for another request"), nil
					}
					if r.State != "executing" {
						return r.Response, nil
					}
				}
			}
			sub, err := emb.NC.SubscribeSync(resultSubject(req))
			if err == nil {
				publish, cancel := context.WithTimeout(ctx, 2*time.Second)
				_, err = emb.JS.Publish(publish, commandSubject(req.Target), data, jetstream.WithMsgID(req.Target+":"+req.ID))
				cancel()
				if err == nil {
					wait, cancel := context.WithTimeout(ctx, 2*time.Second)
					m, e := sub.NextMsgWithContext(wait)
					cancel()
					err = e
					if err == nil {
						var res Response
						err = json.Unmarshal(m.Data, &res)
						sub.Unsubscribe()
						return res, err
					}
				}
				sub.Unsubscribe()
			}
			last = err
		}
		select {
		case <-ctx.Done():
		case <-time.After(200 * time.Millisecond):
		}
	}
	return Response{}, fmt.Errorf("command %s awaiting confirmation (retry with the same Idempotency-Key): %w (%v)", req.ID, ctx.Err(), last)
}
func (b *Bus) Stop() {
	b.mu.Lock()
	b.closed = true
	if b.cancel != nil {
		b.cancel()
	}
	b.emb = nil
	b.mu.Unlock()
	b.workers.Wait()
}

// ForgetTemporary removes the installer consumer after its leaf is detached.
// Completed command receipts remain in the installer's local state; retained
// stream messages expire under the normal command retention policy.
func (b *Bus) ForgetTemporary(ctx context.Context, node string) error {
	emb := b.connection()
	if emb == nil {
		return fmt.Errorf("NATS transport unavailable")
	}
	err := emb.JS.DeleteConsumer(ctx, Stream, "host_"+token(node))
	if errors.Is(err, jetstream.ErrConsumerNotFound) {
		return nil
	}
	return err
}
