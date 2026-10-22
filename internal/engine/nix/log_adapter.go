package nix

import (
	"path/filepath"

	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/engine/task"
)

// NixLogAdapter is ordinary task middleware. Tasks/phases opt in; neither the
// scheduler nor process runner knows anything about Nix's event protocol.
func LogAdapter() engine.LogAdapterFactory {
	return engine.LogAdapterFactory{
		Name:  "nix.internal-json",
		Match: func(plan engine.CommandLine) bool { return filepath.Base(plan.Program) == "nix" },
		New: func(ctx engine.AdapterContext) (engine.LogAdapter, error) {
			parser := NewProgress(ctx.TaskID, ctx.NodeID)
			var logs []engine.ActivityLog
			parser.Log = func(id domain.ID, data []byte) {
				logs = append(logs, engine.ActivityLog{ActivityID: id, Data: append([]byte(nil), data...)})
			}
			var version domain.Version
			return &task.LineAdapter{Parse: func(record engine.LogRecord) (engine.LogUpdate, error) {
				if record.Stream != engine.Stderr {
					return engine.LogUpdate{}, nil
				}
				activities, err := parser.Consume(record.Data)
				progress := parser.Snapshot()
				update := engine.LogUpdate{Activities: activities, Logs: logs}
				logs = nil
				if progress.Version != version {
					update.Progress = &progress
					version = progress.Version
				}
				return update, err
			}}, nil
		},
	}
}
