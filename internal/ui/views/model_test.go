package views

import (
	"reflect"
	"testing"
	"time"
)

func TestStartsEmptyAndDisconnected(t *testing.T) {
	m := New()
	for range 100 {
		m.Advance(time.Second)
	}
	if len(m.Hosts)+len(m.Tasks)+len(m.Discoveries) != 0 || m.Cluster != (Cluster{}) {
		t.Fatal("controller manufactured observations")
	}
	if m.View().Connection != "Connecting…" {
		t.Fatal("connection state is misleading")
	}
	for _, b := range m.View().Toolbar {
		if b.Action.Kind == "replay" || b.Action.Kind == "pause" {
			t.Fatal("demo control remains")
		}
	}
}

func TestFramesAndUnwiredActionsNeverChangeObservations(t *testing.T) {
	m := New()
	m.Hosts = []Host{{ID: "node", Name: "node", Reachability: "online"}}
	m.Tasks = []Task{{ID: "task", HostID: "node", State: "running", Total: 10, Done: 2}}
	m.Discoveries = []Discovery{{ID: "new-node", Requested: true}}
	hosts, tasks, discoveries := append([]Host(nil), m.Hosts...), append([]Task(nil), m.Tasks...), append([]Discovery(nil), m.Discoveries...)
	for range 100 {
		m.Advance(time.Second)
	}
	for _, kind := range []string{"refresh", "network-check", "shutdown", "cancel", "approve", "reject", "add", "replay", "pause", "confirm"} {
		m.Dispatch(Action{Kind: kind, ID: "node"})
	}
	if !reflect.DeepEqual(hosts, m.Hosts) || !reflect.DeepEqual(tasks, m.Tasks) || !reflect.DeepEqual(discoveries, m.Discoveries) {
		t.Fatal("presentation changed backend-owned observations")
	}
	m.Dispatch(Action{Kind: "host", ID: "node"})
	for _, b := range m.View().HeaderActions {
		if !b.Disabled || b.Hint != "Backend is not connected" {
			t.Fatal("unwired action is enabled", b)
		}
	}
	for _, metric := range m.View().Find(Metrics).Metrics {
		if metric.Known {
			t.Fatal("missing telemetry was fabricated")
		}
	}
}

func TestProjectionInputsAreExplicit(t *testing.T) {
	m := New()
	m.Hosts = []Host{{ID: "one", Name: "one"}, {ID: "two", Name: "two"}}
	m.Tasks = []Task{{ID: "task", HostID: "one", State: "running"}}
	before := m.taskTable("Tasks", "", "All", "")
	m.Page, m.Query = "Tasks", "no match"
	if !reflect.DeepEqual(before, m.taskTable("Tasks", "", "All", "")) {
		t.Fatal("table depends on route state")
	}
	if len(m.hostTable("Hosts", "two").Records) != 1 {
		t.Fatal("explicit query ignored")
	}
}

func TestHelpTrapsActionsAndFollowIsShared(t *testing.T) {
	m := New()
	m.Dispatch(Action{Kind: "help"})
	m.Dispatch(Action{Kind: "page", ID: "Hosts"})
	if m.Page != "Cluster" {
		t.Fatal("modal did not trap actions")
	}
	m.Dispatch(Action{Kind: "dismiss"})
	m.Tasks = []Task{{ID: "task"}}
	m.Dispatch(Action{Kind: "task", ID: "task"})
	m.Dispatch(Action{Kind: "tab", ID: "Logs"})
	m.LogFollow.Scrolled(false)
	m.Dispatch(Action{Kind: "follow"})
	if !m.LogFollow.Follow {
		t.Fatal("follow did not resume")
	}
}
