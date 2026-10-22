package service

import (
	"context"
	"encoding/json"
	"io"
	"nixmox/internal/domain"
	"nixmox/internal/engine"
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
type Service struct{ Registry *Registry }

func New() *Service { return &Service{Registry: Catalog()} }
func (s *Service) Submit(ctx context.Context, command domain.Command) (domain.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return domain.Receipt{}, err
	}
	if _, _, err := s.Registry.Decode(command); err != nil {
		return domain.Receipt{}, err
	}
	// Gate until durable receipts, scope admission, handler validation and recovery
	// exist together. Never report acceptance for an unwired operation.
	return domain.Receipt{}, domain.Unavailable(command.Type)
}
func (s *Service) Receipt(context.Context, domain.ID) (domain.Receipt, error) {
	return domain.Receipt{}, domain.Unavailable("durable receipts")
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
