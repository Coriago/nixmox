package views

import (
	"nixmox/internal/ui"
	"slices"
	"testing"
)

func TestClusterLandingAndFlatSettings(t *testing.T) {
	m := New()
	v := m.View()
	if m.Page != "Cluster" || v.Find(Tabs) != nil || slices.ContainsFunc(Pages, func(p Page) bool { return p.Name == "Overview" }) {
		t.Fatal("Cluster is not the single landing page")
	}
	if len(tables(v)) == 0 || tables(v)[0].Title != "Discovery" {
		t.Fatal("Discovery must be the first Cluster section")
	}
	titles := []string{}
	for _, table := range tables(v) {
		titles = append(titles, table.Title)
	}
	for _, title := range []string{"Cluster hosts", "Active tasks", "Discovery"} {
		if !slices.Contains(titles, title) {
			t.Fatal("missing Cluster section", title)
		}
	}
	if v.Find(Sections) != nil {
		t.Fatal("obsolete control-plane or attention section remains")
	}
	for _, row := range tables(v)[len(tables(v))-1].Records {
		if !ui.Active(row.Cells[1].Text) {
			t.Fatal("Cluster active tasks includes inactive work")
		}
	}
	m.Dispatch(Action{Kind: "page", ID: "Settings"})
	v = m.View()
	if v.Find(Tabs) != nil {
		t.Fatal("Settings still has tabs")
	}
	titles = nil
	for _, b := range v.Blocks {
		if b.Kind == Sections {
			titles = append(titles, b.Section.Title)
		}
	}
	for _, title := range []string{"Theme", "Animation & accessibility", "Current connection", "Effective configuration"} {
		if !slices.Contains(titles, title) {
			t.Fatal("settings hidden behind tabs", title)
		}
	}
}
func TestDiscoveryOrderingAndDecisions(t *testing.T) {
	m := New()
	table := m.discoveryTable()
	if len(table.Records) != 3 {
		t.Fatal("missing discovered fixtures")
	}
	for i, row := range table.Records {
		if row.Cells[1].Text != "unjoined" || row.Cells[1].Tone != "muted" {
			t.Fatal("request was confused with pending membership")
		}
		if i < 2 {
			if row.Cells[2].Text != "Awaiting approval" || row.Cells[2].Tone != "warning" || !row.Cells[2].Badge {
				t.Fatal("request not prioritized/labeled")
			}
			if len(row.Controls) != 2 || row.Controls[0].Tone != "success" || row.Controls[1].Tone != "danger" {
				t.Fatal("missing green/red decisions")
			}
		} else if row.Controls[0].Label != "Add" || row.Controls[0].Tone != "success" {
			t.Fatal("normal discovery missing green Add")
		}
	}
	m.Dispatch(Action{Kind: "reject", ID: "maple"})
	if m.Modal == nil || m.Modal.ResourceID != "maple" {
		t.Fatal("decision not bound to reviewed identity")
	}
	m.Dispatch(Action{Kind: "confirm"})
	if m.Host("maple") != nil || m.discovery("maple").Requested {
		t.Fatal("reject granted membership or retained pending request")
	}
	if m.discoveryTable().Records[0].ID != "willow" {
		t.Fatal("remaining requester not promoted to top")
	}
	for _, action := range []Action{{Kind: "approve", ID: "willow"}, {Kind: "add", ID: "spruce"}} {
		m.Dispatch(action)
		if m.Modal == nil {
			t.Fatal("missing enrollment review")
		}
		m.Dispatch(Action{Kind: "confirm"})
		h := m.Host(action.ID)
		if h == nil || h.Membership != "pending" || h.Reachability != "unknown" {
			t.Fatal("enrollment fabricated full/live membership")
		}
	}
	table = m.discoveryTable()
	if len(table.Records) != 1 || table.Records[0].ID != "maple" || table.Records[0].Controls[0].Label != "Add" {
		t.Fatal("enrolled nodes remained in discovery")
	}
}
func TestDiscoveryReviewIsExactAndExplicit(t *testing.T) {
	m := New()
	m.Dispatch(Action{Kind: "approve", ID: "spruce"})
	if m.Modal != nil {
		t.Fatal("approved discovery without a request")
	}
	m.Dispatch(Action{Kind: "add", ID: "willow"})
	if m.Modal != nil {
		t.Fatal("request bypassed approval")
	}
	m.Dispatch(Action{Kind: "approve", ID: "willow"})
	m.discovery("willow").Fingerprint = "changed-key"
	m.Dispatch(Action{Kind: "confirm"})
	if m.Host("willow") != nil {
		t.Fatal("stale key review accepted")
	}
	m.Dispatch(Action{Kind: "reject", ID: "willow"})
	m.Dispatch(Action{Kind: "dismiss"})
	if !m.discovery("willow").Requested {
		t.Fatal("dismiss rejected a request")
	}
}
func TestMembershipColorsInListsAndDetails(t *testing.T) {
	for state, tone := range map[string]string{"pending": "warning", "full": "success", "removed": "danger", "unjoined": "muted"} {
		m := New()
		m.Hosts[0].Membership = state
		m.Dispatch(Action{Kind: "page", ID: "Hosts"})
		cell := tables(m.View())[0].Records[0].Cells[2]
		if cell.Text != state || cell.Tone != tone || !cell.Badge {
			t.Fatal("inventory membership color", state, cell)
		}
		m.Dispatch(Action{Kind: "host", ID: "alder"})
		f := m.View().Find(Identity).Fields[1]
		if f.Value != state || f.Tone != tone {
			t.Fatal("detail membership color", state, f)
		}
	}
}
