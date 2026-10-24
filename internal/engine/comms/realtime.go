package comms

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode"

	"go.opentelemetry.io/otel/trace"
	"nixmox/internal/engine/telemetry"

	"github.com/nats-io/nats.go"
	"golang.org/x/sync/errgroup"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/internal/shared"
)

type Core struct {
	nc            *nats.Conn
	Cluster, Node domain.ID
}

func (c *Core) Close() { c.nc.Close() }

// Connection and membership changes are event sources, not availability polls.
func (c *Core) OnConnectionChange(changed func()) {
	c.nc.SetReconnectHandler(func(*nats.Conn) { changed() })
	c.nc.SetDisconnectErrHandler(func(*nats.Conn, error) { changed() })
}
func (c *Core) NotifyMembership(ctx context.Context, node domain.ID) error {
	return c.publish(ctx, Subject(c.Cluster, "membership", node), domain.Empty{}, 1024)
}
func (c *Core) ObserveMembership(changed func()) (func(), error) {
	sub, err := c.nc.Subscribe(Subject(c.Cluster, "membership", c.Node), func(*nats.Msg) { changed() })
	if err != nil {
		return nil, err
	}
	sub.SetPendingLimits(1, 1024)
	return func() { sub.Unsubscribe() }, nil
}

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

func (c *Core) PublishUpstream(ctx context.Context, repository domain.ID, head domain.Commit) error {
	return c.publish(ctx, Subject(c.Cluster, "git.upstream", repository), struct {
		Repository domain.ID     `json:"repository_id"`
		Commit     domain.Commit `json:"commit"`
	}{repository, head}, domain.CommandPayloadLimit)
}
func (c *Core) PublishTask(ctx context.Context, task domain.Task) error {
	if task.OwnerNodeID != c.Node {
		return errors.New("task owner mismatch")
	}
	return c.publish(ctx, Subject(c.Cluster, "task.state", c.Node), task, domain.CommandPayloadLimit)
}

// ObserveTasks is Core-only: task observations disappear with the daemon session.
func (c *Core) ObserveTasks(receive func(domain.Task)) (func(), error) {
	sub, err := c.nc.Subscribe("nx."+shared.Token(c.Cluster)+".task.state.*", func(msg *nats.Msg) {
		var task domain.Task
		if len(msg.Data) <= domain.CommandPayloadLimit && json.Unmarshal(msg.Data, &task) == nil && msg.Subject == Subject(c.Cluster, "task.state", task.OwnerNodeID) {
			receive(task)
		}
	})
	if err != nil {
		return nil, err
	}
	if err = sub.SetPendingLimits(128, 4<<20); err != nil {
		sub.Unsubscribe()
		return nil, err
	}
	return func() { sub.Unsubscribe() }, nil
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

// Replies are chunked only because bounded reads may exceed NATS's payload
// limit. NATS owns reply routing, subscriptions and cancellation.
type queryReply struct {
	Sequence uint64
	Data     []byte
	Complete bool
	Error    *domain.Error
}

func (c *Core) Request(ctx context.Context, q engine.Query, view bool) (_ json.RawMessage, result error) {
	ctx, span := messageSpan(ctx, "query", "request", trace.SpanKindClient)
	span.SetName(querySpanName("request", q.Type))
	defer func() { telemetry.End(span, result) }()
	kind, replyOwner := "query", q.TargetID
	if view {
		kind, replyOwner = "view", c.Node
	} else if strings.HasPrefix(q.Type, "api.") {
		kind = "api"
	}
	if q.ID == "" || q.TargetID == "" {
		return nil, errors.New("invalid query")
	}
	q.CallerID = c.Node
	msg := nats.NewMsg(Subject(c.Cluster, kind, replyOwner))
	msg.Reply = Subject(c.Cluster, "reply", replyOwner) + "." + shared.Token(q.ID)
	var err error
	msg.Data, err = json.Marshal(q)
	if err != nil {
		return nil, err
	}
	if len(msg.Data) > domain.CommandPayloadLimit {
		return nil, errors.New("query exceeds payload bound")
	}
	injectHeaders(ctx, msg.Header)
	sub, err := c.nc.SubscribeSync(msg.Reply)
	if err != nil {
		return nil, err
	}
	defer sub.Unsubscribe()
	if err = sub.SetPendingLimits(2048, 2*domain.QueryTotalLimit); err != nil {
		return nil, err
	}
	if err = c.nc.FlushWithContext(ctx); err != nil {
		return nil, err
	}
	if err = c.nc.PublishMsg(msg); err != nil {
		return nil, err
	}
	var data json.RawMessage
	for sequence := uint64(0); ; sequence++ {
		message, err := sub.NextMsgWithContext(ctx)
		if err != nil {
			return nil, err
		}
		if message.Header.Get("Status") == "503" {
			return nil, nats.ErrNoResponders
		}
		var reply queryReply
		if err := json.Unmarshal(message.Data, &reply); err != nil {
			return nil, err
		}
		if reply.Sequence != sequence || len(reply.Data) > domain.QueryChunkLimit || len(data)+len(reply.Data) > domain.QueryTotalLimit {
			return nil, errors.New("invalid query sequence/size")
		}
		if reply.Error != nil {
			return nil, reply.Error
		}
		data = append(data, reply.Data...)
		if reply.Complete {
			return data, nil
		}
	}
}

// Separate local API and executor subscriptions prevent forwarding requests from
// occupying the handlers needed to answer them. Authorization stays in service.
func (c *Core) ServeQueries(ctx context.Context, kind string, handler func(context.Context, engine.Query) (json.RawMessage, error), ready func()) error {
	subject := Subject(c.Cluster, kind, c.Node)
	if kind == "view" {
		subject = "nx." + shared.Token(c.Cluster) + ".view.*"
	}
	sub, err := c.nc.SubscribeSync(subject)
	if err != nil {
		return err
	}
	defer sub.Unsubscribe()
	if err = sub.SetPendingLimits(32, 4<<20); err != nil {
		return err
	}
	if ready != nil {
		ready()
	}
	var handlers errgroup.Group
	handlers.SetLimit(16)
	defer handlers.Wait()
	for {
		msg, err := sub.NextMsgWithContext(ctx)
		if err != nil {
			return err
		}
		var q engine.Query
		if len(msg.Data) > domain.CommandPayloadLimit || json.Unmarshal(msg.Data, &q) != nil || q.ID == "" || q.TargetID != c.Node {
			continue
		}
		owner := c.Node
		if kind == "view" {
			if msg.Subject != Subject(c.Cluster, "view", q.CallerID) {
				continue
			}
			owner = q.CallerID
		}
		if msg.Reply != Subject(c.Cluster, "reply", owner)+"."+shared.Token(q.ID) {
			continue
		}
		handlers.Go(func() error {
			parent, span := messageSpan(telemetry.Extract(ctx, headerTrace(msg.Header)), "query", "process", trace.SpanKindServer)
			span.SetName(querySpanName("handle", q.Type))
			call, cancel := context.WithTimeout(parent, domain.ExportTimeout)
			defer cancel()
			data, err := handler(call, q)
			if len(data) > domain.QueryTotalLimit {
				err = errors.New("query exceeds response bound")
			}
			defer func() { telemetry.End(span, err) }()
			var failure *domain.Error
			if err != nil {
				if !errors.As(err, &failure) {
					failure = &domain.Error{Code: "QUERY_FAILED", Message: "Owner query failed"}
				}
				data = nil
			}
			for sequence := uint64(0); ; sequence++ {
				n := min(len(data), domain.QueryChunkLimit)
				reply := queryReply{Sequence: sequence, Data: data[:n], Complete: n == len(data), Error: failure}
				if publishErr := c.publish(call, msg.Reply, reply, domain.CommandPayloadLimit); publishErr != nil {
					err = publishErr
					return nil
				}
				if reply.Complete {
					return nil
				}
				data = data[n:]
			}
		})
	}
}

func (c *Core) Import(ctx context.Context, in engine.SecretIngress, r io.Reader) (domain.Receipt, error) {
	value, err := io.ReadAll(io.LimitReader(r, domain.CommandPayloadLimit+1))
	if err != nil {
		return domain.Receipt{}, err
	}
	defer clear(value)
	if len(value) == 0 || len(value) > domain.CommandPayloadLimit || in.HolderID == "" || in.CommandID == "" || strings.ContainsFunc(string(in.CommandID)+string(in.HolderID)+in.Name+in.Kind, unicode.IsControl) {
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
	var reply secretReply
	if err = json.Unmarshal(result.Data, &reply); err != nil {
		return domain.Receipt{}, errors.New("invalid secret receipt")
	}
	if reply.Error != nil {
		return domain.Receipt{}, reply.Error
	}
	return reply.Receipt, nil
}
