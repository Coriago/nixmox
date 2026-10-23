package views

import (
	"nixmox/internal/ui"
	"reflect"
	"testing"
	"time"
)

func TestSharedActionsAndReview(t *testing.T) {
	m := New()
	m.Dispatch(Action{Kind: "host", ID: "birch"})
	m.Dispatch(Action{Kind: "host", ID: "alder"})
	if m.HostID != "alder" || len(m.View().Find(Metrics).Metrics) != 4 || len(tables(m.View())) == 0 {
		t.Fatal("host switch lost integrated telemetry or history")
	}
	count := len(m.Tasks)
	m.Dispatch(Action{Kind: "shutdown"})
	if m.Modal == nil || len(m.Tasks) != count {
		t.Fatal("action bypassed review")
	}
	m.Dispatch(Action{Kind: "page", ID: "Settings"})
	if m.Page != "Hosts" {
		t.Fatal("background accepted input under modal")
	}
	m.Dispatch(Action{Kind: "dismiss"})
	if len(m.Tasks) != count {
		t.Fatal("dismiss executed effects")
	}
	m.Dispatch(Action{Kind: "shutdown"})
	m.Dispatch(Action{Kind: "confirm"})
	if len(m.Tasks) != count+1 || m.Task(m.TaskID).State != "reconciling" {
		t.Fatal("mock power must reconcile, not claim off")
	}
	m.Dispatch(Action{Kind: "approve", ID: "willow"})
	m.Dispatch(Action{Kind: "confirm"})
	if m.Host("willow").Membership != "pending" || m.Host("willow").Reachability != "unknown" {
		t.Fatal("approval fabricated live/full member")
	}
}
func TestMockProgressBoundedAndTerminal(t *testing.T) {
	m := New()
	for range 1000 {
		m.Advance(100 * time.Millisecond)
	}
	for _, task := range m.Tasks {
		if len(task.Log.Lines) > 160 {
			t.Fatal("unbounded log")
		}
		if task.Total > 0 && task.Done != task.Total {
			t.Fatal("mock work never completed", task)
		}
	}
	for _, host := range m.Hosts {
		if len(host.History) > 60 {
			t.Fatal("unbounded telemetry")
		}
	}
	if m.Task("power-cedar").State != "reconciling" {
		t.Fatal("offline was used as proof of shutdown")
	}
	_, tone := m.View().Hosts[2].Marker, m.View().Hosts[2].Tone
	if tone != "muted" {
		t.Fatal("offline host animated as active")
	}
}
func TestPauseFiltersCancelAndMotion(t *testing.T) {
	m := New()
	m.Dispatch(Action{Kind: "pause"})
	before := m.Tasks[0].Done
	m.Advance(time.Hour)
	if before != m.Tasks[0].Done {
		t.Fatal("pause advanced observations")
	}
	m.Dispatch(Action{Kind: "motion"})
	v := m.View()
	m.Advance(time.Second)
	if tables(m.View())[len(tables(v))-1].Records[0].Marker != tables(v)[len(tables(v))-1].Records[0].Marker {
		t.Fatal("reduced motion spinner changed")
	}
	m.Dispatch(Action{Kind: "task", ID: "network-birch"})
	m.Dispatch(Action{Kind: "cancel"})
	m.Dispatch(Action{Kind: "confirm"})
	if m.Task("network-birch").State != "cancelled" {
		t.Fatal("cancellation missing")
	}
	m.Dispatch(Action{Kind: "page", ID: "Tasks"})
	m.Dispatch(Action{Kind: "filter", ID: "Active"})
	for _, row := range tables(m.View())[0].Records {
		if !ui.Active(row.Cells[1].Text) {
			t.Fatal("active filter", row.Cells[1].Text)
		}
	}
}
func TestDeterministicPresentation(t *testing.T) {
	a, b := New(), New()
	for range 50 {
		a.Advance(100 * time.Millisecond)
		b.Advance(100 * time.Millisecond)
	}
	if !reflect.DeepEqual(a.View(), b.View()) {
		t.Fatal("adapters would receive different views")
	}
}
