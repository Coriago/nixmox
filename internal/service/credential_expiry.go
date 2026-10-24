package service

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	js "github.com/nats-io/nats.go/jetstream"
	"nixmox/internal/domain"
	"nixmox/internal/engine/identity"
	"nixmox/internal/engine/task"
)

// Fence expiry through the existing receipt store under the same authority lock
// as command claims. A missing receipt alone never proves work was not accepted.
func (c *ClusterNode) expireCredentialStage(ctx context.Context, pending identity.PendingSecret) error {
	if !time.Now().After(pending.Stage.ExpiresAt) {
		return nil
	}
	p := domain.CredentialImport{Name: pending.Ingress.Name, Kind: pending.Ingress.Kind, StorageNodeID: pending.Ingress.HolderID, StagingRef: pending.Stage.Reference}
	payload, err := json.Marshal(p)
	if err != nil {
		return err
	}
	command := domain.Command{ID: pending.Ingress.CommandID, Type: "credential.import", TargetID: pending.Ingress.HolderID, Payload: payload}
	hash, err := task.RequestHash(command)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if err = c.quorum(ctx); err != nil {
		c.mu.Unlock()
		return err
	}
	_, _, err = readRecord[domain.Envelope](ctx, c.transport.Store, recordKey("command", command.ID))
	if err == nil {
		c.mu.Unlock()
		return nil
	}
	if !errors.Is(err, js.ErrKeyNotFound) {
		c.mu.Unlock()
		return err
	}
	receipt, revision, err := readRecord[domain.AdmissionRecord](ctx, c.transport.Store, recordKey("receipt", command.ID))
	if errors.Is(err, js.ErrKeyNotFound) {
		receipt = domain.AdmissionRecord{SchemaVersion: 1, Envelope: domain.Envelope{SchemaVersion: 1, Command: command, RequestHash: hash, Scope: domain.ClusterScope, OwnerNodeID: c.keys.Identity.NodeID, ClusterID: &c.state.Cluster.ID}, Receipt: domain.Receipt{CommandID: command.ID, Admission: "rejected"}, Error: &domain.Error{Code: "STAGING_EXPIRED", Message: "Unclaimed credential staging expired; start a new import"}}
		_, err = writeRecord(ctx, c.transport.Store, recordKey("receipt", command.ID), revision, receipt)
	}
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if receipt.Error == nil || receipt.Error.Code != "STAGING_EXPIRED" {
		return nil
	}
	var result domain.Empty
	return c.credentialCall(ctx, pending.Ingress.HolderID, "credential.expire", credentialLocalRequest{CommandID: command.ID, StagingRef: pending.Stage.Reference}, &result)
}
