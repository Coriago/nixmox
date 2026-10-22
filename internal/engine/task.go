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
type Resource struct {
	Key      string
	Capacity int
}
type Fence interface {
	Token() uint64
	Release(context.Context) error // Uncertain effects keep ownership until reconciled.
}
type Exclusions interface {
	Acquire(context.Context, domain.ID, []Resource) (Fence, error)
}
type Phase struct {
	Name          string
	Interruptible bool
	Resources     []Resource
	Adapters      []LogAdapterFactory
	Run           func(context.Context, Execution) error
	Reconcile     func(context.Context, Execution) error
}
type Execution interface {
	Task() domain.Task
	// Intent precedes effects; Observed follows independently verified evidence.
	Intent(context.Context, domain.Checkpoint) error
	Observed(context.Context, domain.Checkpoint) error
	Block(context.Context, domain.Review) error
	Log() io.Writer
	// Runner decorates any command adapter with task-owned persistence and parsing.
	Runner(Runner) Runner
	Progress(context.Context, domain.TaskProgress) error
}
type TaskDefinition struct {
	Kind     string
	Adapters []LogAdapterFactory
	Phases   []Phase
}
type Tasks interface {
	Admit(context.Context, domain.Envelope) (domain.Receipt, error)
	Run(context.Context) error
	Recover(context.Context) error
	Cancel(context.Context, domain.ID, domain.Version) error
	Retry(context.Context, domain.ID, domain.Version) (domain.Receipt, error)
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
	Offset     int64 // Absolute offset in the persisted, combined task log.
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
