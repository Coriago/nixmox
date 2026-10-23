package engine

import (
	"context"
	"io"

	"nixmox/internal/domain"
)

type BuildRequest struct {
	Source    domain.Source
	Worktree  string
	System    string
	Attribute string
}
type OutputPin interface {
	Path() string
	Close() error
}
type Nix interface {
	Validate(context.Context, domain.Source, string) (domain.Validation, error)
	BuildPlan(BuildRequest) (CommandLine, error)
	ObserveOutput(context.Context, string) (domain.StoreOutput, error)
	Closure(context.Context, string) ([]domain.ClosurePath, error)
	Pin(context.Context, string, domain.ID) (OutputPin, error)
	ActivationPlan(string, domain.ActivationMode) (CommandLine, error)
	ObserveSystem(context.Context) (domain.Generation, error)
}
type PackageDiffer interface {
	Compare(context.Context, []domain.ClosurePath, []domain.ClosurePath) (domain.PackageDiff, error)
}
type StoreExport interface {
	io.WriterTo
	io.Closer
}

type StoreCache interface {
	Open(context.Context, domain.StoreOutput) (StoreExport, error)
	Lookup(context.Context, string) (domain.StoreOutput, error)
}
