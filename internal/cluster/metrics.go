package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/nats-io/nats.go"
)

type Metrics struct{ Connection *nats.Conn }

// Publish uses core NATS: live telemetry is replaceable, not retained history.
func (m Metrics) Publish(ctx context.Context, id string, data []byte) error {
	if !subjectID.MatchString(id) || !json.Valid(data) {
		return errors.New("invalid host metrics")
	}
	message := nats.NewMsg("metrics.host." + id)
	message.Data = data
	Inject(ctx, message)
	return m.Connection.PublishMsg(message)
}

func (m Metrics) Follow(ctx context.Context, id string, handle func(json.RawMessage) error) error {
	if !subjectID.MatchString(id) {
		return errors.New("invalid host ID")
	}
	latest := make(chan []byte, 1)
	subscription, err := m.Connection.Subscribe("metrics.host."+id, func(message *nats.Msg) {
		select {
		case latest <- message.Data:
		default:
			select {
			case <-latest:
			default:
			}
			select {
			case latest <- message.Data:
			default:
			}
		}
	})
	if err != nil {
		return err
	}
	defer subscription.Unsubscribe()
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err = m.Connection.FlushWithContext(probe); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case data := <-latest:
			if !json.Valid(data) {
				continue
			}
			if err = handle(json.RawMessage(data)); err != nil {
				return err
			}
		}
	}
}
