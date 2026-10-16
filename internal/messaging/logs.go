package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"nixmox/internal/jobs"
)

func logSubject(node, id string) string { return "nixmox.logs." + token(node) + "." + token(id) }

// Published after append. Core NATS avoids duplicating potentially large logs
// into JetStream; byte offsets allow persisted catch-up after dropped messages.
func (b *Bus) PublishLog(id string, chunk jobs.LogChunk) {
	emb := b.connection()
	if emb == nil {
		return
	}
	for len(chunk.Data) > 0 {
		n := len(chunk.Data)
		if n > 32<<10 {
			n = 32 << 10
		}
		part := jobs.LogChunk{Offset: chunk.Offset, Next: chunk.Offset + int64(n), Data: chunk.Data[:n]}
		data, _ := json.Marshal(part)
		if emb.NC.Publish(logSubject(b.node, id), data) != nil {
			return
		}
		chunk.Data = chunk.Data[n:]
		chunk.Offset = part.Next
	}
}

type LogSubscription struct {
	Updates <-chan jobs.LogChunk
	Done    <-chan struct{}
	Close   func()
}

func (b *Bus) SubscribeLog(host, id string) (LogSubscription, error) {
	b.mu.Lock()
	emb, generation := b.emb, b.readyContext
	b.mu.Unlock()
	if emb == nil || generation == nil {
		return LogSubscription{}, fmt.Errorf("NATS transport unavailable")
	}
	chunks := make(chan jobs.LogChunk, 64)
	sub, err := emb.NC.Subscribe(logSubject(host, id), func(msg *nats.Msg) {
		var chunk jobs.LogChunk
		if json.Unmarshal(msg.Data, &chunk) != nil {
			return
		}
		select {
		case chunks <- chunk:
		default:
		} // a subsequent offset gap triggers catch-up
	})
	if err != nil {
		return LogSubscription{}, err
	}
	ctx, cancel := context.WithCancel(generation)
	if err = emb.NC.FlushTimeout(2 * time.Second); err != nil {
		sub.Unsubscribe()
		cancel()
		return LogSubscription{}, err
	}
	return LogSubscription{Updates: chunks, Done: ctx.Done(), Close: func() { sub.Unsubscribe(); cancel() }}, nil
}
