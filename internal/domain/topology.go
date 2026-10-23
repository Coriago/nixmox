package domain

type RoutePeer struct {
	ForcedBy    ID     `json:"forced_by,omitempty"`
	NodeID      ID     `json:"node_id"`
	Epoch       uint64 `json:"epoch"`
	Address     string `json:"address"`
	Certificate string `json:"certificate"`
}
type Topology struct {
	ClusterID ID               `json:"cluster_id"`
	Version   Version          `json:"version"`
	Peers     map[ID]RoutePeer `json:"peers"`
	Replicas  int              `json:"replicas"`
}
type SignedTopology struct {
	Topology  Topology `json:"topology"`
	Signature string   `json:"signature"`
}
type PowerReservation struct {
	CommandID ID     `json:"command_id"`
	HostID    ID     `json:"host_id"`
	Epoch     uint64 `json:"epoch"`
	BootID    string `json:"boot_id"`
}
type PowerInfo struct {
	NodeID  ID     `json:"node_id"`
	Name    string `json:"name"`
	Epoch   uint64 `json:"epoch"`
	Enabled bool   `json:"enabled"`
	BootID  string `json:"boot_id"`
}
