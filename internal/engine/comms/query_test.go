package comms

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
)

func TestNativeQueryReplies(t *testing.T) {
	nc, _, _, _ := brokerFixture(t)
	core := &Core{nc: nc, Node: "node", Cluster: "cluster"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready, done := make(chan struct{}), make(chan error, 1)
	payload, _ := json.Marshal(string(bytes.Repeat([]byte("x"), 3*domain.QueryChunkLimit)))
	go func() {
		done <- core.ServeQueries(ctx, "query", func(_ context.Context, q engine.Query) (json.RawMessage, error) {
			if q.Type == "fail" {
				return nil, &domain.Error{Code: "VERSION_CONFLICT", Message: "changed"}
			}
			if q.Type == "private" {
				return nil, errors.New("must-not-leak")
			}
			return payload, nil
		}, func() { close(ready) })
	}()
	<-ready
	q := engine.Query{ID: "request", Type: "read", TargetID: "node", Payload: json.RawMessage("{}")}
	data, err := core.Request(ctx, q, false)
	if err != nil || !bytes.Equal(data, payload) {
		t.Fatal("chunked response changed", len(data), err)
	}
	q.ID, q.Type = "failure", "fail"
	_, err = core.Request(ctx, q, false)
	var detail *domain.Error
	if !errors.As(err, &detail) || detail.Code != "VERSION_CONFLICT" {
		t.Fatal(err)
	}
	q.ID, q.Type = "private", "private"
	_, err = core.Request(ctx, q, false)
	if !errors.As(err, &detail) || detail.Code != "QUERY_FAILED" || bytes.Contains([]byte(err.Error()), []byte("must-not-leak")) {
		t.Fatal("private error leaked", err)
	}
	cancel()
	<-done
}
