package host

import (
	"context"
	"encoding/json"
	"errors"
)

// Disks returns the local host's disk inventory; dispatch belongs to the caller.
func (p Process) Disks(ctx context.Context) (json.RawMessage, error) {
	data, err := p.Run(ctx, "", nil, "lsblk", "--json", "--bytes", "--output", "NAME,PATH,SIZE,TYPE,FSTYPE,MOUNTPOINTS,MODEL,SERIAL,RM,RO")
	if err != nil {
		return nil, err
	}
	if !json.Valid(data) {
		return nil, errors.New("invalid lsblk JSON")
	}
	return data, nil
}

func (p Process) Hardware(ctx context.Context) (string, error) {
	data, err := p.Run(ctx, "", nil, "nixos-generate-config", "--show-hardware-config", "--no-filesystems")
	return string(data), err
}
