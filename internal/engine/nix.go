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
type StoreCache interface {
	Lookup(context.Context, string) (domain.StoreOutput, error)
	// Embedded go-nix NAR serializer; hold GC pin for the entire stream.
	Export(context.Context, domain.StoreOutput, io.Writer) error
}
type CacheGateway interface {
	Locate(context.Context, string) ([]domain.CacheLocation, error)
	Open(context.Context, domain.CacheLocation) (io.ReadCloser, error)
	Status(context.Context) domain.CacheStatus
}
