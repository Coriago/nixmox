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
	subscription, err := nc.SubscribeSync("host." + nodeID + ".observe")
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
			var request host.ObservationRequest
			var response host.ObservationResponse
			err = json.Unmarshal(message.Data, &request)
			if err == nil {
				var result any
				result, err = host.ObserveLocal(operation, host.Process{}, request)
				if err == nil {
					response.Output, err = json.Marshal(result)
				}
				if len(response.Output) > 512<<10 {
					err = errors.New("host observation exceeds 512 KiB")
				}
			}
			cancel()
			if err != nil {
				response.Output, response.Error = nil, err.Error()
			}
			data, err := json.Marshal(response)
			if err == nil {
				message.Respond(data)
			}
		}
	})
	return nil
}
