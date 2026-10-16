package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nats-io/nats.go"
	"time"
)

type chunk struct {
	Response
	End bool `json:"end"`
}

// Queries use NATS request/reply with bounded chunks, preserving complete logs
// without forcing a large response through the broker's single-message limit.
func (b *Bus) Query(ctx context.Context, req Request) (Response, error) {
	emb := b.connection()
	if emb == nil {
		return Response{}, fmt.Errorf("NATS transport unavailable")
	}
	inbox := nats.NewInbox()
	sub, err := emb.NC.SubscribeSync(inbox)
	if err != nil {
		return Response{}, err
	}
	defer sub.Unsubscribe()
	sub.SetPendingLimits(4096, 64<<20)
	data, _ := json.Marshal(req)
	if err = emb.NC.PublishRequest(querySubject(req.Target), inbox, data); err != nil {
		return Response{}, err
	}
	var out Response
	for {
		msg, err := sub.NextMsgWithContext(ctx)
		if err != nil {
			return out, err
		}
		if msg.Header.Get("Status") == "503" {
			return out, fmt.Errorf("host %s has no NATS responder", req.Target)
		}
		var part chunk
		if err = json.Unmarshal(msg.Data, &part); err != nil {
			return out, err
		}
		if out.Status == 0 {
			out.Status = part.Status
			out.Header = part.Header
		}
		out.Body = append(out.Body, part.Body...)
		if len(out.Body) > 64<<20 {
			return out, fmt.Errorf("query response exceeds 64 MiB; inspect the host log directly")
		}
		if part.End {
			return out, nil
		}
	}
}
func (b *Bus) query(ctx context.Context, msg *nats.Msg) {
	var req Request
	if json.Unmarshal(msg.Data, &req) != nil || req.Method != "GET" || req.Target != b.node {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res := b.Handle(ctx, req)
	for len(res.Body) > 64<<10 {
		data, _ := json.Marshal(chunk{Response: Response{Status: res.Status, Header: res.Header, Body: res.Body[:64<<10]}})
		if err := msg.Respond(data); err != nil {
			return
		}
		res.Body = res.Body[64<<10:]
	}
	data, _ := json.Marshal(chunk{Response: res, End: true})
	msg.Respond(data)
}
