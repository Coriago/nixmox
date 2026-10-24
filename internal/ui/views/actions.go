package views

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"nixmox/internal/domain"
	"nixmox/internal/ui/components/button"
	"nixmox/internal/ui/components/confirmation"
)

func (m *Model) actionHint(a Action) string {
	if !m.connected {
		return "Backend is not connected"
	}
	if m.busy {
		return "A command is being submitted"
	}
	if m.uncertain != "" {
		return "Reconcile the previous command receipt first"
	}
	switch a.Kind {
	case "credential-check", "credential-remove":
		credential := m.credential(a.ID)
		if credential == nil || m.identity.ClusterID == nil {
			return "Credential metadata unavailable"
		}
		if a.Kind == "credential-check" && (credential.Kind == "git-http-token" || credential.Kind == "git-ssh-key") && len(m.Repositories) == 0 {
			return "Import a repository before checking Git access"
		}
		if a.Kind == "credential-remove" {
			if len(credential.Scope.RepositoryIDs) != 0 {
				return "Remove repository bindings first"
			}
			for _, replica := range credential.Replicas {
				if replica.Source == "runtime_file" {
					return "Owned by Nix configuration"
				}
			}
		}
	case "repository-fetch":
		if m.repository(a.ID) == nil || m.identity.ClusterID == nil {
			return "Repository metadata unavailable"
		}
	case "refresh", "network-check", "shutdown":
		h := m.Host(a.ID)
		if h == nil || h.Reachability != "online" {
			return "Host is not reachable"
		}
		if a.Kind == "network-check" {
			if a.ID != string(m.identity.NodeID) {
				return "Network check is local to the connected daemon"
			}
			return ""
		}
		if m.identity.ClusterID == nil {
			return "Requires cluster membership"
		}
		if a.Kind == "shutdown" && (!h.Source.Capabilities.Power || h.Source.MembershipEpoch == nil) {
			return "Power capability or membership epoch unavailable"
		}
	case "cancel":
		t := m.Task(a.ID)
		if t == nil || !t.Source.Cancellation.Allowed {
			return "Task cannot be cancelled in its current state"
		}
		if t.Source.Scope == domain.NodeLocal && t.Source.OwnerNodeID != m.identity.NodeID {
			return "Connect to the task owner to cancel node-local work"
		}
	case "approve", "reject", "add":
		d := m.discovery(a.ID)
		if d == nil || time.Now().After(d.ExpiresAt) {
			return "Discovery expired; refresh the observation"
		}
		if m.identity.ClusterID == nil {
			return "Requires cluster membership"
		}
		if (a.Kind == "add") == d.Requested {
			return "Join-request state changed"
		}
		if d.NodeID == "" || d.Fingerprint == "" {
			return "Exact node identity is unavailable"
		}
	default:
		return "Action unavailable"
	}
	return ""
}
func (m *Model) resourceControl(label, kind, id string) button.Model {
	a := Action{Kind: kind, ID: id}
	hint := m.actionHint(a)
	tone := "primary"
	switch kind {
	case "shutdown", "cancel", "reject", "credential-remove":
		tone = "danger"
	case "add", "approve":
		tone = "success"
	}
	return button.Model{Label: label, Action: a, Disabled: hint != "", Hint: hint, Tone: tone}
}
func (m *Model) prepare(a Action) {
	if a.ID == "" {
		if a.Kind == "cancel" {
			a.ID = m.TaskID
		} else {
			a.ID = m.HostID
		}
	}
	if hint := m.actionHint(a); hint != "" {
		m.Notice = hint
		return
	}
	if a.Kind == "credential-check" {
		if credential := m.credential(a.ID); credential != nil && (credential.Kind == "git-http-token" || credential.Kind == "git-ssh-key") {
			m.openSourceForm("credential-check", a.ID)
			return
		}
	}
	command := domain.Command{ID: domain.ID("cmd_" + uuid.NewString()), TargetID: m.identity.NodeID}
	dialog := confirmation.Model{Confirm: "Confirm", Accent: m.resourceControl("", a.Kind, a.ID).Tone}
	var payload any
	switch a.Kind {
	case "repository-fetch":
		command.Type = "repository.fetch"
		payload = domain.RepositoryTarget{RepositoryID: domain.ID(a.ID)}
	case "credential-check":
		credential := m.credential(a.ID)
		command.Type = "credential.check"
		p := domain.CredentialCheckPayload{CredentialID: credential.ID}
		if credential.Kind == "git-http-token" || credential.Kind == "git-ssh-key" {
			p.RepositoryID = &credential.Scope.RepositoryIDs[0]
		}
		payload = p
	case "credential-remove":
		credential := m.credential(a.ID)
		command.Type = "credential.remove"
		command.ExpectedVersions = domain.Versions{"credential:" + a.ID: credential.Version}
		payload = domain.CredentialTarget{CredentialID: credential.ID}
		dialog.Title, dialog.Target, dialog.Confirm = "Remove credential?", credential.Name+" · "+a.ID, "Remove"
		dialog.Body = "Delete managed copies from all holders. Unreachable holders block completion. This does not revoke the credential at its provider."
	case "network-check":
		command.Type = "network.check"
		payload = domain.Empty{}
	case "refresh":
		command.Type, command.TargetID = "host.refresh", domain.ID(a.ID)
		payload = domain.HostTarget{HostID: command.TargetID}
	case "shutdown":
		h := m.Host(a.ID)
		command.Type, command.TargetID = "host.shutdown", h.Source.ID
		command.ExpectedVersions = domain.Versions{"host:" + a.ID: h.Source.Version}
		payload = domain.HostPower{HostID: h.Source.ID, MembershipEpoch: *h.Source.MembershipEpoch, ConfirmHost: string(h.Source.ID)}
		dialog.Title, dialog.Target, dialog.Confirm = "Shut down this host?", h.Name+" · "+h.ID, "Shut down"
		dialog.Body = "Schedule shutdown on this exact host. Connectivity will be lost. Task completion requires observed evidence, not merely command acceptance."
	case "cancel":
		t := m.Task(a.ID)
		command.Type, command.TargetID = "task.cancel", t.Source.ID
		command.ExpectedVersions = domain.Versions{"task:" + a.ID: t.Source.Version}
		payload = domain.TaskTarget{TaskID: t.Source.ID}
		dialog.Title, dialog.Target, dialog.Confirm = "Cancel this task?", t.Name+" · "+t.ID, "Cancel task"
		dialog.Body = "Request cancellation of this task at the reviewed lifecycle version. Completed effects are not undone."
	case "approve", "reject", "add":
		d := m.discovery(a.ID)
		dialog.Target = d.Name + " · " + string(d.NodeID)
		dialog.Body = fmt.Sprintf("Address: %s · key: %s. ", unknown(d.Address), d.Fingerprint)
		if a.Kind == "add" {
			command.Type = "cluster.add"
			payload = domain.ClusterAdd{DiscoveryID: domain.ID(d.ID), ExpectedNodeID: d.NodeID, ExpectedKeyFingerprint: domain.Digest(d.Fingerprint)}
			dialog.Title, dialog.Confirm = "Add discovered host?", "Add"
		} else {
			command.Type = "cluster.request." + a.Kind
			payload = domain.JoinDecisionPayload{RequestID: d.RequestID}
			command.ExpectedVersions = domain.Versions{"join_request:" + string(d.RequestID): d.Version}
			dialog.Title, dialog.Confirm = "Approve join request?", "Approve"
		}
		if a.Kind == "reject" {
			dialog.Title, dialog.Confirm = "Reject join request?", "Reject"
			dialog.Body += "Reject without granting membership."
		} else {
			dialog.Body += "Grant pending membership only, never full membership or voting rights."
		}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		m.Notice = err.Error()
		return
	}
	command.Payload = data
	if a.Kind == "refresh" || a.Kind == "network-check" || a.Kind == "credential-check" || a.Kind == "repository-fetch" {
		m.command = &command
		m.busy = true
		return
	}
	m.review = &command
	m.Modal = &dialog
}

// TakeCommand transfers one immutable, reviewed command to the HTTP worker.
func (m *Model) TakeCommand() *domain.Command { command := m.command; m.command = nil; return command }
func (m *Model) Selection() Selection {
	return Selection{Host: domain.ID(m.HostID), Task: domain.ID(m.TaskID)}
}
