package testutil

import (
	"context"
	"io"

	"nixmox/internal/engine"
)

type RunnerFunc func(context.Context, engine.CommandLine, io.Writer, io.Writer) (engine.ProcessResult, error)

func (f RunnerFunc) Run(ctx context.Context, p engine.CommandLine, out, errout io.Writer) (engine.ProcessResult, error) {
	return f(ctx, p, out, errout)
}
