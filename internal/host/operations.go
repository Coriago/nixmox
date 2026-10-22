package host

import (
	"context"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/service"
)

func Operations(inspector Inspector) []service.Operation {
	run := func(ctx context.Context, e *service.Execution) (any, error) {
		if err := e.Checkpoint("collecting", struct{}{}, true); err != nil {
			return nil, err
		}
		return inspector.Inspect(ctx, e.Command.TargetID)
	}
	return []service.Operation{{Kind: "host.refresh", Scope: "cluster", Timeout: 15 * time.Second, SafeRetry: true,
		Validate: func(c domain.Command) error {
			var p struct {
				HostID string `json:"host_id"`
			}
			if domain.StrictJSON(c.Payload, &p) != nil || p.HostID != c.TargetID || len(c.ExpectedVersions) != 0 {
				return domain.Err("INVALID_PAYLOAD", "host.refresh requires its target host_id and no expected versions.", 422)
			}
			return nil
		}, Run: run, Reconcile: run}}
}
