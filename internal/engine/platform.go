package engine

import (
	"context"
	"io"

	"nixmox/internal/domain"
)

type Network interface {
	Status(context.Context) (domain.NetworkStatus, error)
	Scan(context.Context) ([]domain.WiFiNetwork, error)
	Connect(context.Context, domain.NetworkConnect) error
	StageCredential(context.Context, domain.ID, io.Reader) (domain.SecretStaging, error)
}
type Discovery interface {
	Search(context.Context) ([]domain.Discovery, error)
	Watch(context.Context) (Subscription[domain.Discovery], error)
}
type Hardware interface {
	Observe(context.Context) (domain.Observation, error)
	Collect(context.Context, domain.CollectionPolicy) (domain.HardwareFacts, string, error)
}
type Bootstrap interface {
	Inspect(context.Context, domain.HostBootstrap) (domain.Capabilities, error)
	Start(context.Context, domain.HostBootstrap, domain.ID) (domain.Identity, error)
	Reconcile(context.Context, domain.ID) error
}
type BootAdapter interface {
	Inspect(context.Context) (domain.InstallerStatus, error)
	Stage(context.Context, domain.Handoff, domain.Media) error
	Select(context.Context, domain.BootSelection) error
	Observe(context.Context, domain.BootSelection) (domain.BootSelection, error)
}
type Preservation interface {
	Plan(context.Context, domain.Observation, []domain.ID) (domain.Handoff, error)
	Stage(context.Context, domain.Handoff) error
	Restore(context.Context, domain.Handoff) error
	Consume(context.Context, domain.Handoff) error
}

// Disk/media effects require executor-refreshed observations and bound approval.
type Installation interface {
	Preflight(context.Context, domain.Preparation, domain.Candidate) error
	Partition(context.Context, domain.Observation, domain.Review, Execution) error
	Install(context.Context, domain.Preparation, domain.Candidate, Execution) error
	Verify(context.Context, domain.Candidate) error
}
type MediaWriter interface {
	Plan(context.Context, domain.MediaPlanCreate) (domain.MediaPlan, error)
	Write(context.Context, domain.MediaPlan, domain.Observation, Execution) error
	Readback(context.Context, domain.MediaPlan) error
}
type SOPSUpdater interface {
	Update(context.Context, Worktree, domain.Preparation, string) ([]domain.FileChange, error)
}
