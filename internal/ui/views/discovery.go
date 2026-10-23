package views

import (
	"cmp"
	"fmt"
	"slices"

	"nixmox/internal/ui"
	"nixmox/internal/ui/components/button"
	"nixmox/internal/ui/components/confirmation"
	"nixmox/internal/ui/components/status"
	"nixmox/internal/ui/components/table"
)

// Discovery is a mock observation, distinct from enrollment and membership.
type Discovery struct {
	ID, Name, Address, Fingerprint, Decision string
	Requested                                bool
}

func (m *Model) discovery(id string) *Discovery {
	return ui.Find(m.Discoveries, func(d Discovery) bool { return d.ID == id })
}
func (m *Model) reviewDiscovery(a Action) {
	d := m.discovery(a.ID)
	if d == nil || m.Host(d.ID) != nil {
		return
	}
	if (a.Kind == "approve" || a.Kind == "reject") && !d.Requested {
		return
	}
	if a.Kind == "add" && d.Requested {
		return
	}
	title, confirm := "Add discovered host?", "Simulate add"
	if a.Kind == "approve" {
		title, confirm = "Approve join request?", "Simulate approval"
	}
	if a.Kind == "reject" {
		title, confirm = "Reject join request?", "Simulate rejection"
	}
	body := fmt.Sprintf("Mock identity n_%s · %s · key %s. ", d.ID, d.Address, d.Fingerprint)
	if a.Kind == "reject" {
		body += "Reject this request without granting membership or broker access."
	} else {
		body += "Grant pending membership only, not full membership or voting rights."
	}
	m.Modal = &confirmation.Model{Title: title, Target: d.Name, Body: body, Confirm: confirm, Action: a.Kind, ResourceID: d.ID, Fingerprint: d.Fingerprint}
}
func (m *Model) decideDiscovery(c confirmation.Model) {
	d := m.discovery(c.ResourceID)
	if d == nil || m.Host(d.ID) != nil || d.Fingerprint != c.Fingerprint {
		m.Notice = "Discovery changed; reopen the review."
		return
	}
	if (c.Action == "approve" || c.Action == "reject") && !d.Requested || c.Action == "add" && d.Requested {
		m.Notice = "Request changed; reopen the review."
		return
	}
	if c.Action == "reject" {
		d.Requested = false
		d.Decision = "Rejected"
		if t := m.Task("join-" + d.ID); t != nil && t.State == "blocked" {
			t.State = "failed"
			t.Phase = "Request rejected"
			t.Result = "No membership or broker access granted."
		}
	} else {
		m.Hosts = append(m.Hosts, Host{ID: d.ID, Name: d.Name, Address: d.Address, Reachability: "unknown", Membership: "pending", Role: "Non-voting leaf", Memory: "Unknown"})
		d.Requested = false
		d.Decision = "Enrolled"
		if t := m.Task("join-" + d.ID); t != nil && t.State == "blocked" {
			t.State = "succeeded"
			t.Phase = "Mock approval recorded"
			t.Result = "Pending membership granted in preview only."
		}
	}
	m.Notice = "Preview only · no commands submitted to real hosts."
}
func (m *Model) discoveryTable() table.Model {
	t := table.Model{Title: "Discovery", Empty: "No discovered hosts awaiting enrollment.", Columns: []string{"Host / address", "Membership", "Status", "Public fingerprint", "Actions"}}
	discovered := slices.Clone(m.Discoveries)
	slices.SortStableFunc(discovered, func(a, b Discovery) int {
		if a.Requested != b.Requested {
			if a.Requested {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.Name, b.Name)
	})
	for _, d := range discovered {
		if m.Host(d.ID) != nil {
			continue
		}
		label, tone := "Discovered", "muted"
		controls := []button.Model{{Label: "Add", Tone: "success", Action: Action{Kind: "add", ID: d.ID}}}
		if d.Requested {
			label, tone = "Awaiting approval", "warning"
			controls = []button.Model{{Label: "Approve", Tone: "success", Action: Action{Kind: "approve", ID: d.ID}}, {Label: "Reject", Tone: "danger", Action: Action{Kind: "reject", ID: d.ID}}}
		} else if d.Decision != "" {
			label = d.Decision
		}
		t.Records = append(t.Records, table.Record{ID: d.ID, Marker: "○", Tone: "muted", Controls: controls, Cells: []table.Cell{
			{Text: d.Name, Detail: d.Address}, {Text: "unjoined", Tone: status.MembershipTone("unjoined"), Badge: true}, {Text: label, Tone: tone, Badge: d.Requested}, {Text: d.Fingerprint}, {},
		}})
	}
	return t
}
