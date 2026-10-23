package comms

import (
	"context"
	"crypto/tls"
	"time"

	"github.com/nats-io/nats.go"
)

// Runtime owns one immutable transport generation. Closing it cancels consumers
// before closing clients and the broker. Policy/configuration belongs to services.
type Runtime struct {
	Context    context.Context
	Broker     *EmbeddedBroker
	Core       *Core
	Store      *JetStream
	cancel     context.CancelFunc
	connection *nats.Conn
}

func OpenRuntime(ctx context.Context, options BrokerOptions, clientTLS *tls.Config, storage *StorageLimits) (_ *Runtime, err error) {
	r := &Runtime{}
	r.Context, r.cancel = context.WithCancel(ctx)
	defer func() {
		if err != nil {
			r.Close()
		}
	}()
	if r.Broker, err = StartBroker(r.Context, options); err != nil {
		return nil, err
	}
	address := "tls://" + options.ClientAddress
	if r.connection, err = ConnectBroker(address, options.NodeID, clientTLS); err != nil {
		return nil, err
	}
	if storage != nil {
		deadline := time.NewTimer(20 * time.Second)
		defer deadline.Stop()
		for {
			r.Store, err = OpenJetStream(r.Context, r.connection, options.ClusterID, options.NodeID, *storage)
			if err == nil {
				break
			}
			select {
			case <-r.Context.Done():
				return nil, r.Context.Err()
			case <-deadline.C:
				return nil, err
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	if r.Core, err = OpenCore(address, options.NodeID, options.ClusterID, clientTLS); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Runtime) Close() {
	if r.cancel != nil {
		r.cancel()
	}
	if r.Core != nil {
		r.Core.Close()
	}
	if r.connection != nil {
		r.connection.Close()
	}
	if r.Broker != nil {
		r.Broker.Close()
	}
}
