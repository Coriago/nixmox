package tui

import (
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"nixmox/internal/domain"
	"nixmox/internal/presentation"
	"strings"
	"testing"
)

func TestSharedPresentationAndResize(t *testing.T) {
	n := domain.UnknownNetwork("n_test")
	m := Model{viewport: viewport.New(80, 10), width: 80}
	updated, _ := m.Update(snapshot{network: n})
	m = updated.(Model)
	if !strings.Contains(m.View(), presentation.Network(n)) {
		t.Fatal("terminal diverges from shared status")
	}
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 25, Height: 8})
	m = updated.(Model)
	if m.viewport.Width != 21 || m.viewport.Height != 1 {
		t.Fatal("resize not bounded")
	}
	m.viewport.SetContent(strings.Repeat("line\n", 100))
	m.viewport.SetYOffset(5)
	updated, _ = m.Update(snapshot{network: n, tasks: make([]domain.Task, 50)})
	m = updated.(Model)
	if m.viewport.YOffset != 5 {
		t.Fatal("live update lost scroll position")
	}
}

func TestClusterCreatePreservesEnteredPayloadOnUncertainRetry(t *testing.T) {
	m := Model{viewport: viewport.New(80, 20), width: 80, network: domain.UnknownNetwork("n_test"), cluster: domain.ClusterStatus{Membership: "unjoined"}, nameInput: textinput.New()}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = updated.(Model)
	if !m.creating {
		t.Fatal("create form unavailable")
	}
	m.nameInput.SetValue("homelab")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	if m.pending == nil || m.pending.Type != "cluster.create" || string(m.pending.Payload) != `{"name":"homelab"}` {
		t.Fatal("invalid create request", m.pending)
	}
	command := *m.pending
	updated, _ = m.Update(submitted{err: domain.Err("OUTCOME_UNKNOWN", "Unknown outcome.", 503)})
	m = updated.(Model)
	m.nameInput.SetValue("another")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = updated.(Model)
	if m.pending.ID != command.ID || string(m.pending.Payload) != string(command.Payload) {
		t.Fatal("retry changed request")
	}
}

func TestTaskControlPreservesReviewedVersionAndSelection(t *testing.T) {
	task := domain.Task{ID: "task_selected", TargetID: "n_test", Version: 7, State: "succeeded", Kind: "network.check"}
	m := Model{viewport: viewport.New(100, 20), width: 100, network: domain.UnknownNetwork("n_test"), tasks: []domain.Task{task}, taskFocus: true}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	m = updated.(Model)
	if cmd != nil || m.pending != nil || m.notice != presentation.TaskActions(task)[0].Reason {
		t.Fatal("enabled cancel on terminal task")
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	m = updated.(Model)
	if m.pending == nil || m.pending.Type != "task.retry" || m.pending.ExpectedVersions["task:"+task.ID] != 7 {
		t.Fatal("lost reviewed task binding")
	}
	saved := m.pending.ID
	updated, _ = m.Update(snapshot{network: m.network, tasks: []domain.Task{{ID: "task_new", Version: 1}, task}})
	m = updated.(Model)
	if m.selected != 1 || m.pending.ID != saved || m.pending.ExpectedVersions["task:"+task.ID] != 7 {
		t.Fatal("live update changed selection or pending command")
	}
}
