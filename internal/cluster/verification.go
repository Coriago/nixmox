package cluster

import (
	"context"
	"nixmox/internal/domain"
	"nixmox/internal/state"
	"time"
)

// VerifyLocalDeployment receives evidence collected by the local deployment
// service after observing its exact candidate. No console payload supplies it.
func (m *Manager) VerifyLocalDeployment(ctx context.Context, candidate domain.Candidate, deploymentID string, sample domain.HostSample) error {
	id := m.Identity()
	if sample.HostID != id.NodeID || sample.Environment != "installed" || sample.RunningOutput == nil || *sample.RunningOutput != candidate.OutputPath || sample.Freshness != "live" || time.Since(sample.ObservedAt) > 15*time.Second || sample.ObservedAt.After(time.Now().Add(time.Second)) || candidate.NarHash == "" || candidate.Source.Commit == "" {
		return domain.Err("VERIFICATION_REQUIRED", "Full membership requires exact output evidence from the permanent installed agent.", 409)
	}
	member, err := m.member(id.NodeID)
	if err != nil {
		return err
	}
	if member.Epoch != id.MembershipEpoch || member.State != "pending" && member.State != "full" {
		return domain.Err("MEMBERSHIP_CHANGED", "Deployment evidence belongs to another membership epoch.", 409)
	}
	st := state.Store{Acquire: m.DurableStores}
	var old domain.ManagedEvidence
	revision, err := st.Get(ctx, "managed."+id.NodeID, &old)
	if err != nil && !missingRecord(err) {
		return err
	}
	evidence := domain.ManagedEvidence{HostID: id.NodeID, Epoch: id.MembershipEpoch, CandidateID: candidate.ID, DeploymentID: deploymentID, Source: candidate.Source, OutputPath: candidate.OutputPath, NarHash: candidate.NarHash, VerifiedAt: sample.ObservedAt, Environment: sample.Environment}
	if _, err = st.CAS(ctx, "managed."+id.NodeID, evidence, revision); err != nil {
		return err
	}
	var current Member
	revision, err = st.Get(ctx, "member."+id.NodeID, &current)
	if err != nil && !missingRecord(err) {
		return err
	}
	if err == nil {
		member = current
	}
	if member.State == "full" {
		return nil
	}
	if member.State != "pending" || member.Epoch != id.MembershipEpoch {
		return domain.Err("MEMBERSHIP_CHANGED", "Membership is no longer eligible for promotion.", 409)
	}
	// This executor owns the durable control plane. Other nodes require the
	// authenticated topology transition, never a leaf-local identity edit.
	member.State = "full"
	member.BrokerRole = "full"
	member.Version++
	_, err = st.CAS(ctx, "member."+id.NodeID, member, revision)
	return err
}
func missingRecord(err error) bool { e, ok := err.(*domain.Error); return ok && e.Code == "NOT_FOUND" }
