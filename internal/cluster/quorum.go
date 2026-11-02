package cluster

import "errors"

// Topology counts stable physical hosts, not online clients or bootstrap peers.
type Topology struct {
	StableHosts      int  `json:"stableHosts"`
	Bootstrap        bool `json:"bootstrap"`
	Replicas         int  `json:"replicas"`
	CriticalReplicas int  `json:"criticalReplicas"`
}

func Plan(stableHosts int) (Topology, error) {
	if stableHosts < 1 {
		return Topology{}, errors.New("cluster requires a full-stable host")
	}
	plan := Topology{StableHosts: stableHosts, Bootstrap: stableHosts < 3, Replicas: 3, CriticalReplicas: 3}
	if stableHosts == 1 {
		plan.Replicas, plan.CriticalReplicas = 2, 2
	}
	if stableHosts >= 5 {
		plan.CriticalReplicas = 5
	}
	return plan, nil
}

// ValidateTransition protects the last stable member even for forced actions.
// A pending-to-full promotion still requires a successful deployment unless
// explicitly forced; checking that fact is the coordinator's responsibility.
func ValidateTransition(from, to string, stableHosts int) error {
	allowed := map[string][]string{
		"pending":     {"full", "removed"},
		"full":        {"full-stable", "pending", "removed"},
		"full-stable": {"full", "pending", "removed"},
	}
	if from == "full-stable" && stableHosts <= 1 {
		return errors.New("cannot remove or demote the last full-stable member")
	}
	for _, target := range allowed[from] {
		if target == to {
			return nil
		}
	}
	return errors.New("invalid membership transition")
}
