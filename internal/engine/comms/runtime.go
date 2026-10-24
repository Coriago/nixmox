package comms

import (
	"context"
	"crypto/tls"
	"errors"
	"sync"
	"time"

	"github.com/nats-io/jsm.go/api"
	"github.com/nats-io/nats.go"
	js "github.com/nats-io/nats.go/jetstream"
)

var (
	ErrMaintenance = errors.New("broker store transition requires reconciliation; durable operations are fenced")
	errNotReady    = errors.New("JetStream topology is not ready")
)

// Runtime owns one transport generation. StartRuntime exposes Core before a
// metadata election, so a peer handshake can finish and establish both routes.
// Membership policy owns admission/rearming and publishes Store only after
// ConnectStorage succeeds. Never assign Store asynchronously behind its readers.
type Runtime struct {
	Context    context.Context
	Broker     *EmbeddedBroker
	Core       *Core
	Store      *JetStream
	API        js.JetStream
	cancel     context.CancelFunc
	connection *nats.Conn
	parent     context.Context
	options    BrokerOptions
	clientTLS  *tls.Config
	closeOnce  sync.Once
}

func StartRuntime(ctx context.Context, options BrokerOptions, clientTLS *tls.Config) (_ *Runtime, err error) {
	if fenced, e := storeFenced(options.DataDir, options.NodeID, options.ClusterID); e != nil {
		return nil, e
	} else if fenced {
		options.Maintenance = true
	}
	r := &Runtime{parent: ctx, options: options, clientTLS: clientTLS}
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
	r.Core = &Core{nc: r.connection, Node: options.NodeID, Cluster: options.ClusterID}
	r.API, err = js.New(r.connection)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// OpenRuntime retains the synchronous convenience API for existing callers.
// Topology handshakes must use StartRuntime, then ConnectStorage separately.
func OpenRuntime(ctx context.Context, options BrokerOptions, clientTLS *tls.Config, storage *StorageLimits) (*Runtime, error) {
	r, err := StartRuntime(ctx, options, clientTLS)
	if err != nil {
		return nil, err
	}
	if storage != nil {
		ready, cancel := context.WithTimeout(r.Context, 20*time.Second)
		defer cancel()
		r.Store, err = r.ConnectStorage(ready, *storage)
		if err != nil {
			r.Close()
			return nil, err
		}
	}
	return r, nil
}
func (r *Runtime) ConnectStorage(ctx context.Context, limits StorageLimits) (*JetStream, error) {
	if r.options.Maintenance {
		return nil, ErrMaintenance
	}
	var store *JetStream
	err := await(ctx, func() error {
		attempt, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		api, err := js.New(r.connection)
		if err != nil {
			return err
		}
		if _, err = api.AccountInfo(attempt); err != nil {
			return err
		}
		store, err = OpenJetStream(ctx, r.connection, r.options.ClusterID, r.options.NodeID, limits)
		return err
	})
	return store, err
}
func (r *Runtime) IsLeaf() bool           { return r.options.Pending }
func (r *Runtime) StoreMode() StoreMode   { return mode(r.options) }
func (r *Runtime) StoreDirectory() string { return r.options.DataDir }
func (r *Runtime) Close() {
	r.closeOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		if r.connection != nil {
			r.connection.Close()
		}
		if r.Broker != nil {
			r.Broker.Close()
		}
	})
}

// await retries readiness/leader changes, not invalid configuration or permissions.
// The caller owns the deadline; cancellation closes administrative connections.
func await(ctx context.Context, operation func() error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := operation()
		if err == nil {
			return nil
		}
		var streamErr *js.APIError
		var adminErr *api.ApiError
		transient := errors.Is(err, errNotReady) || errors.Is(err, nats.ErrNoResponders) || errors.Is(err, nats.ErrDisconnected) || errors.Is(err, nats.ErrTimeout) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, js.ErrStreamNotFound) || errors.Is(err, js.ErrBucketNotFound) || (errors.As(err, &streamErr) && streamErr.Code >= 500) || (errors.As(err, &adminErr) && adminErr.Code >= 500)
		if !transient {
			return err
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(ctx.Err(), err)
		case <-timer.C:
		}
	}
}
