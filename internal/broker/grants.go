package broker

import (
	"errors"
	"strings"

	"nixmox/internal/domain"
)

func validateGrants(c Config) error {
	if len(c.Leaves) > 256 || len(c.FullBrokers) > 256 {
		return errors.New("broker grant limit exceeded")
	}
	for key, g := range c.Leaves {
		if len(key) != 64 || !domain.SafeID.MatchString(g.NodeID) || len(g.Tasks) > 64 || len(g.Acknowledgments) > 64 {
			return errors.New("invalid leaf grant")
		}
		for _, id := range g.Tasks {
			if !domain.SafeID.MatchString(id) {
				return errors.New("invalid task grant")
			}
		}
		for _, ack := range g.Acknowledgments {
			if !strings.HasPrefix(ack, "$JS.ACK.nx_commands.") || len(ack) > 512 || strings.ContainsAny(ack, "*> \r\n\t") {
				return errors.New("invalid acknowledgment grant")
			}
		}
	}
	return nil
}
func leafSubjects(cluster string, g LeafGrant) (publish, subscribe []string) {
	prefix := "nx." + cluster + "."
	for _, kind := range []string{"heartbeat", "activity", "result", "observation", "query"} {
		publish = append(publish, prefix+kind+"."+g.NodeID)
	}
	subscribe = []string{prefix + "command." + g.NodeID, prefix + "reply." + g.NodeID + ".>", prefix + "view." + g.NodeID + ".>"}
	for _, task := range g.Tasks {
		publish = append(publish, prefix+"log."+task, prefix+"progress."+task)
	}
	publish = append(publish, g.Acknowledgments...)
	return publish, subscribe
}
