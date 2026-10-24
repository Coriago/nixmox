package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/comms"
	taskengine "nixmox/internal/engine/task"
)

func (c *ClusterNode) leaf() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.keys.Trust != nil && !c.creator()
}

// call uses the existing bounded/chunked Core query transport. Leaf views have
// their own caller-bound subject, never wildcard query or JetStream permissions.
func (c *ClusterNode) call(ctx context.Context, target domain.ID, kind string, input any, view bool) (json.RawMessage, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	transport, identity := c.transport, c.keys.Identity
	if target == "" {
		target = c.keys.Identity.NodeID
		if c.keys.Trust != nil {
			target = c.keys.Trust.Cluster.IssuerNodeID
		}
	}
	c.mu.Unlock()
	return fetchQuery(ctx, transport, identity, target, kind, data, view)
}

func (c *ClusterNode) callResult(ctx context.Context, target domain.ID, kind string, input any, view bool, result any) error {
	data, err := c.call(ctx, target, kind, input, view)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, result)
}

func fetchQuery(ctx context.Context, transport *comms.Runtime, identity domain.Identity, target domain.ID, kind string, data []byte, view bool) (json.RawMessage, error) {
	if transport.Core == nil {
		return nil, unreachable()
	}
	ctx, cancel := context.WithTimeout(ctx, domain.ExportTimeout)
	defer cancel()
	epoch := uint64(0)
	if identity.MembershipEpoch != nil {
		epoch = *identity.MembershipEpoch
	}
	q := engine.Query{ID: domain.ID(uuid.NewString()), Type: kind, TargetID: target, MembershipEpoch: epoch, Payload: data}
	result, err := transport.Core.Request(ctx, q, view)
	var detail *domain.Error
	if err != nil && !errors.As(err, &detail) {
		return nil, unreachable()
	}
	return result, err
}

func (s *Service) query(ctx context.Context, core *comms.Core, q engine.Query, view bool) (json.RawMessage, error) {
	c := s.Cluster
	c.mu.Lock()
	valid := c.transport.Core == core && (!view || c.state != nil)
	if valid && view {
		member, exists := c.state.Members[q.CallerID]
		valid = exists && !member.Removed && member.Epoch == q.MembershipEpoch
	} else if valid {
		valid = q.CallerID == c.keys.Identity.NodeID || (c.keys.Trust != nil && q.CallerID == c.keys.Trust.Cluster.IssuerNodeID)
	}
	c.mu.Unlock()
	if !valid {
		return nil, &domain.Error{Code: "MEMBERSHIP_REQUIRED", Message: "Current authenticated membership is required"}
	}
	if strings.HasPrefix(q.Type, "api.") {
		if view || q.CallerID != s.Identity.NodeID {
			return nil, invalid("API ingress is local")
		}
		q.Type = strings.TrimPrefix(q.Type, "api.")
	}
	if q.Type == "command.submit" {
		var command domain.Command
		if err := decodeQuery(q.Payload, &command); err != nil {
			return nil, err
		}
		op, _, err := s.Registry.Decode(command)
		if err != nil {
			return nil, err
		}
		if view && op.Scope != domain.NodeLocal {
			c.mu.Lock()
			full := c.state.Members[q.CallerID].Full
			c.mu.Unlock()
			if !full {
				return nil, invalid("Cluster mutations require full membership")
			}
		}
		result, err := s.queueCommand(ctx, q.CallerID, command)
		return marshalResult(result, err)
	}
	if q.Type == "command.receipt" {
		return queryJSON(ctx, q.Payload, s.Receipt)
	}
	if view && q.Type == "tasks.prepare" {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.transport.Store == nil {
			return nil, unreachable()
		}
		return marshalResult(domain.Empty{}, c.transport.Store.PrepareTasks(ctx, q.CallerID))
	}
	if view && (q.Type == "source.prepare" || q.Type == "source.accept") {
		var command domain.Command
		var accepted sourceAcceptance
		if q.Type == "source.prepare" {
			if err := decodeQuery(q.Payload, &command); err != nil {
				return nil, err
			}
		} else {
			if err := decodeQuery(q.Payload, &accepted); err != nil {
				return nil, err
			}
			command.ID = accepted.CommandID
		}
		var input domain.Envelope
		if err := c.callResult(ctx, q.CallerID, "task.input", taskengine.TaskID(command.ID), false, &input); err != nil {
			return nil, err
		}
		if input.Command.ID != command.ID || input.OwnerNodeID != q.CallerID || !repositoryOperation(input.Command.Type) {
			return nil, invalid("Source executor mismatch")
		}
		if q.Type == "source.prepare" {
			_, value, err := s.Registry.Decode(input.Command)
			if err != nil {
				return nil, err
			}
			c.mu.Lock()
			state, err := c.repositoryInput(ctx, input.Command, value)
			c.mu.Unlock()
			return marshalResult(state, err)
		}
		var current domain.Task
		if err := c.callResult(ctx, q.CallerID, "task.state", taskengine.TaskID(command.ID), false, &current); err != nil {
			return nil, err
		}
		expected, _ := json.Marshal(accepted.State)
		if !bytes.Equal(current.Result, expected) {
			return nil, invalid("Pinned source result mismatch")
		}
		result, err := c.acceptSource(ctx, command.ID, input.Command.Type, accepted.State)
		return marshalResult(result, err)
	}
	if view && q.Type == "power.authorize" {
		return queryJSON(ctx, q.Payload, func(ctx context.Context, id domain.ID) (domain.PowerReservation, error) {
			return c.authorizePower(ctx, q.CallerID, id)
		})
	}
	if !view {
		if q.Type == "credential.inventory" || q.Type == "credential.consume" || q.Type == "credential.remove" || q.Type == "credential.removable" {
			return queryJSON(ctx, q.Payload, func(ctx context.Context, input credentialLocalRequest) (any, error) {
				return c.credentialLocal(ctx, q.Type, input)
			})
		}
		switch q.Type {
		case "source.upstream":
			return queryJSON(ctx, q.Payload, func(ctx context.Context, repository domain.Repository) (domain.Commit, error) {
				head, err := c.git.RemoteHead(ctx, repository)
				if err != nil {
					return "", &domain.Error{Code: "SOURCE_UNAVAILABLE", Message: "Upstream observation failed"}
				}
				return head, nil
			})
		case "source.check":
			return queryJSON(ctx, q.Payload, func(ctx context.Context, source domain.Source) (domain.Empty, error) {
				return domain.Empty{}, c.checkConfigurationSource(ctx, source)
			})
		case "source.configurations":
			return queryJSON(ctx, q.Payload, func(ctx context.Context, input sourceCatalogRequest) ([]domain.Configuration, error) {
				return c.configurations(ctx, input.State, input.RefID, input.Commit)
			})
		case "power.inspect":
			info, err := c.powerInfo()
			return marshalResult(info, err)
		case "membership.route":
			c.mu.Lock()
			address := c.routeAddress()
			c.mu.Unlock()
			return json.Marshal(address)
		case "membership.prepare_leave":
			for _, current := range s.Tasks.List() {
				if current.Scope == domain.ClusterScope && !taskTerminal(current.State) {
					return nil, invalid("Host still owns unfinished cluster tasks")
				}
			}
			return json.Marshal(domain.Empty{})
		case "task.input":
			return queryJSON(ctx, q.Payload, s.Tasks.Input)
		case "task.state":
			return queryJSON(ctx, q.Payload, s.Tasks.Task)
		case "task.logs":
			return queryJSON(ctx, q.Payload, func(ctx context.Context, request logQuery) (domain.LogChunk, error) {
				return s.Tasks.Read(ctx, request.TaskID, nil, request.Offset, request.Limit)
			})
		}
	}
	if q.Type == "read" {
		var request ReadRequest
		if err := decodeQuery(q.Payload, &request); err != nil {
			return nil, err
		}
		if view && request.Collection != "hosts" && request.Collection != "cluster/status" && request.Collection != "repositories" && request.Collection != "configurations" && request.Collection != "credentials" && request.Collection != "tasks" {
			return nil, invalid("This leaf view is not permitted")
		}
		if (request.Collection == "identity" || request.Collection == "network/status") && (view || q.CallerID != s.Identity.NodeID) {
			return nil, invalid("Owner-local read is not a cluster view")
		}
		return s.read(ctx, request)
	}
	return nil, domain.Unavailable(q.Type)
}

func queryJSON[T, R any](ctx context.Context, data []byte, handle func(context.Context, T) (R, error)) (json.RawMessage, error) {
	var input T
	if err := decodeQuery(data, &input); err != nil {
		return nil, err
	}
	result, err := handle(ctx, input)
	return marshalResult(result, err)
}
func decodeQuery(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(value) != nil || decoder.Decode(new(any)) != io.EOF {
		return invalid("Invalid query payload")
	}
	return nil
}
func unreachable() error {
	return &domain.Error{Code: "TARGET_UNREACHABLE", Message: "Authenticated owner query is unavailable", Retryable: true}
}

// A pending gateway obtains only the requested host sample through a scoped
// view. It cannot subscribe to other nodes' unrestricted activity subjects.
type remoteTelemetry struct {
	cluster  *ClusterNode
	host     domain.ID
	interval time.Duration
	next     time.Time
	last     domain.HostTelemetry
	closed   context.Context
	cancel   context.CancelFunc
}

func (s *remoteTelemetry) Close() error { s.cancel(); return nil }
func (s *remoteTelemetry) Next(ctx context.Context) (domain.HostTelemetry, error) {
	for {
		select {
		case <-ctx.Done():
			return domain.HostTelemetry{}, ctx.Err()
		case <-s.closed.Done():
			return domain.HostTelemetry{}, s.closed.Err()
		case <-time.After(time.Until(s.next)):
		}
		s.next = time.Now().Add(s.interval)
		var sample domain.HostTelemetry
		if err := s.cluster.callResult(ctx, "", "read", ReadRequest{Collection: "hosts", ID: &s.host, Subresource: "telemetry"}, true, &sample); err != nil {
			return sample, err
		}
		if sample.HostID != s.host {
			return sample, errors.New("telemetry owner mismatch")
		}
		if sample.StreamID != s.last.StreamID || sample.Sequence != s.last.Sequence || sample.Freshness != s.last.Freshness {
			s.last = sample
			return sample, nil
		}

	}
}
