package host

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
)

type Disk struct {
	Device   string `json:"device"`
	Size     uint64 `json:"size"`
	Model    string `json:"model"`
	Serial   string `json:"serial"`
	ReadOnly bool   `json:"readOnly"`
	Mounted  bool   `json:"mounted"`
}

// Disks returns whole disks with stable by-id paths where available. It never
// selects an installation target or treats a partition as a whole disk.
func (p Process) Disks(ctx context.Context) ([]Disk, error) {
	data, err := p.Run(ctx, "", nil, "lsblk", "--json", "--bytes", "--output", "NAME,PATH,SIZE,TYPE,FSTYPE,MOUNTPOINTS,MODEL,SERIAL,RM,RO")
	if err != nil {
		return nil, err
	}
	var inventory struct {
		Devices []blockDevice `json:"blockdevices"`
	}
	if err = json.Unmarshal(data, &inventory); err != nil {
		return nil, err
	}
	stable := map[string]string{}
	entries, err := os.ReadDir("/dev/disk/by-id")
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	for _, entry := range entries {
		path := filepath.Join("/dev/disk/by-id", entry.Name())
		target, err := filepath.EvalSymlinks(path)
		if err == nil && stable[target] == "" {
			stable[target] = path
		}
	}
	disks := []Disk{}
	for _, device := range inventory.Devices {
		if device.Type != "disk" {
			continue
		}
		path := device.Path
		if stable[path] != "" {
			path = stable[path]
		}
		disks = append(disks, Disk{Device: path, Size: device.Size, Model: device.Model, Serial: device.Serial, ReadOnly: device.ReadOnly, Mounted: device.mounted()})
	}
	return disks, nil
}

type blockDevice struct {
	Path        string        `json:"path"`
	Type        string        `json:"type"`
	Size        uint64        `json:"size"`
	Model       string        `json:"model"`
	Serial      string        `json:"serial"`
	ReadOnly    bool          `json:"ro"`
	MountPoints []*string     `json:"mountpoints"`
	Children    []blockDevice `json:"children"`
}

func (d blockDevice) mounted() bool {
	for _, mount := range d.MountPoints {
		if mount != nil && *mount != "" {
			return true
		}
	}
	for _, child := range d.Children {
		if child.mounted() {
			return true
		}
	}
	return false
}

func (p Process) Hardware(ctx context.Context, noFileSystems bool) (string, error) {
	args := []string{"--show-hardware-config"}
	if noFileSystems {
		args = append(args, "--no-filesystems")
	}
	data, err := p.Run(ctx, "", nil, "nixos-generate-config", args...)
	return string(data), err
}
