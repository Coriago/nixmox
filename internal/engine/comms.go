package engine

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"

	"nixmox/internal/domain"
)

type BrokerConfig struct {
	NodeID, ClusterID             domain.ID
	DataDir                       string
	ClientAddress                 string
	RouteAddresses, LeafAddresses []string
	ClientTLS, RouteTLS, LeafTLS  *tls.Config
	Authorization                 BrokerAuthorization
}
type BrokerAuthorization struct {
	DaemonKeys     []domain.Digest
	FullBrokerKeys []domain.Digest
	LeafGrants     map[domain.ID]domain.Grants
}
type Broker interface {
	Start(context.Context, BrokerConfig) error
	Reconfigure(context.Context, BrokerAuthorization) error
	Quorum(context.Context) (domain.Quorum, error)
	// Explicit topology changes only; lost peers never cause automatic scale-down.
	SetReplicas(context.Context, []domain.ID) error
	Close(context.Context) error
}
type Delivery interface {
	Envelope() domain.Envelope
	Ack(context.Context) error // Only after receipt and deterministic task are durable.
	Retry(context.Context) error
}
type Query struct {
	CallerID        domain.ID
	MembershipEpoch uint64
	ID              domain.ID
	Type            string
	TargetID        domain.ID
	Payload         json.RawMessage
	Trace           domain.TraceContext
}
type QueryReply struct {
	RequestID domain.ID
	Sequence  uint64
	Data      []byte
	Complete  bool
	Error     *domain.Error
}
type Communications interface {
	Submit(context.Context, domain.Envelope) error
	Commands(context.Context, domain.ID) (Subscription[Delivery], error)
	Query(context.Context, Query) (Subscription[QueryReply], error)
	Result(context.Context, domain.Task) error
	PublishLog(context.Context, domain.LogEvent) error
	PublishProgress(context.Context, domain.TaskProgress) error
}
type LiveTelemetry interface {
	// Separate connection, no reconnect buffer; one pending latest value per host.
	Publish(context.Context, domain.HostTelemetry) error
	Subscribe(context.Context, domain.ID) (Subscription[domain.HostTelemetry], error)
}
type SecretIngress struct {
	CommandID  domain.ID
	HolderID   domain.ID
	Name, Kind string
}

// Secret delivery never shares durable messages, generic logging, or telemetry.
type SecretDelivery interface {
	Import(context.Context, SecretIngress, io.Reader) (domain.Receipt, error)
}
