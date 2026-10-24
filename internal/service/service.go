// Package service registers operations and adapts HTTP to NATS.
package service

import (
	"context"
	"errors"
	"os"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/comms"
	"nixmox/internal/engine/platform"
	"nixmox/internal/engine/task"
	"nixmox/internal/engine/telemetry"
)

type ReadRequest struct {
	Collection  string
	ID          *domain.ID
	Subresource string
	Page        domain.PageRequest
}
type Service struct {
	Cache     *Cache
	Registry  *Registry
	Cluster   *ClusterNode
	Identity  domain.Identity
	Tasks     *task.Manager
	Sampler   engine.HostSampler
	Discovery *platform.Discovery
	Network   func(context.Context) (domain.NetworkStatus, error)
}

func New() *Service { return &Service{Registry: Catalog()} }

// Submit only forwards. Admission/routing run in the NATS request handler.
func (s *Service) Submit(ctx context.Context, command domain.Command) (domain.Receipt, error) {
	if s.Cluster == nil {
		return domain.Receipt{}, domain.Unavailable("NATS command ingress")
	}
	var receipt domain.Receipt
	s.Cluster.mu.Lock()
	joined := s.Cluster.keys.Trust != nil
	s.Cluster.mu.Unlock()
	err := s.Cluster.callResult(ctx, "", "command.submit", command, joined, &receipt)
	return receipt, err
}

func (s *Service) queueCommand(ctx context.Context, caller domain.ID, command domain.Command) (domain.Receipt, error) {
	hash, err := task.RequestHash(command)
	if err != nil {
		return domain.Receipt{}, err
	}
	c := s.Cluster
	if local, lookup := s.Tasks.Receipt(ctx, command.ID); lookup == nil {
		if local.Hash != hash {
			return domain.Receipt{}, &domain.Error{Code: "IDEMPOTENCY_CONFLICT", Message: "Command ID already used in this session"}
		}
		return local.Receipt, nil
	}
	c.mu.Lock()
	prior, err := c.commandReceiptLocked(command.ID, hash)
	c.mu.Unlock()
	if prior.TaskID != nil || err != nil {
		return prior, err
	}
	op, value, err := s.Registry.Decode(command)
	if err != nil {
		return domain.Receipt{}, err
	}
	owner := s.Identity.NodeID
	var controlled *domain.Task
	if op.Scope == domain.Inherited {
		current, err := s.taskState(ctx, value.(domain.TaskTarget).TaskID)
		if err != nil {
			return domain.Receipt{}, err
		}
		controlled, op.Scope, owner = &current, current.Scope, current.OwnerNodeID
	} else {
		if op.validate == nil {
			return domain.Receipt{}, domain.Unavailable(command.Type)
		}
		if op.Scope == domain.NodeLocal {
			if command.TargetID != caller {
				return domain.Receipt{}, invalid("Node-local target must match requesting host")
			}
			owner = caller
		}
	}
	now := time.Now().UTC()
	envelope := domain.Envelope{SchemaVersion: 1, RequestHash: hash, Command: command, Scope: op.Scope, OwnerNodeID: owner, AdmittedAt: now, ExpiresAt: now.Add(domain.CommandTTL), Trace: telemetry.Inject(ctx)}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Recheck duplicates and validate/resolve/publish against one generation.
	if prior, err := c.commandReceiptLocked(command.ID, hash); prior.TaskID != nil || err != nil {
		return prior, err
	}
	if controlled == nil {
		if err := op.validate(c, ctx, command, value); err != nil {
			return domain.Receipt{}, err
		}
	}
	if op.Scope == domain.ClusterScope {
		if err := c.quorum(ctx); err != nil {
			return domain.Receipt{}, err
		}
		envelope.ClusterID = &c.state.Cluster.ID
		if op.resolve != nil {
			if err := op.resolve(c, ctx, &envelope, value); err != nil {
				return domain.Receipt{}, err
			}
		}
		member, ok := c.state.Members[envelope.OwnerNodeID]
		if !ok || member.Removed {
			return domain.Receipt{}, invalid("Executor is not an active member")
		}
		envelope.MembershipEpoch = member.Epoch
	}
	if c.transport.Core == nil {
		return domain.Receipt{}, unreachable()
	}
	publisher := &comms.JetStream{API: c.transport.API, Cluster: c.transport.Core.Cluster, Node: s.Identity.NodeID}
	if err := publisher.Submit(ctx, envelope); err != nil {
		return domain.Receipt{}, err
	}
	id := task.TaskID(command.ID)
	receipt := domain.Receipt{CommandID: command.ID, Admission: "queued", TaskID: &id}
	kind := command.Type
	if controlled != nil && command.Type == "task.retry" {
		kind = controlled.Kind
	}
	c.observeTaskLocked(domain.Task{Record: domain.Record{SchemaVersion: 1, ID: id}, CommandID: command.ID, Kind: kind, Scope: op.Scope, ClusterID: envelope.ClusterID, OwnerNodeID: envelope.OwnerNodeID, TargetID: command.TargetID, MembershipEpoch: envelope.MembershipEpoch, State: domain.Queued, CreatedAt: now, UpdatedAt: now}, hash)
	return receipt, nil
}
func (c *ClusterNode) commandReceiptLocked(id domain.ID, hash domain.Digest) (domain.Receipt, error) {
	prior, exists := c.taskViews[task.TaskID(id)]
	if !exists {
		return domain.Receipt{}, nil
	}
	if prior.Hash == "" {
		return domain.Receipt{}, &domain.Error{Code: "COMMAND_ALREADY_QUEUED", Message: "Prior task was observed without its request fingerprint; inspect it before submitting fresh work"}
	}
	if prior.Hash != hash {
		return domain.Receipt{}, &domain.Error{Code: "IDEMPOTENCY_CONFLICT", Message: "Command ID already used in this session"}
	}
	return prior.receipt(), nil
}

func (s *Service) Receipt(ctx context.Context, id domain.ID) (domain.Receipt, error) {
	if s.Tasks != nil {
		record, err := s.Tasks.Receipt(ctx, id)
		if !errors.Is(err, os.ErrNotExist) {
			return record.Receipt, err
		}
	}
	c := s.Cluster
	if c == nil {
		return domain.Receipt{}, os.ErrNotExist
	}
	if c.leaf() {
		var receipt domain.Receipt
		err := c.callResult(ctx, "", "command.receipt", id, true, &receipt)
		return receipt, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	observation, ok := c.taskViews[task.TaskID(id)]
	if !ok {
		return domain.Receipt{}, os.ErrNotExist
	}
	return observation.receipt(), nil
}

type Deployments interface {
	Resolve(context.Context, domain.ID, domain.DeploymentApply) (domain.Source, error)
	Review(context.Context, domain.Candidate, domain.Generation) (domain.PackageDiff, error)
	Approve(context.Context, domain.DeploymentApprove) error
	Observe(context.Context, domain.Deployment) (domain.Deployment, error)
}
