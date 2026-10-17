package addhost

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Observation is collected on the selected machine, never supplied by the browser.
type Observation struct {
	Hardware string `json:"hardware"`
	Disks    []Disk `json:"disks"`
}

func Inspect(ctx context.Context) (Observation, error) {
	return InspectRetry(ctx, "")
}

// Retry may include mappings and /mnt filesystems created by the previous
// attempt on the already confirmed drive. Live-system mounts remain excluded.
func InspectRetry(ctx context.Context, retryDrive string) (Observation, error) {
	data, err := exec.CommandContext(ctx, "lsblk", "--json", "--tree", "--bytes", "--output", "PATH,TYPE,FSTYPE,MODEL,SERIAL,SIZE,RO,MOUNTPOINTS").Output()
	if err != nil {
		return Observation{}, fmt.Errorf("read disk inventory: %w", err)
	}
	disks, err := parseDisks(data, retryDrive)
	if err != nil {
		return Observation{}, err
	}
	hardware, err := exec.CommandContext(ctx, "nixos-generate-config", "--show-hardware-config", "--no-filesystems").Output()
	if err != nil {
		return Observation{}, fmt.Errorf("generate hardware configuration: %w", err)
	}
	return Observation{Hardware: string(hardware), Disks: disks}, nil
}

// Exclude mounted disks and active mappings, including the live installer
// filesystem, swap, and their parent disks. Never offer partitions as drives.
func ParseDisks(data []byte) ([]Disk, error) {
	return parseDisks(data, "")
}
func parseDisks(data []byte, retryDrive string) ([]Disk, error) {
	type block struct {
		Path     string            `json:"path"`
		Type     string            `json:"type"`
		FSType   string            `json:"fstype"`
		Model    string            `json:"model"`
		Serial   string            `json:"serial"`
		Size     uint64            `json:"size"`
		RO       bool              `json:"ro"`
		Mounts   []*string         `json:"mountpoints"`
		Children []json.RawMessage `json:"children"`
	}
	var busy func(block, bool) (bool, error)
	busy = func(b block, retry bool) (bool, error) {
		if b.RO || b.FSType == "iso9660" {
			return true, nil
		}
		for _, m := range b.Mounts {
			if m != nil && *m != "" {
				if retry && (*m == "/mnt" || strings.HasPrefix(*m, "/mnt/") || *m == "[SWAP]") {
					continue
				}
				return true, nil
			}
		}
		for _, raw := range b.Children {
			var child block
			if err := json.Unmarshal(raw, &child); err != nil {
				return false, err
			}
			if child.Type != "part" && !retry {
				return true, nil
			}
			used, err := busy(child, retry)
			if err != nil || used {
				return used, err
			}
		}
		return false, nil
	}
	var tree struct {
		Devices []block `json:"blockdevices"`
	}
	if err := json.Unmarshal(data, &tree); err != nil {
		return nil, err
	}
	disks := []Disk{}
	for _, b := range tree.Devices {
		if b.Type != "disk" || b.Size == 0 {
			continue
		}
		used, err := busy(b, retryDrive != "" && b.Path == retryDrive)
		if err != nil {
			return nil, err
		}
		if used {
			continue
		}
		disks = append(disks, Disk{Path: b.Path, Whole: true, Model: b.Model, Serial: b.Serial, Size: b.Size})
	}
	return disks, nil
}
