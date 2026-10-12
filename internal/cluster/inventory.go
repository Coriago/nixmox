package cluster

import (
	"encoding/json"
	"errors"
	"nixmox/internal/statefile"
	"os"
)

func (v *View) Load(path string) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var hosts []HostPayload
	if err = json.Unmarshal(b, &hosts); err != nil {
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, h := range hosts {
		h.Online = false
		v.hosts[h.NodeID] = h
	}
	return nil
}
func (v *View) Save(path string) error { return statefile.Write(path, v.List()) }
