package comms

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
)

type secretReply struct {
	Receipt domain.Receipt `json:"receipt"`
	Error   *domain.Error  `json:"error,omitempty"`
}

// ServeSecrets has a bounded subscription queue and no payload tracing.
// Broker grants allow only full members to publish and this holder to subscribe.
func (c *Core) ServeSecrets(ctx context.Context, accept func(context.Context, engine.SecretIngress, io.Reader) (domain.Receipt, error)) error {
	sub, err := c.nc.SubscribeSync(Subject(c.Cluster, "secret", c.Node))
	if err != nil {
		return err
	}
	defer sub.Unsubscribe()
	if err = sub.SetPendingLimits(8, 8*domain.CommandPayloadLimit); err != nil {
		return err
	}
	if err = c.nc.FlushWithContext(ctx); err != nil {
		return err
	}
	for {
		msg, err := sub.NextMsgWithContext(ctx)
		if err != nil {
			return err
		}
		if len(msg.Data) == 0 || len(msg.Data) > domain.CommandPayloadLimit || msg.Reply == "" {
			clear(msg.Data)
			continue
		}
		in := engine.SecretIngress{CommandID: domain.ID(msg.Header.Get("Nx-Command")), HolderID: domain.ID(msg.Header.Get("Nx-Holder")), Name: msg.Header.Get("Nx-Name"), Kind: msg.Header.Get("Nx-Kind")}
		call, cancel := context.WithTimeout(ctx, 30*time.Second)
		var reply secretReply
		if in.HolderID != c.Node {
			err = &domain.Error{Code: "INVALID_INPUT", Message: "Secret holder mismatch"}
		} else {
			reply.Receipt, err = accept(call, in, bytes.NewReader(msg.Data))
		}
		cancel()
		clear(msg.Data)
		if err != nil && !errors.As(err, &reply.Error) {
			reply.Error = &domain.Error{Code: "CREDENTIAL_UNAVAILABLE", Message: "Credential import could not be completed"}
		}
		data, err := json.Marshal(reply)
		if err == nil {
			_ = msg.Respond(data)
		}
	}
}
