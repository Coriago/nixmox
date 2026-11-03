// Package installer coordinates installer selection and host-owned launch tasks.
package installer

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"nixmox/internal/cluster"
	"nixmox/internal/host"
	"nixmox/internal/installer/finalize"
	"nixmox/internal/installer/installers"
	"nixmox/internal/installer/prepare"
	"nixmox/internal/source"
	"nixmox/internal/task"
)

var ErrInvalid = errors.New("invalid installer request")
var ErrUnavailable = errors.New("installer execution unavailable")

type Registration struct {
	Kind       string     `json:"kind"`
	System     string     `json:"system"`
	TargetRepo source.Pin `json:"targetRepo"`
	FlakeDir   string     `json:"flakeDir"`
}

type Service struct {
	HostID      string
	Definitions cluster.Store[installers.Definition]
	Tasks       *task.Manager
	Repository  func(context.Context, string) (source.Repository, error)
	Inspect     func(context.Context) (installers.Target, error)
	Transition  Transition
}

// Transition is the daemon boundary: it must verify recovery support, complete
// quorum-safe demotion, stage private identity/network handoff and persist the
// original task for restart. No permissive default implementation is provided.
type Transition interface {
	Ready(context.Context, installers.Target) error
	Demote(context.Context, installers.Target) error
	Stage(context.Context, *task.Execution, host.BuildResult) (prepare.Boot, error)
	Persist(context.Context, *task.Execution) error
	PreserveInstalled(context.Context, *task.Execution, string) error
}

func (s *Service) Register(ctx context.Context, input Registration) (installers.Definition, error) {
	profile, err := installers.Lookup(input.Kind)
	if err != nil {
		return installers.Definition{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	definition, err := profile.Definition(input.System, input.TargetRepo, input.FlakeDir)
	if err != nil {
		return definition, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if s.Repository == nil {
		return definition, fmt.Errorf("%w: repository resolver missing", ErrUnavailable)
	}
	if _, err = s.Repository(ctx, input.TargetRepo.ID); err != nil {
		return definition, err
	}
	definition.ID = uuid.NewString()
	_, err = s.Definitions.Create(ctx, definition.ID, definition)
	return definition, err
}

func (s *Service) target(ctx context.Context, id string) (installers.Target, error) {
	if id != s.HostID {
		return installers.Target{}, fmt.Errorf("%w: remote host dispatch is not implemented", ErrUnavailable)
	}
	if s.Inspect == nil {
		return installers.Target{}, fmt.Errorf("%w: host inspection missing", ErrUnavailable)
	}
	target, err := s.Inspect(ctx)
	if err == nil && target.NodeID != id {
		err = errors.New("host inspection returned a different node")
	}
	return target, err
}

func Preparations() []prepare.Definition { return []prepare.Definition{(prepare.Kexec{}).Definition()} }
func Finalizers() []finalize.Definition {
	return []finalize.Definition{(finalize.General{}).Definition()}
}

func (s *Service) FinalizeChecks(ctx context.Context, id, root, system string) (installers.Check, error) {
	target, err := s.target(ctx, id)
	if err != nil {
		return installers.Check{}, err
	}
	return (finalize.General{}).Check(finalize.Installation{Target: target, Root: root, SystemPath: system}), nil
}
