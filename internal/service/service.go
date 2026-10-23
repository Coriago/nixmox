package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/platform"
	"nixmox/internal/engine/task"
	"nixmox/internal/engine/telemetry"
	"nixmox/internal/logging"
)

type Commands interface {
	Submit(context.Context, domain.Command) (domain.Receipt, error)
	Receipt(context.Context, domain.ID) (domain.Receipt, error)
}
type ReadRequest struct {
	Collection  string
	ID          *domain.ID
	Subresource string
	Page        domain.PageRequest
}
type Reads interface {
	Read(context.Context, ReadRequest) (json.RawMessage, error)
	Events(context.Context, domain.EventSelector) (engine.Subscription[domain.Event], error)
	Download(context.Context, ReadRequest) (domain.Download, io.ReadCloser, error)
}
type SecretIngress interface {
	Import(context.Context, engine.SecretIngress, io.Reader) (domain.Receipt, error)
	WiFi(context.Context, domain.ID, io.Reader) (domain.SecretStaging, error)
}
type Resolutions interface {
	Stage(context.Context, domain.ID, domain.Version, []domain.FileManifest, io.Reader) (domain.Resolution, error)
}
type Service struct {
	Cache     *Cache
	Registry  *Registry
	Cluster   *ClusterNode
	Identity  domain.Identity
	Host      domain.Host
	Journal   *task.Journal
	Scheduler *task.Scheduler
	Sampler   engine.HostSampler
	Discovery *platform.Discovery
	Network   func(context.Context) (domain.NetworkStatus, error)
}

func New() *Service { return &Service{Registry: Catalog()} }
func (s *Service) Submit(ctx context.Context, command domain.Command) (_ domain.Receipt, result error) {
	ctx, span := telemetry.Start(ctx, "command.admit", trace.WithAttributes(attribute.String("nixmox.command.id", string(command.ID))))
	defer func() { telemetry.End(span, result) }()
	ctx = logging.With(ctx, "command_id", string(command.ID))
	if err := ctx.Err(); err != nil {
		return domain.Receipt{}, err
	}
	op, value, err := s.Registry.Decode(command)
	if err != nil {
		return domain.Receipt{}, err
	}
	span.SetName("command.admit " + op.Name)
	if (repositoryOperation(command.Type) || command.Type == "host.assign") && s.Cluster != nil && s.Cluster.leaf() {
		data, err := s.Cluster.call(ctx, "", "source.submit", command, true)
		var receipt domain.Receipt
		if err == nil {
			err = json.Unmarshal(data, &receipt)
		}
		return receipt, err
	}
	if s.Scheduler == nil {
		return domain.Receipt{}, domain.Unavailable(command.Type)
	}
	hash, err := task.RequestHash(command)
	if err != nil {
		return domain.Receipt{}, err
	}
	var controlled *domain.Task
	if op.Scope == domain.Inherited {
		p := value.(domain.TaskTarget)
		if p.TaskID == "" || command.TargetID != p.TaskID || len(command.ExpectedVersions) != 1 || command.ExpectedVersions["task:"+string(p.TaskID)] == 0 {
			return domain.Receipt{}, invalid("Task ID must match target_id and include exactly one positive task version")
		}
		current, err := s.Journal.Task(ctx, p.TaskID)
		if err == nil {
			controlled = &current
			op.Scope = current.Scope
		} else if !errors.Is(err, os.ErrNotExist) {
			return domain.Receipt{}, err
		}
	}
	prior, err := s.Journal.Receipt(ctx, command.ID)
	if errors.Is(err, os.ErrNotExist) && op.Scope != domain.NodeLocal && s.Cluster != nil {
		prior, err = s.Cluster.Receipt(ctx, command.ID)
	}
	if err == nil {
		if prior.Envelope.RequestHash != hash {
			return domain.Receipt{}, &domain.Error{Code: "IDEMPOTENCY_CONFLICT", Message: "Command ID already used"}
		}
		if controlled != nil && prior.Envelope.Scope == domain.NodeLocal && prior.Error == nil {
			return s.Scheduler.Control(ctx, prior.Envelope)
		}
		if prior.Error != nil {
			return prior.Receipt, prior.Error
		}
		return prior.Receipt, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return domain.Receipt{}, err
	}
	if op.Scope == domain.Inherited {
		current, err := s.taskState(ctx, value.(domain.TaskTarget).TaskID)
		if err != nil {
			return domain.Receipt{}, err
		}
		controlled = &current
		op.Scope = current.Scope
	}
	if controlled == nil && op.Scope == domain.NodeLocal && command.TargetID != s.Identity.NodeID {
		return domain.Receipt{}, &domain.Error{Code: "TARGET_UNREACHABLE", Message: "Target is not this unjoined node"}
	}
	if controlled != nil {
		if controlled.Scope == domain.NodeLocal && controlled.OwnerNodeID != s.Identity.NodeID {
			return domain.Receipt{}, invalid("Node-local task controls must be submitted to their owner")
		}
	} else if command.Type == "discovery.search" {
		if s.Discovery == nil {
			return domain.Receipt{}, invalid("Discovery is disabled")
		}
		if len(command.ExpectedVersions) != 0 {
			return domain.Receipt{}, invalid("Discovery has no version prerequisites")
		}
	} else if command.Type == "network.check" {
		if len(command.ExpectedVersions) != 0 {
			return domain.Receipt{}, invalid("network.check has no mutable version prerequisites")
		}
	} else {
		if s.Cluster == nil {
			return domain.Receipt{}, domain.Unavailable(command.Type)
		}
		if err := s.Cluster.Validate(ctx, command, value); err != nil {
			return domain.Receipt{}, err
		}
	}
	now := time.Now().UTC()
	envelope := domain.Envelope{SchemaVersion: domain.SchemaVersion,
		Command: command, Scope: op.Scope, OwnerNodeID: s.Identity.NodeID, RequestHash: hash,
		AdmittedAt: now, ExpiresAt: now.Add(domain.CommandTTL), Trace: telemetry.Inject(ctx)}
	if controlled != nil {
		envelope.OwnerNodeID = controlled.OwnerNodeID
	}
	if op.Scope == domain.NodeLocal {
		if controlled != nil {
			return s.Scheduler.Control(ctx, envelope)
		}
		return s.Scheduler.Admit(ctx, envelope)
	}
	if s.Cluster == nil {
		return domain.Receipt{}, domain.Unavailable(command.Type)
	}
	if command.Type == "host.refresh" || command.Type == "host.shutdown" || command.Type == "host.reboot" {
		envelope.OwnerNodeID = command.TargetID
	}
	if err := s.Cluster.PublishCommand(ctx, envelope); err != nil {
		return domain.Receipt{}, err
	}
	// Acceptance is the executor's persisted receipt, not just a broker ack.
	wait, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if receipt, err := s.Receipt(wait, command.ID); err == nil || receipt.Admission == "rejected" {
			return receipt, err
		}
		select {
		case <-wait.Done():
			return domain.Receipt{}, &domain.Error{Code: "SUBMISSION_UNKNOWN", Message: "Command published; reconcile the original command ID", Retryable: true}
		case <-ticker.C:
		}
	}
}
func (s *Service) Receipt(ctx context.Context, id domain.ID) (domain.Receipt, error) {
	if s.Journal == nil {
		return domain.Receipt{}, domain.Unavailable("durable receipts")
	}
	record, err := s.Journal.Receipt(ctx, id)
	if errors.Is(err, os.ErrNotExist) && s.Cluster != nil {
		record, err = s.Cluster.Receipt(ctx, id)
	}
	if err == nil && record.Error != nil {
		err = record.Error
	}
	return record.Receipt, err
}

// Feature orchestration ports keep presentation and adapters out of domain rules.
type Configurations interface {
	Catalog(context.Context, domain.ID, *domain.ID, *domain.Commit) (domain.Page[domain.Configuration], error)
	Templates(context.Context, domain.Source) ([]domain.Template, error)
	Render(context.Context, domain.ConfigurationGenerate) (domain.Draft, error)
	Validate(context.Context, domain.Draft) (domain.Validation, error)
	Commit(context.Context, domain.Draft, domain.Versions) (domain.Artifact, error)
}
type Sources interface {
	Resolve(context.Context, domain.ID, domain.SourceSelector) (domain.Source, error)
	Accept(context.Context, domain.ID, domain.Draft, domain.Versions) (domain.Artifact, error)
	PlanGC(context.Context, domain.RepositoryGCPlan) (domain.GCPlan, error)
}
type Membership interface {
	Approve(context.Context, domain.JoinRequest, domain.Version) (domain.Issuance, error)
	Promote(context.Context, domain.Host, domain.Generation) error
	Remove(context.Context, domain.HostRemove) (domain.SignedRemoval, error)
}
type Deployments interface {
	Resolve(context.Context, domain.ID, domain.DeploymentApply) (domain.Source, error)
	Review(context.Context, domain.Candidate, domain.Generation) (domain.PackageDiff, error)
	Approve(context.Context, domain.DeploymentApprove) error
	Observe(context.Context, domain.Deployment) (domain.Deployment, error)
}
type Onboarding interface {
	Prepare(context.Context, domain.OnboardingPrepare) (domain.Preparation, error)
	Review(context.Context, domain.Preparation, domain.Observation) (domain.Review, error)
	Verify(context.Context, domain.Preparation, domain.Candidate) error
}
