package cluster

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// TLSConfig rejects absent trust and always verifies peers. Callers supply the
// certificate issued for this node, not a shared cluster client identity.
func TLSConfig(certificate, key, ca []byte) (*tls.Config, error) {
	pair, err := tls.X509KeyPair(certificate, key)
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("invalid cluster CA")
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{pair},
		RootCAs:      roots,
		ClientCAs:    roots,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}, nil
}

func Connect(endpoints []string, config *tls.Config, nodeID string) (*nats.Conn, error) {
	if config == nil || config.InsecureSkipVerify || config.RootCAs == nil || len(config.Certificates) == 0 {
		return nil, errors.New("verified node TLS configuration required")
	}
	if len(endpoints) == 0 {
		return nil, errors.New("NATS endpoints required")
	}
	for _, endpoint := range endpoints {
		if !strings.HasPrefix(endpoint, "tls://") {
			return nil, errors.New("NATS endpoint must use tls://")
		}
	}
	return nats.Connect(strings.Join(endpoints, ","), nats.Secure(config.Clone()), nats.Name(nodeID), nats.CustomInboxPrefix("_INBOX."+nodeID), nats.Timeout(5*time.Second), nats.MaxReconnects(-1), nats.ReconnectWait(time.Second))
}

// NewServer validates mTLS for clients and routes without starting listeners.
// This lets callers configure and launch all cluster peers before waiting.
func NewServer(options server.Options) (*server.Server, error) {
	if options.TLSConfig == nil || options.TLSConfig.ClientAuth != tls.RequireAndVerifyClientCert || !options.TLSMap || len(options.Users) == 0 {
		return nil, errors.New("server requires mTLS and explicit certificate-mapped users")
	}
	if options.Cluster.Port != 0 && (options.Cluster.TLSConfig == nil || options.Cluster.TLSConfig.ClientAuth != tls.RequireAndVerifyClientCert || options.Cluster.TLSConfig.InsecureSkipVerify) {
		return nil, errors.New("cluster routes require verified mutual TLS")
	}
	options.NoSigs, options.NoLog = true, true
	return server.NewServer(&options)
}

// StartServer starts a standalone embedded server and waits for its listeners.
func StartServer(options server.Options) (*server.Server, error) {
	s, err := NewServer(options)
	if err != nil {
		return nil, err
	}
	s.Start()
	if !s.ReadyForConnections(10 * time.Second) {
		s.Shutdown()
		s.WaitForShutdown()
		return nil, fmt.Errorf("NATS server %s failed readiness", options.ServerName)
	}
	return s, nil
}

type headerCarrier struct{ nats.Header }

func (h headerCarrier) Get(key string) string { return h.Header.Get(key) }

func (h headerCarrier) Set(key, value string) { h.Header.Set(key, value) }
func (h headerCarrier) Keys() []string {
	keys := make([]string, 0, len(h.Header))
	for key := range h.Header {
		keys = append(keys, key)
	}
	return keys
}

func Inject(ctx context.Context, msg *nats.Msg) {
	if msg.Header == nil {
		msg.Header = nats.Header{}
	}
	otel.GetTextMapPropagator().Inject(ctx, headerCarrier{msg.Header})
}

func Extract(ctx context.Context, msg *nats.Msg) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, headerCarrier{msg.Header})
}

// DispatchContext is persisted with durable work, never its request cancellation.
func DispatchContext(ctx context.Context) propagation.MapCarrier {
	carrier := propagation.MapCarrier{}
	otel.GetTextMapPropagator().Inject(ctx, carrier)
	return carrier
}

func ResumeContext(lifetime context.Context, carrier propagation.MapCarrier) context.Context {
	return otel.GetTextMapPropagator().Extract(lifetime, carrier)
}

type Response struct {
	Output json.RawMessage `json:"output,omitempty"`
	Error  string          `json:"error,omitempty"`
}

func RequestJSON(ctx context.Context, nc *nats.Conn, subject string, input, output any) error {
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	message, err := Request(ctx, nc, subject, data)
	if err != nil {
		return err
	}
	var response Response
	if err = json.Unmarshal(message.Data, &response); err != nil {
		return err
	}
	if response.Error != "" {
		return errors.New(response.Error)
	}
	if output == nil {
		return nil
	}
	return json.Unmarshal(response.Output, output)
}

func Request(ctx context.Context, nc *nats.Conn, subject string, data []byte) (*nats.Msg, error) {
	ctx, span := otel.Tracer("nixmox.cluster").Start(ctx, "nats.request", trace.WithSpanKind(trace.SpanKindProducer))
	defer span.End()
	msg := nats.NewMsg(subject)
	msg.Data = data
	Inject(ctx, msg)
	return nc.RequestMsgWithContext(ctx, msg)
}
