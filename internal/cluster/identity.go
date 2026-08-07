package cluster

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// nodeIdentity is the persisted cluster identity at {dataDir}/node.json.
// nodeID equals the hostname in the POC (join rejects hostname collisions).
type nodeIdentity struct {
	NodeID      string   `json:"node_id"`
	Hostname    string   `json:"hostname"`
	ClusterName string   `json:"cluster_name"`
	Routes      []string `json:"routes"`
}

func loadIdentity(dataDir string) (nodeIdentity, error) {
	var id nodeIdentity
	data, err := os.ReadFile(filepath.Join(dataDir, "node.json"))
	if errors.Is(err, os.ErrNotExist) {
		return id, nil
	}
	if err != nil {
		return id, err
	}
	return id, json.Unmarshal(data, &id)
}

func saveIdentity(dataDir string, id nodeIdentity) error {
	data, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, "node.json"), data, 0o640)
}
