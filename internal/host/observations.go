package host

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/nats-io/nats.go"
	"nixmox/internal/cluster"
)

type ObservationRequest struct {
	Kind          string `json:"kind"`
	NoFileSystems bool   `json:"noFileSystems"`
}

type ObservationResponse struct {
	Output json.RawMessage `json:"output,omitempty"`
	Error  string          `json:"error,omitempty"`
}

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
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	message, err := cluster.Request(ctx, nc, "host."+nodeID+".observe", data)
	if err != nil {
		return err
	}
	var response ObservationResponse
	if err = json.Unmarshal(message.Data, &response); err != nil {
		return err
	}
	if response.Error != "" {
		return errors.New(response.Error)
	}
	return json.Unmarshal(response.Output, output)
}
