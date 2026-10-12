package natsx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// RemovePeer waits for committed removal. A departing metadata leader must
// step down first: once it removes itself it cannot acknowledge its own RPC.
func (e *EmbeddedServer) RemovePeer(ctx context.Context, name string) error {
	body, _ := json.Marshal(server.JSApiMetaServerRemoveRequest{Server: name})
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.Srv.JetStreamIsLeader() {
			msg, err := e.Sys.RequestWithContext(ctx, server.JSApiLeaderStepDown, nil)
			if err != nil {
				return err
			}
			var reply server.JSApiLeaderStepDownResponse
			if err = json.Unmarshal(msg.Data, &reply); err != nil {
				return err
			}
			if reply.Error != nil {
				return fmt.Errorf("step down before removal: %s", reply.Error.Description)
			}
		}
		attempt, cancel := context.WithTimeout(ctx, 2*time.Second)
		msg, err := e.Sys.RequestWithContext(attempt, server.JSApiRemoveServer, body)
		cancel()
		if err == nil {
			var reply server.JSApiMetaServerRemoveResponse
			if err = json.Unmarshal(msg.Data, &reply); err != nil {
				return err
			}
			if reply.Success {
				return nil
			}
			if reply.Error != nil {
				code := server.ErrorIdentifier(reply.Error.ErrCode)
				// A retry may observe removal already committed after an RPC timed out.
				if code == server.JSClusterServerNotMemberErr {
					return nil
				}
				if code != server.JSClusterServerMemberChangeInflightErr && reply.Error.Code < 500 {
					return fmt.Errorf("remove peer: %s", reply.Error.Description)
				}
			}
		} else if !errors.Is(err, nats.ErrNoResponders) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, nats.ErrTimeout) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}
