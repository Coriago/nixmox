package comms

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"sync"

	"go.opentelemetry.io/otel/trace"
	"nixmox/internal/engine/telemetry"

	"github.com/nats-io/nats.go"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/internal/shared"
)

type Core struct {
	nc            *nats.Conn
	Cluster, Node domain.ID
}

// Each Core owns a connection with reconnect buffering disabled. Use separate
// instances for telemetry and secret ingress so slow telemetry cannot delay work.
func OpenCore(address string, node, cluster domain.ID, cfg *tls.Config) (*Core, error) {
	nc, err := ConnectBroker(address, node, cfg)
	if err != nil {
		return nil, err
	}
	return &Core{nc: nc, Cluster: cluster, Node: node}, nil
}

func (c *Core) Close() { c.nc.Close() }

func (c *Core) publish(ctx context.Context, subject string, v any, limit int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(data) > limit {
		return errors.New("Core event exceeds size bound")
	}
	if !c.nc.IsConnected() {
		return nats.ErrDisconnected
	}
	msg := nats.NewMsg(subject)
	msg.Data = data
	injectHeaders(ctx, msg.Header)
	return c.nc.PublishMsg(msg)
}

func (c *Core) PublishLog(ctx context.Context, e domain.LogEvent) error {
	if e.OwnerNodeID != c.Node || e.TaskID == "" {
		return errors.New("log owner mismatch")
	}
	return c.publish(ctx, Subject(c.Cluster, "log", c.Node)+"."+shared.Token(e.TaskID), e, domain.LogEventLimit)
}

func (c *Core) PublishProgress(ctx context.Context, p domain.TaskProgress) error {
	if p.TaskID == "" {
		return errors.New("missing progress task")
	}
	return c.publish(ctx, Subject(c.Cluster, "progress", c.Node)+"."+shared.Token(p.TaskID), p, domain.CommandPayloadLimit)
}

func (c *Core) Publish(ctx context.Context, s domain.HostTelemetry) error {
	if s.HostID != c.Node || s.MembershipEpoch == nil {
		return errors.New("telemetry owner/membership mismatch")
	}
	return c.publish(ctx, Subject(c.Cluster, "activity", c.Node), s, domain.CommandPayloadLimit)
}

func (c *Core) Subscribe(ctx context.Context, node domain.ID) (engine.Subscription[domain.HostTelemetry], error) {
	sub, err := c.nc.SubscribeSync(Subject(c.Cluster, "activity", node))
	if err != nil {
		return nil, err
	}
	if err = sub.SetPendingLimits(domain.TelemetryPendingMessages, domain.TelemetryPendingBytes); err != nil {
		sub.Unsubscribe()
		return nil, err
	}
	if err = c.nc.FlushWithContext(ctx); err != nil {
		sub.Unsubscribe()
		return nil, err
	}
	return &telemetrySubscription{sub: sub, node: node}, nil
}

type telemetrySubscription struct {
	sub  *nats.Subscription
	node domain.ID
}

func (s *telemetrySubscription) Close() error { return s.sub.Unsubscribe() }

func (s *telemetrySubscription) Next(ctx context.Context) (domain.HostTelemetry, error) {
	msg, err := s.sub.NextMsgWithContext(ctx)
	if err != nil {
		return domain.HostTelemetry{}, err
	}
	var sample domain.HostTelemetry
	if err = json.Unmarshal(msg.Data, &sample); err != nil {
		return sample, err
	}
	if sample.HostID != s.node {
		return sample, errors.New("telemetry subject/owner mismatch")
	}
	return sample, nil
}

type queryRequest struct {
	Query engine.Query
	Reply string
}

func (c *Core) Query(ctx context.Context, q engine.Query) (engine.Subscription[engine.QueryReply], error) {
	return c.query(ctx, q, Subject(c.Cluster, "query", q.TargetID), q.TargetID)
}

// View is the leaf's narrowly scoped application read path. Broker grants bind
// its request subject to the caller, without granting arbitrary peer queries.
func (c *Core) View(ctx context.Context, q engine.Query) (engine.Subscription[engine.QueryReply], error) {
	return c.query(ctx, q, Subject(c.Cluster, "view", c.Node), c.Node)
}

func (c *Core) query(ctx context.Context, q engine.Query, subject string, replyOwner domain.ID) (_ engine.Subscription[engine.QueryReply], result error) {
	ctx, span := messageSpan(ctx, "query", "request", trace.SpanKindClient)
	span.SetName(querySpanName("request", q.Type))
	defer func() {
		if result != nil {
			telemetry.End(span, result)
		}
	}()
	q.Trace = telemetry.Inject(ctx)
	q.CallerID = c.Node
	if q.ID == "" || q.TargetID == "" || len(q.Payload) > domain.CommandPayloadLimit {
		return nil, errors.New("invalid query")
	}
	reply := Subject(c.Cluster, "reply", replyOwner) + "." + shared.Token(q.ID)
	sub, err := c.nc.SubscribeSync(reply)
	if err != nil {
		return nil, err
	}
	sub.SetPendingLimits(1024, domain.QueryTotalLimit+1<<20)
	if err = c.nc.FlushWithContext(ctx); err == nil {
		err = c.publish(ctx, subject, queryRequest{q, reply}, domain.CommandPayloadLimit)
	}
	if err != nil {
		sub.Unsubscribe()
		return nil, err
	}
	return &querySubscription{sub: sub, id: q.ID, span: span, ctx: ctx}, nil
}

type querySubscription struct {
	span     trace.Span
	ctx      context.Context
	once     sync.Once
	sub      *nats.Subscription
	id       domain.ID
	next     uint64
	bytes    int
	complete bool
}

func (s *querySubscription) finish(err error) error {
	var result error
	s.once.Do(func() { result = s.sub.Unsubscribe(); telemetry.End(s.span, err) })
	return result
}
func (s *querySubscription) Close() error { return s.finish(s.ctx.Err()) }

func (s *querySubscription) Next(ctx context.Context) (_ engine.QueryReply, result error) {
	defer func() {
		if result != nil && result != io.EOF {
			_ = s.finish(result)
		}
	}()
	if s.complete {
		return engine.QueryReply{}, io.EOF
	}
	m, err := s.sub.NextMsgWithContext(ctx)
	if err != nil {
		return engine.QueryReply{}, err
	}
	var q engine.QueryReply
	if err = json.Unmarshal(m.Data, &q); err != nil {
		return q, err
	}
	if q.RequestID != s.id || q.Sequence != s.next || len(q.Data) > domain.QueryChunkLimit || s.bytes+len(q.Data) > domain.QueryTotalLimit {
		return q, errors.New("invalid query sequence/size")
	}
	s.next++
	s.bytes += len(q.Data)
	s.complete = q.Complete
	if q.Complete {
		s.span.AddEvent("messaging.reply.complete")
		var failure error
		if q.Error != nil {
			failure = q.Error
		}
		_ = s.finish(failure)
	}
	return q, nil
}

// ServeQueries serializes handlers per subscription, with bounded reply bytes.
// Feature handlers must authorize query type and caller/task scope before reading.
func (c *Core) ServeQueries(ctx context.Context, handler func(context.Context, engine.Query) (io.ReadCloser, error)) error {
	return c.serveQueries(ctx, false, handler)
}
func (c *Core) ServeViews(ctx context.Context, handler func(context.Context, engine.Query) (io.ReadCloser, error)) error {
	return c.serveQueries(ctx, true, handler)
}
func (c *Core) serveQueries(ctx context.Context, view bool, handler func(context.Context, engine.Query) (io.ReadCloser, error)) error {
	subject := Subject(c.Cluster, "query", c.Node)
	if view {
		subject = "nx." + shared.Token(c.Cluster) + ".view.*"
	}
	sub, err := c.nc.SubscribeSync(subject)
	if err != nil {
		return err
	}
	defer sub.Unsubscribe()
	sub.SetPendingLimits(32, 4<<20)
	for {
		m, err := sub.NextMsgWithContext(ctx)
		if err != nil {
			return err
		}
		var req queryRequest
		if len(m.Data) > domain.CommandPayloadLimit || json.Unmarshal(m.Data, &req) != nil || req.Query.TargetID != c.Node || !validReply(c.Cluster, req.Reply) {
			continue
		}
		replyOwner := c.Node
		if view {
			if m.Subject != Subject(c.Cluster, "view", req.Query.CallerID) {
				continue
			}
			replyOwner = req.Query.CallerID
		}
		if req.Reply != Subject(c.Cluster, "reply", replyOwner)+"."+shared.Token(req.Query.ID) {
			continue
		}
		origin := headerTrace(m.Header)
		if len(origin) == 0 {
			origin = req.Query.Trace
		}
		parent, span := messageSpan(telemetry.Extract(ctx, origin), "query", "process", trace.SpanKindServer)
		span.SetName(querySpanName("handle", req.Query.Type))
		call, cancel := context.WithTimeout(parent, domain.ExportTimeout)
		reader, e := handler(call, req.Query)
		if e != nil {
			var detail *domain.Error
			if !errors.As(e, &detail) {
				detail = &domain.Error{Code: "QUERY_FAILED", Message: "Owner query failed"}
			}
			c.publish(call, req.Reply, engine.QueryReply{RequestID: req.Query.ID, Complete: true, Error: detail}, domain.CommandPayloadLimit)
			cancel()
			telemetry.End(span, e)
			continue
		}
		buf := make([]byte, domain.QueryChunkLimit)
		var seq uint64
		total := 0
		for {
			n, readErr := reader.Read(buf)
			total += n
			response := engine.QueryReply{RequestID: req.Query.ID, Sequence: seq, Data: buf[:n], Complete: readErr != nil}
			if total > domain.QueryTotalLimit || (readErr != nil && readErr != io.EOF) {
				response.Data = nil
				response.Complete = true
				response.Error = &domain.Error{Code: "QUERY_FAILED", Message: "Query exceeds bounds or read failed"}
			}
			if e = c.publish(call, req.Reply, response, domain.CommandPayloadLimit); e != nil || response.Complete {
				if e == nil && response.Error != nil {
					e = response.Error
				}
				break
			}
			seq++
		}
		reader.Close()
		cancel()
		telemetry.End(span, e)
	}
}

func validReply(cluster domain.ID, reply string) bool {
	parts := bytes.Split([]byte(reply), []byte("."))
	if len(parts) != 5 || string(parts[0]) != "nx" || string(parts[1]) != shared.Token(cluster) || string(parts[2]) != "reply" {
		return false
	}
	for _, p := range parts[3:] {
		if len(p) == 0 {
			return false
		}
		for _, b := range p {
			if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f') {
				return false
			}
		}
	}
	return true
}

func (c *Core) Import(ctx context.Context, in engine.SecretIngress, r io.Reader) (domain.Receipt, error) {
	value, err := io.ReadAll(io.LimitReader(r, domain.CommandPayloadLimit+1))
	if err != nil {
		return domain.Receipt{}, err
	}
	defer clear(value)
	if len(value) > domain.CommandPayloadLimit || in.HolderID == "" || in.CommandID == "" {
		return domain.Receipt{}, errors.New("invalid secret ingress")
	}
	msg := nats.NewMsg(Subject(c.Cluster, "secret", in.HolderID))
	msg.Data = value
	msg.Header.Set("Nx-Command", string(in.CommandID))
	msg.Header.Set("Nx-Holder", string(in.HolderID))
	msg.Header.Set("Nx-Name", in.Name)
	msg.Header.Set("Nx-Kind", in.Kind)
	result, err := c.nc.RequestMsgWithContext(ctx, msg)
	if err != nil {
		return domain.Receipt{}, &domain.Error{Code: "SUBMISSION_UNKNOWN", Message: "Secret import receipt unavailable; query the same command ID"}
	}
	var receipt domain.Receipt
	err = json.Unmarshal(result.Data, &receipt)
	return receipt, err
}
