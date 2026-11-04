package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/nats-io/nats.go"
	"nixmox/internal/cluster"
	"nixmox/internal/host"
)

func (r *Runtime) startObservations(nc *nats.Conn, nodeID string) error {
	return r.serveRequests(nc, "host."+nodeID+".observe", func(ctx context.Context, message *nats.Msg) (any, error) {
		var request host.ObservationRequest
		if err := json.Unmarshal(message.Data, &request); err != nil {
			return nil, err
		}
		if request.Kind == "runtime" {
			return r.membershipInfo(ctx)
		}
		return host.ObserveLocal(ctx, host.Process{}, request)
	})
}

func (r *Runtime) serveRequests(nc *nats.Conn, subject string, handle func(context.Context, *nats.Msg) (any, error)) error {
	subscription, err := nc.SubscribeSync(subject)
	if err != nil {
		return err
	}
	if err = nc.FlushTimeout(5 * time.Second); err != nil {
		subscription.Unsubscribe()
		return err
	}
	r.workers.Go(func() {
		defer subscription.Unsubscribe()
		for {
			message, err := subscription.NextMsgWithContext(r.lifetime)
			if err != nil {
				return
			}
			if message.Reply == "" {
				continue
			}
			operation, cancel := context.WithTimeout(cluster.Extract(r.lifetime, message), 30*time.Second)
			var response cluster.Response
			result, err := handle(operation, message)
			restart := errors.Is(err, errRoleRestart)
			if restart {
				err = nil
			}
			if err == nil {
				response.Output, err = json.Marshal(result)
			}
			if len(response.Output) > 512<<10 {
				err = errors.New("node response exceeds 512 KiB")
			}
			cancel()
			if err != nil {
				response.Output, response.Error = nil, err.Error()
			}
			data, err := json.Marshal(response)
			if err == nil {
				message.Respond(data)
			}
			if restart {
				nc.FlushTimeout(2 * time.Second)
				select {
				case r.failures <- errRoleRestart:
				default:
				}
				return
			}
		}
	})
	return nil
}
