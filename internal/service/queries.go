package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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
	if target == "" && c.keys.Trust != nil {
		target = c.keys.Trust.Cluster.IssuerNodeID
	}
	c.mu.Unlock()
	return fetchQuery(ctx, transport, identity, target, kind, data, view)
}

func fetchQuery(ctx context.Context, transport *comms.Runtime, identity domain.Identity, target domain.ID, kind string, data []byte, view bool) (json.RawMessage, error) {
	var err error
	if identity.ClusterID == nil || identity.MembershipEpoch == nil || transport.Core == nil {
		return nil, unreachable()
	}
	ctx, cancel := context.WithTimeout(ctx, domain.ExportTimeout)
	defer cancel()
	q := engine.Query{ID: domain.ID(uuid.NewString()), Type: kind, TargetID: target, MembershipEpoch: *identity.MembershipEpoch, Payload: data}
	var replies engine.Subscription[engine.QueryReply]
	if view {
		replies, err = transport.Core.View(ctx, q)
	} else {
		replies, err = transport.Core.Query(ctx, q)
	}
	if err != nil {
		return nil, unreachable()
	}
	defer replies.Close()
	var result []byte
	for {
		reply, err := replies.Next(ctx)
		if err != nil {
			return nil, unreachable()
		}
		if reply.Error != nil {
			return nil, reply.Error
		}
		result = append(result, reply.Data...)
		if reply.Complete {
			return result, nil
		}
	}
}

// Query workers follow transport generations, so authorization changes close old
// subscriptions before exposing the next generation's grants.
func (s *Service) ServeQueries(ctx context.Context, views bool) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		c := s.Cluster
		c.mu.Lock()
		transport, joined, creator := c.transport, c.keys.Trust != nil, c.creator()
		c.mu.Unlock()
		if !joined || transport.Core == nil || (views && !creator) {
			continue
		}
		handler := func(call context.Context, q engine.Query) (io.ReadCloser, error) {
			value, err := s.query(call, transport.Core, q, views)
			if err != nil {
				return nil, err
			}
			return io.NopCloser(bytes.NewReader(value)), nil
		}
		if views {
			_ = transport.Core.ServeViews(transport.Context, handler)
		} else {
			_ = transport.Core.ServeQueries(transport.Context, handler)
		}
	}
}

func (s *Service) query(ctx context.Context, core *comms.Core, q engine.Query, view bool) (json.RawMessage, error) {
	c := s.Cluster
	c.mu.Lock()
	valid := c.transport.Core == core && c.keys.Trust != nil && (!view || c.state != nil)
	if valid && view {
		member, exists := c.state.Members[q.CallerID]
		valid = exists && !member.Removed && member.Epoch == q.MembershipEpoch
	} else if valid {
		valid = q.CallerID == c.keys.Trust.Cluster.IssuerNodeID
	}
	c.mu.Unlock()
	if !valid {
		return nil, &domain.Error{Code: "MEMBERSHIP_REQUIRED", Message: "Current authenticated membership is required"}
	}
	if view && q.Type == "source.submit" {
		c.mu.Lock()
		full := c.state.Members[q.CallerID].Full
		c.mu.Unlock()
		if !full {
			return nil, invalid("Source mutations require a full-member console")
		}
		var command domain.Command
		if err := decodeQuery(q.Payload, &command); err != nil {
			return nil, err
		}
		if !repositoryOperation(command.Type) && !credentialOperation(command.Type) && command.Type != "host.assign" {
			return nil, invalid("Operation is not a source command")
		}
		receipt, err := s.Submit(ctx, command)
		if err != nil {
			return nil, err
		}
		return json.Marshal(receipt)
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
		c.mu.Lock()
		envelope, _, err := readRecord[domain.Envelope](ctx, c.transport.Store, recordKey("command", command.ID))
		if err == nil && (envelope.OwnerNodeID != q.CallerID || !repositoryOperation(envelope.Command.Type)) {
			err = invalid("Source executor assignment mismatch")
		}
		var state domain.RepositoryState
		if err == nil && q.Type == "source.prepare" {
			_, value, decodeErr := Catalog().Decode(envelope.Command)
			err = decodeErr
			if err == nil {
				state, err = c.repositoryInput(ctx, envelope.Command, value)
			}
		}
		c.mu.Unlock()
		if err != nil {
			return nil, err
		}
		if q.Type == "source.prepare" {
			return json.Marshal(state)
		}
		// Reconcile against the executor's checkpoint, not a caller-selected candidate.
		var task domain.Task
		data, err := c.call(ctx, q.CallerID, "task.state", taskengine.TaskID(command.ID), false)
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &task); err != nil {
			return nil, err
		}
		matched := false
		for _, cp := range task.Checkpoints {
			if cp.Phase == "fetch" {
				var checkpoint domain.RepositoryState
				if json.Unmarshal(cp.Evidence, &checkpoint) == nil {
					a, _ := json.Marshal(checkpoint)
					b, _ := json.Marshal(accepted.State)
					matched = bytes.Equal(a, b)
				}
			}
		}
		if !matched {
			return nil, invalid("Pinned source checkpoint unavailable")
		}
		result, err := c.acceptSource(ctx, command.ID, envelope.Command.Type, accepted.State)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
	if q.Type == "membership.topology" {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.state == nil {
			return nil, notFound()
		}
		return json.Marshal(c.state.Topology)
	}
	if view && q.Type == "power.authorize" {
		var id domain.ID
		if err := decodeQuery(q.Payload, &id); err != nil {
			return nil, err
		}
		reservation, err := c.authorizePower(ctx, q.CallerID, id)
		if err != nil {
			return nil, err
		}
		return json.Marshal(reservation)
	}
	if !view {
		if q.Type == "credential.inventory" || q.Type == "credential.consume" || q.Type == "credential.remove" || q.Type == "credential.removable" || q.Type == "credential.expired" || q.Type == "credential.expire" {
			var input credentialLocalRequest
			if err := decodeQuery(q.Payload, &input); err != nil {
				return nil, err
			}
			value, err := c.credentialLocal(ctx, q.Type, input)
			if err != nil {
				return nil, err
			}
			return json.Marshal(value)
		}
		switch q.Type {
		case "source.check":
			var source domain.Source
			if err := decodeQuery(q.Payload, &source); err != nil {
				return nil, err
			}
			if err := c.checkConfigurationSource(ctx, source); err != nil {
				return nil, err
			}
			return json.Marshal(domain.Empty{})
		case "source.configurations":
			var input sourceCatalogRequest
			if err := decodeQuery(q.Payload, &input); err != nil {
				return nil, err
			}
			items, err := c.configurations(ctx, input.State, input.RefID, input.Commit)
			if err != nil {
				return nil, err
			}
			return json.Marshal(items)
		case "power.inspect":
			info, err := c.powerInfo()
			if err != nil {
				return nil, err
			}
			return json.Marshal(info)
		case "membership.route":
			c.mu.Lock()
			address := c.routeAddress()
			c.mu.Unlock()
			return json.Marshal(address)
		case "membership.prepare_leave":
			page, err := s.Journal.Recoverable(ctx, domain.PageRequest{Limit: domain.PageLimit})
			if err != nil {
				return nil, err
			}
			for _, current := range page.Items {
				if current.Scope == domain.ClusterScope {
					return nil, invalid("Host still owns unfinished cluster tasks")
				}
			}
			return json.Marshal(domain.Empty{})
		case "command.admit":
			var envelope domain.Envelope
			if err := decodeQuery(q.Payload, &envelope); err != nil {
				return nil, err
			}
			result, err := s.assign(ctx, envelope)
			if err != nil {
				return nil, err
			}
			return json.Marshal(result)
		case "task.state":
			var id domain.ID
			if err := decodeQuery(q.Payload, &id); err != nil {
				return nil, err
			}
			current, err := s.Journal.Task(ctx, id)
			if err != nil {
				return nil, err
			}
			if current.ClusterID == nil || *current.ClusterID != core.Cluster {
				return nil, invalid("Task is outside this cluster")
			}
			return json.Marshal(current)
		case "task.logs":
			var request logQuery
			if err := decodeQuery(q.Payload, &request); err != nil {
				return nil, err
			}
			current, err := s.Journal.Task(ctx, request.TaskID)
			if err != nil {
				return nil, err
			}
			if current.ClusterID == nil || *current.ClusterID != core.Cluster {
				return nil, invalid("Task is outside this cluster")
			}
			chunk, err := s.Journal.Read(ctx, request.TaskID, nil, request.Offset, request.Limit)
			if err != nil {
				return nil, err
			}
			return json.Marshal(chunk)
		}
	}
	if q.Type == "read" {
		var request ReadRequest
		if err := decodeQuery(q.Payload, &request); err != nil {
			return nil, err
		}
		if view && request.Collection != "hosts" && request.Collection != "cluster/status" && request.Collection != "repositories" && request.Collection != "configurations" && request.Collection != "credentials" {
			return nil, invalid("This leaf view is not permitted")
		}
		if request.Collection == "identity" || request.Collection == "network/status" {
			return nil, invalid("Owner-local read is not a cluster view")
		}
		return s.Read(ctx, request)
	}
	return nil, domain.Unavailable(q.Type)
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
	last     domain.HostTelemetry
	closed   context.Context
	cancel   context.CancelFunc
}

func (s *remoteTelemetry) Close() error { s.cancel(); return nil }
func (s *remoteTelemetry) Next(ctx context.Context) (domain.HostTelemetry, error) {
	for {
		if s.last.StreamID != "" {
			select {
			case <-ctx.Done():
				return domain.HostTelemetry{}, ctx.Err()
			case <-s.closed.Done():
				return domain.HostTelemetry{}, s.closed.Err()
			case <-time.After(s.interval):
			}
		}
		value, err := s.cluster.call(ctx, "", "read", ReadRequest{Collection: "hosts", ID: &s.host, Subresource: "telemetry"}, true)
		if err != nil {
			return domain.HostTelemetry{}, err
		}
		var sample domain.HostTelemetry
		if err = json.Unmarshal(value, &sample); err != nil {
			return sample, err
		}
		if sample.HostID != s.host {
			return sample, errors.New("telemetry owner mismatch")
		}
		if sample.StreamID != s.last.StreamID || sample.Sequence != s.last.Sequence || sample.Freshness != s.last.Freshness {
			s.last = sample
			return sample, nil
		}
		// Unknown samples have no stream ID; still bound polling when unchanged.
		select {
		case <-ctx.Done():
			return sample, ctx.Err()
		case <-s.closed.Done():
			return sample, s.closed.Err()
		case <-time.After(s.interval):
		}
	}
}
