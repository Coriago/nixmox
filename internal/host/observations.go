package host

import (
	"context"
	"errors"

	"github.com/nats-io/nats.go"
	"nixmox/internal/cluster"
)

type ObservationRequest struct {
	Kind          string `json:"kind"`
	NoFileSystems bool   `json:"noFileSystems"`
}

type ObservationResponse = cluster.Response

// ObserveLocal accepts only named, read-only inventory operations. Remote callers
// cannot supply a command, filesystem destination or another host identity.
func ObserveLocal(ctx context.Context, process Process, request ObservationRequest) (any, error) {
	switch request.Kind {
	case "disks":
		return process.Disks(ctx)
	case "hardware":
		return process.Hardware(ctx, request.NoFileSystems)
	default:
		return nil, errors.New("unsupported host observation")
	}
}

func Observe(ctx context.Context, nc *nats.Conn, nodeID string, request ObservationRequest, output any) error {
	return cluster.RequestJSON(ctx, nc, "host."+nodeID+".observe", request, output)
}
