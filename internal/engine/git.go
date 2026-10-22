package engine

import (
	"context"
	"io"

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
	// Require remote-base prerequisites: never use --all/full base bundles.
	Bundle(context.Context, domain.Repository, domain.Commit, []domain.Commit, io.Writer) (domain.Bundle, error)
	Import(context.Context, domain.Repository, domain.Bundle, io.Reader) error
	Rebase(context.Context, Worktree, domain.Commit) ([]domain.FileChange, error)
	Push(context.Context, domain.Repository, domain.Commit, string) (domain.Publication, error)
	ObservePublication(context.Context, domain.Publication) (domain.Publication, error)
}
type PublicationProvider interface {
	CreatePullRequest(context.Context, domain.Publication) (domain.Publication, error)
	Merge(context.Context, domain.Publication, domain.Commit) (domain.Publication, error)
}
