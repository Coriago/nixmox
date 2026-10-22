package cluster

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"nixmox/internal/domain"
)

type queryRequest struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}
type statusReply struct {
	Status *domain.ClusterStatus `json:"status,omitempty"`
	Error  *domain.Error         `json:"error,omitempty"`
}

// Queries never mutate state and are carried only on the node's allowed query
// and reply subjects. A pending leaf cannot subscribe to arbitrary inboxes.
func (m *Manager) startQueries() error {
	if m.querySubscription != nil || m.stores == nil {
		return nil
	}
	nc, kv, cluster := m.broker.Client, m.stores.State, m.membership.ClusterID
	prefix := "nx." + cluster + ".query."
	sub, err := nc.Subscribe(prefix+"*", func(msg *nats.Msg) {
		if len(msg.Data) > 4096 {
			return
		}
		node := strings.TrimPrefix(msg.Subject, prefix)
		var request queryRequest
		if !domain.SafeID.MatchString(node) || domain.StrictJSON(msg.Data, &request) != nil || !domain.SafeID.MatchString(request.ID) || msg.Reply != "nx."+cluster+".reply."+node+"."+request.ID {
			return
		}
		entry, err := kv.Get("member." + node)
		if err != nil {
			return
		}
		var member Member
		if json.Unmarshal(entry.Value(), &member) != nil || member.NodeID != node || (member.State != "pending" && member.State != "full") {
			return
		}
		response := statusReply{}
		if request.Kind == "cluster.status" {
			status, err := m.Status()
			if err != nil {
				response.Error = unavailable()
			} else {
				response.Status = &status
			}
		} else {
			response.Error = domain.Err("UNSUPPORTED_QUERY", "This query is not available to the node.", 422)
		}
		data, _ := json.Marshal(response)
		_ = nc.Publish(msg.Reply, data)
	})
	if err != nil {
		return err
	}
	if err = sub.SetPendingLimits(64, 256<<10); err != nil {
		sub.Unsubscribe()
		return err
	}
	m.querySubscription = sub
	return nc.FlushTimeout(time.Second)
}
func pendingStatus(nc *nats.Conn, membership Membership) (domain.ClusterStatus, error) {
	request := queryRequest{ID: domain.NewID("q_"), Kind: "cluster.status"}
	reply := "nx." + membership.ClusterID + ".reply." + membership.NodeID + "." + request.ID
	sub, err := nc.SubscribeSync(reply)
	if err != nil {
		return domain.ClusterStatus{}, unavailable()
	}
	defer sub.Unsubscribe()
	if err = sub.SetPendingLimits(1, 16<<10); err != nil {
		return domain.ClusterStatus{}, unavailable()
	}
	data, _ := json.Marshal(request)
	if err = nc.PublishRequest("nx."+membership.ClusterID+".query."+membership.NodeID, reply, data); err != nil {
		return domain.ClusterStatus{}, unavailable()
	}
	msg, err := sub.NextMsg(4 * time.Second)
	if err != nil {
		return domain.ClusterStatus{}, unavailable()
	}
	var response statusReply
	if len(msg.Data) > 16<<10 || domain.StrictJSON(msg.Data, &response) != nil || response.Error != nil || response.Status == nil || response.Status.ClusterID == nil || *response.Status.ClusterID != membership.ClusterID {
		return domain.ClusterStatus{}, unavailable()
	}
	status := *response.Status
	status.Membership = "pending"
	status.BrokerRole = "leaf"
	return status, nil
}
