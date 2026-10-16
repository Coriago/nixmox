package messaging

import (
	"context"
	"encoding/json"
	"github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/jobs"
	"nixmox/internal/natsx"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T) (context.Context, *natsx.EmbeddedServer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	opts, err := natsx.NewOptions("bus-test", 0, 0, "127.0.0.1", t.TempDir(), natsx.ClusterSpec{})
	if err != nil {
		t.Fatal(err)
	}
	emb, err := natsx.Start(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(emb.Stop)
	return ctx, emb
}
func agent(t *testing.T, ctx context.Context, emb *natsx.EmbeddedServer, node string, handler Handler) *Bus {
	t.Helper()
	b, err := New(ctx, node, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b.Handle = handler
	b.Rearm(ctx, emb)
	t.Cleanup(b.Stop)
	deadline := time.Now().Add(5 * time.Second)
	for b.connection() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if b.connection() == nil {
		t.Fatal("bus never became ready")
	}
	return b
}
func TestLocalAndRemoteDurableCommandsAndDuplicateProtection(t *testing.T) {
	ctx, emb := fixture(t)
	var executions atomic.Int32
	handler := func(context.Context, Request) Response { executions.Add(1); return response(202, "accepted") }
	local := agent(t, ctx, emb, "local", handler)
	remote := agent(t, ctx, emb, "remote", handler)
	for _, target := range []string{"local", "remote"} {
		req := Request{ID: NewID(), Target: target, Method: "POST", Path: "/apply"}
		for range 3 {
			res, err := local.Command(ctx, req)
			if err != nil || res.Status != 202 {
				t.Fatal(res, err)
			}
		}
		req.Body = []byte("different")
		res, err := local.Command(ctx, req)
		if err != nil || res.Status != 409 {
			t.Fatal(res, err)
		}
	}
	if executions.Load() != 2 {
		t.Fatal("duplicate execution", executions.Load())
	}
	stream, err := emb.JS.Stream(ctx, Stream)
	if err != nil {
		t.Fatal(err)
	}
	info, err := stream.Info(ctx)
	if err != nil || info.State.Msgs != 2 {
		t.Fatal(info, err)
	}
	remote.Rearm(ctx, nil)
	req := Request{ID: NewID(), Target: "remote", Method: "POST", Path: "/queued", Expires: time.Now().Add(time.Minute)}
	data, _ := json.Marshal(req)
	if _, err := emb.JS.Publish(ctx, commandSubject("remote"), data, jetstream.WithMsgID(req.ID)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	remote.Rearm(ctx, emb)
	res, err := local.Command(ctx, req)
	if err != nil || res.Status != 202 {
		t.Fatal(res, err)
	}
	if executions.Load() != 3 {
		t.Fatal(executions.Load())
	}
}
func TestInterruptedReceiptAndExpiredCommandNeverExecute(t *testing.T) {
	ctx, emb := fixture(t)
	var executions atomic.Int32
	b := agent(t, ctx, emb, "local", func(context.Context, Request) Response { executions.Add(1); return response(200, "unsafe") })
	req := Request{ID: NewID(), Target: "local", Method: "POST", Path: "/write", Expires: time.Now().Add(time.Minute)}
	if err := b.save(Receipt{ID: req.ID, Hash: fingerprint(req), State: "executing"}); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(req)
	if _, err := emb.JS.Publish(ctx, commandSubject("local"), data); err != nil {
		t.Fatal(err)
	}
	res, err := b.Command(ctx, req)
	if err != nil || res.Status != 409 || !strings.Contains(string(res.Body), "interrupted") {
		t.Fatal(res, err)
	}
	req.ID = NewID()
	req.Expires = time.Now().Add(-time.Minute)
	res, err = b.Command(ctx, req)
	if err != nil || res.Status != 409 || !strings.Contains(string(res.Body), "expired") {
		t.Fatal(res, err)
	}
	if executions.Load() != 0 {
		t.Fatal("unsafe replay")
	}
}
func TestChunkedQueryAndUnavailableTransport(t *testing.T) {
	ctx, emb := fixture(t)
	body := strings.Repeat("log line\n", 200000)
	b := agent(t, ctx, emb, "local", func(context.Context, Request) Response { return response(200, body) })
	res, err := b.Query(ctx, Request{Target: "local", Method: "GET", Path: "/log"})
	if err != nil || string(res.Body) != body {
		t.Fatal("log truncated", len(res.Body), err)
	}
	b.Rearm(ctx, nil)
	call, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if _, err := b.Command(call, Request{Target: "local", Method: "POST", Path: "/apply"}); err == nil {
		t.Fatal("command bypassed unavailable NATS")
	}
}

func TestTopologyCommandReturnsReceiptAfterBrokerGenerationStops(t *testing.T) {
	ctx, emb := fixture(t)
	b, err := New(ctx, "local", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b.Handle = func(context.Context, Request) Response { b.Rearm(ctx, nil); return response(200, "join accepted") }
	b.Rearm(ctx, emb)
	defer b.Stop()
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res, err := b.Command(call, Request{ID: NewID(), Target: "local", Method: "POST", Path: "/join"})
	if err != nil || string(res.Body) != "join accepted" {
		t.Fatal(res, err)
	}
}

func TestRedeliveryAfterReceiptDoesNotRepeatEffects(t *testing.T) {
	ctx, emb := fixture(t)
	var calls atomic.Int32
	b := agent(t, ctx, emb, "host", func(context.Context, Request) Response { calls.Add(1); return response(200, "done") })
	req := Request{ID: NewID(), Target: "host", Method: "POST", Path: "/effect", Expires: time.Now().Add(time.Minute)}
	if _, err := b.Command(ctx, req); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(req)
	// A different broker message ID forces real redelivery to the consumer,
	// bypassing the broker deduplication window and exercising the receipt.
	ack, err := emb.JS.Publish(ctx, commandSubject("host"), data, jetstream.WithMsgID(NewID()))
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := emb.JS.Consumer(ctx, Stream, "host_"+token("host"))
	if err != nil {
		t.Fatal(err)
	}
	for {
		info, err := consumer.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if info.AckFloor.Stream >= ack.Sequence {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if calls.Load() != 1 {
		t.Fatal("external effect replayed", calls.Load())
	}
}

func TestLiveLogChunksCarryOffsetsAndEndOnReconfigure(t *testing.T) {
	ctx, emb := fixture(t)
	b := agent(t, ctx, emb, "logger", func(context.Context, Request) Response { return response(200, "ok") })
	sub, err := b.SubscribeLog("logger", "job-stream")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	payload := []byte(strings.Repeat("line\n", 20000))
	b.PublishLog("job-stream", jobs.LogChunk{Offset: 20, Next: 20 + int64(len(payload)), Data: payload})
	var got []byte
	offset := int64(20)
	for len(got) < len(payload) {
		select {
		case part := <-sub.Updates:
			if part.Offset != offset || part.Next != offset+int64(len(part.Data)) {
				t.Fatal("gap", part)
			}
			got = append(got, part.Data...)
			offset = part.Next
		case <-ctx.Done():
			t.Fatal("missing log chunks")
		}
	}
	if string(got) != string(payload) {
		t.Fatal("changed log bytes")
	}
	b.Rearm(ctx, nil)
	select {
	case <-sub.Done:
	case <-time.After(time.Second):
		t.Fatal("subscription remained on old broker")
	}
}

func TestCommandResultIsCorrelatedWithRequestContents(t *testing.T) {
	ctx, emb := fixture(t)
	b := agent(t, ctx, emb, "observer", func(context.Context, Request) Response { return response(200, "ok") })
	req := Request{ID: NewID(), Target: "not-running", Method: "POST", Path: "/effect", Body: []byte("first")}
	other := req
	other.Body = []byte("different")
	ctx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := b.Command(ctx, req); done <- err }()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("accepted the response to another request using the same command ID")
			}
			return
		case <-tick.C:
			b.reply(other, response(202, "wrong request"))
		}
	}
}
