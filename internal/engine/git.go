package engine

import (
	"context"

	"nixmox/internal/domain"
)

type Worktree interface {
	Path() string
	Commit() domain.Commit
	Close() error
}
type Git interface {
	Fetch(context.Context, domain.Repository) (domain.Commit, error)
	Materialize(context.Context, domain.Source) (Worktree, error)
	Commit(context.Context, Worktree, []domain.FileManifest, string) (domain.Commit, error)
	Push(context.Context, domain.Repository, domain.Commit, string) (domain.Publication, error)
	ObservePublication(context.Context, domain.Publication) (domain.Publication, error)
}
type PublicationProvider interface {
	CreatePullRequest(context.Context, domain.Publication) (domain.Publication, error)
	Merge(context.Context, domain.Publication, domain.Commit) (domain.Publication, error)
}
