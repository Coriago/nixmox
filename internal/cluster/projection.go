package cluster

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"nixmox/internal/domain"
)

type queryRequest struct {
	ID       string          `json:"id"`
	Kind     string          `json:"kind"`
	Epoch    int             `json:"membership_epoch"`
	Payload  json.RawMessage `json:"payload,omitempty"`
	Deadline time.Time       `json:"deadline"`
}
type statusReply struct {
	Status      *domain.ClusterStatus `json:"status,omitempty"`
	Error       *domain.Error         `json:"error,omitempty"`
	Receipt     *domain.Receipt       `json:"receipt,omitempty"`
	ErrorStatus int                   `json:"error_status,omitempty"`
}

// Pending gateway mutations enter the normal command service and JetStream;
// receipt of a Core request never authorizes execution by itself.
func (m *Manager) startQueries() error {
	if m.querySubscription != nil || m.stores == nil {
		return nil
	}
	nc, cluster := m.broker.Client, m.membership.ClusterID
	prefix := "nx." + cluster + ".query."
	sub, err := nc.Subscribe(prefix+"*", func(msg *nats.Msg) {
		if len(msg.Data) > 64<<10 {
			return
		}
		node := strings.TrimPrefix(msg.Subject, prefix)
		var request queryRequest
		if !domain.SafeID.MatchString(node) || domain.StrictJSON(msg.Data, &request) != nil || !domain.SafeID.MatchString(request.ID) || time.Now().After(request.Deadline) || msg.Reply != "nx."+cluster+".reply."+node+"."+request.ID {
			return
		}
		m.mu.RLock()
		if m.closed {
			m.mu.RUnlock()
			return
		}
		select {
		case m.queryIngress <- struct{}{}:
		default:
			m.mu.RUnlock()
			return
		}
		m.queryWorkers.Add(1)
		m.mu.RUnlock()
		go func() {
			defer m.queryWorkers.Done()
			defer func() { <-m.queryIngress }()
			response := m.peerRequest(node, request)
			data, _ := json.Marshal(response)
			if len(data) <= 64<<10 {
				_ = nc.Publish(msg.Reply, data)
			}
		}()
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
	response, err := pendingQuery(nc, membership, queryRequest{Kind: "cluster.status"}, 4*time.Second)
	if err != nil || response.Status == nil || response.Status.ClusterID == nil || *response.Status.ClusterID != membership.ClusterID {
		return domain.ClusterStatus{}, unavailable()
	}
	status := *response.Status
	status.Membership = "pending"
	status.BrokerRole = "leaf"
	return status, nil
}
func pendingQuery(nc *nats.Conn, membership Membership, request queryRequest, timeout time.Duration) (statusReply, error) {
	request.ID = domain.NewID("q_")
	request.Epoch = membership.Epoch
	request.Deadline = time.Now().Add(timeout)
	reply := "nx." + membership.ClusterID + ".reply." + membership.NodeID + "." + request.ID
	sub, err := nc.SubscribeSync(reply)
	if err != nil {
		return statusReply{}, unavailable()
	}
	defer sub.Unsubscribe()
	if err = sub.SetPendingLimits(1, 64<<10); err != nil {
		return statusReply{}, unavailable()
	}
	data, _ := json.Marshal(request)
	if len(data) > 64<<10 {
		return statusReply{}, domain.Err("INVALID_PAYLOAD", "Scoped gateway request exceeds 64 KiB.", 422)
	}
	if err = nc.PublishRequest("nx."+membership.ClusterID+".query."+membership.NodeID, reply, data); err != nil {
		return statusReply{}, unavailable()
	}
	msg, err := sub.NextMsg(timeout)
	if err != nil {
		return statusReply{}, domain.Err("OUTCOME_UNKNOWN", "Gateway response is unavailable; query the original command ID before retrying.", 503)
	}
	var response statusReply
	if len(msg.Data) > 64<<10 || domain.StrictJSON(msg.Data, &response) != nil {
		return statusReply{}, unavailable()
	}
	if response.Error != nil {
		if response.ErrorStatus < 400 || response.ErrorStatus > 599 {
			return statusReply{}, unavailable()
		}
		response.Error.Status = response.ErrorStatus
		return response, response.Error
	}
	return response, nil
}
