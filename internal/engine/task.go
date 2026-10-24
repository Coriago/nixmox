package engine

import (
	"context"
	"io"
	"time"

	"nixmox/internal/domain"
)

type CommandLine struct {
	Stdin           io.Reader
	Program         string
	Args            []string
	Directory       string
	Environment     map[string]string // Explicit allowlist; no ambient credentials.
	SensitiveOutput bool
}
type ProcessResult struct {
	ExitCode              int
	StartedAt, FinishedAt time.Time
}
type Runner interface {
	// Stream bounded output; cancellation stops/reaps the process group.
	Run(context.Context, CommandLine, io.Writer, io.Writer) (ProcessResult, error)
}
type Execution interface {
	Task() domain.Task
	Envelope() domain.Envelope
	Result(context.Context, any) error
	Phase(context.Context, string, bool) error
	Block(context.Context, domain.Review) error
	Log() io.Writer
	// Runner decorates commands with bounded session output and parsing.
	Runner(Runner) Runner
	Progress(context.Context, domain.TaskProgress) error
}
type TaskDefinition struct {
	// SafeRetry permits explicit replay of observational work.
	SafeRetry bool

	Phase         string
	Interruptible bool
	Resources     []string // Exclusive host-local keys, held for the whole invocation.
	Adapter       *LogAdapterFactory
	Run           func(context.Context, Execution) error
}

// Log adapters derive telemetry only; they cannot authorize effects or transition
// task lifecycle state. Factories create fresh state for each subprocess.
type OutputStream string

const (
	Stdout OutputStream = "stdout"
	Stderr OutputStream = "stderr"
)

type LogRecord struct {
	Stream     OutputStream
	Data       []byte
	Offset     int64 // Absolute offset in the retained, combined task log.
	ObservedAt time.Time
}
type AdapterContext struct {
	TaskID, NodeID, ProcessID domain.ID
	Phase                     string
}
type ActivityLog struct {
	ActivityID domain.ID
	Data       []byte
}
type LogUpdate struct {
	Progress   *domain.TaskProgress
	Activities []domain.Activity
	Logs       []ActivityLog
}
type LogAdapter interface {
	Consume(LogRecord) (LogUpdate, error)
	Flush() (LogUpdate, error)
}
type LogAdapterFactory struct {
	Name  string
	Match func(CommandLine) bool
	New   func(AdapterContext) (LogAdapter, error)
}
