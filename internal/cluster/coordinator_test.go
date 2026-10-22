package cluster

import (
	"context"
	"encoding/json"
	"nixmox/internal/domain"
	"nixmox/internal/service"
	"testing"
	"time"
)

func TestCoordinatorTaskRetainsRemoteTargetAndOwnerBinding(t *testing.T) {
	op := service.Operation{Kind: "host.assign", Scope: "cluster", RunOnCoordinator: true, SafeRetry: true, Timeout: time.Second, Validate: func(c domain.Command) error { return nil }, Run: func(ctx context.Context, e *service.Execution) (any, error) {
		task, err := e.Snapshot()
		if err != nil {
			return nil, err
		}
		return map[string]string{"target": e.Command.TargetID, "owner": task.OwnerNodeID}, nil
	}}
	manager, authority := issuerFixture(t, op)
	leaf, target := nodeFixture(t, op)
	joinFixture(t, manager, authority, leaf, target)
	payload, _ := json.Marshal(map[string]string{"host_id": target.Identity().NodeID})
	cmd := domain.Command{ID: "cmd_assign_remote", Type: op.Kind, TargetID: target.Identity().NodeID, Payload: payload}
	receipt, err := authority.Admit(cmd)
	if err != nil {
		t.Fatal(err)
	}
	task := waitTask(t, authority, receipt.TaskID, "succeeded")
	if task.OwnerNodeID != authority.Identity().NodeID || task.TargetID != target.Identity().NodeID {
		t.Fatal("owner and target conflated", task)
	}
	if _, err = target.LocalTask(task.ID); err == nil {
		t.Fatal("metadata mutation ran on pending target")
	}
	p, _, err := manager.dispatcher.read(cmd.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Prepared.ExecutionNode() != task.OwnerNodeID || p.Prepared.TargetEpoch != target.Identity().MembershipEpoch || p.Prepared.ExecutionEpoch() != authority.Identity().MembershipEpoch {
		t.Fatal("execution epochs not pinned", p)
	}
	sample := domain.HostSample{SchemaVersion: 1, HostID: target.Identity().NodeID, Name: "observed-target", System: "x86_64-linux", Environment: "bootstrap", ObservedAt: time.Now().UTC(), Freshness: "live"}
	if err = leaf.PublishHost(sample); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	var observation domain.HostObservation
	for time.Now().Before(deadline) {
		entry, err := manager.stores.State.Get("observation." + sample.HostID)
		if err == nil && json.Unmarshal(entry.Value(), &observation) == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if observation.Sample.Name != sample.Name || observation.Epoch != target.Identity().MembershipEpoch {
		t.Fatal("authenticated inventory was not projected", observation)
	}
	sample.HostID = authority.Identity().NodeID
	if err = leaf.PublishHost(sample); err == nil {
		t.Fatal("owner spoof accepted")
	}
}
