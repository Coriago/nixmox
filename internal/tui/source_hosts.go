package tui

import (
	"encoding/json"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"nixmox/internal/domain"
	"nixmox/internal/presentation"
	"strings"
	"time"
)

func (m *sourceModel) hostMessage(v sourceMessage) tea.Cmd {
	switch v.mode {
	case "hosts":
		m.hosts = v.hosts
	case "host":
		m.host = v.host
	case "assign":
		m.configurations = v.configurations
	case "build-progress":
		m.buildTask = v.task
		if v.task != nil && !v.task.Terminal() && v.task.State != "blocked" {
			return m.pollBuild(v.task.ID)
		}
	}
	return nil
}
func (m *sourceModel) pollBuild(id string) tea.Cmd {
	ctx, c := m.ctx, m.client
	return func() tea.Msg {
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return sourceMessage{err: ctx.Err()}
		case <-timer.C:
		}
		task, err := c.Task(ctx, id)
		return sourceMessage{mode: "build-progress", task: &task, err: err}
	}
}
func (m *sourceModel) hostKey(key tea.KeyMsg) (bool, tea.Cmd) {
	if key.String() == "h" {
		m.busy = true
		return true, func() tea.Msg {
			var page domain.Page[domain.Host]
			err := m.client.Read(m.ctx, "/api/v1/hosts?limit=500", &page)
			return sourceMessage{mode: "hosts", hosts: page.Items, err: err}
		}
	}
	if m.mode != "hosts" && m.mode != "host" && m.mode != "assign" && m.mode != "build-progress" {
		return false, nil
	}
	switch key.String() {
	case "b":
		return false, nil
	case "up", "k":
		m.selection = max(0, m.selection-1)
	case "down", "j":
		count := len(m.hosts)
		if m.mode == "assign" {
			count = len(m.configurations)
		}
		m.selection = min(max(0, count-1), m.selection+1)
	case "enter":
		if m.mode == "hosts" && len(m.hosts) > 0 {
			m.host = m.hosts[m.selection]
			m.mode = "host"
			m.selection = 0
		}
		if m.mode == "assign" && len(m.configurations) > 0 {
			cfg, host, ctx, c := m.configurations[m.selection], m.host, m.ctx, m.client
			m.busy = true
			return true, func() tea.Msg {
				_, err := sourceSubmit(ctx, c, host.ID, "host.assign", map[string]string{"host_id": host.ID, "repository_id": cfg.RepositoryID, "configuration": cfg.ID}, map[string]int64{"host:" + host.ID: host.Version, "configuration:" + cfg.ID: cfg.Version})
				if err == nil {
					err = c.Read(ctx, "/api/v1/hosts/"+host.ID, &host)
				}
				return sourceMessage{mode: "host", host: host, err: err}
			}
		}
	case "a":
		if m.mode == "host" {
			if m.repo.ID == "" {
				m.notice = "Open a repository first (b), then return to Hosts to select a configuration."
				return true, nil
			}
			id, ctx, c := m.repo.ID, m.ctx, m.client
			m.busy = true
			return true, func() tea.Msg {
				var page domain.Page[domain.Configuration]
				err := c.Read(ctx, "/api/v1/configurations?repository_id="+id+"&limit=500", &page)
				return sourceMessage{mode: "assign", configurations: page.Items, err: err}
			}
		}
	case "p", "v":
		if m.mode == "host" {
			kind := "deployment.preview"
			if key.String() == "v" {
				kind = "deployment.build"
			}
			host, ctx, c := m.host, m.ctx, m.client
			m.busy = true
			return true, func() tea.Msg {
				body, _ := json.Marshal(map[string]string{"host_id": host.ID})
				cmd := domain.Command{ID: domain.NewID("cmd_"), Type: kind, TargetID: host.ID, ExpectedVersions: map[string]int64{"host:" + host.ID: host.Version}, Payload: body}
				receipt, err := c.Submit(ctx, cmd)
				if err != nil {
					return sourceMessage{err: fmt.Errorf("%s · command %s", err, cmd.ID)}
				}
				task, err := c.Task(ctx, receipt.TaskID)
				return sourceMessage{mode: "build-progress", task: &task, err: err}
			}
		}
	case "c":
		if m.mode == "build-progress" && m.buildTask != nil && m.buildTask.Cancellation.Allowed && !m.buildTask.Terminal() {
			task, ctx, c := *m.buildTask, m.ctx, m.client
			return true, func() tea.Msg {
				body, _ := json.Marshal(map[string]string{"task_id": task.ID})
				_, err := c.Submit(ctx, domain.Command{ID: domain.NewID("cmd_"), Type: "task.cancel", TargetID: task.TargetID, ExpectedVersions: map[string]int64{"task:" + task.ID: task.Version}, Payload: body})
				return sourceMessage{mode: "build-progress", task: &task, err: err}
			}
		}
	}
	return true, nil
}
func (m *sourceModel) hostView() string {
	var b strings.Builder
	switch m.mode {
	case "hosts":
		for i, h := range m.hosts {
			mark := "  "
			if i == m.selection {
				mark = "> "
			}
			fmt.Fprintf(&b, "%s%s · %s · %s · %s\n", mark, h.Name, h.Membership, h.Environment, h.Reachability)
		}
		b.WriteString("\nEnter host detail")
	case "host":
		h := m.host
		fmt.Fprintf(&b, "%s · %s\n%s · %s · %s\nHealth: %s\n", h.Name, h.ID, h.Membership, h.Environment, h.Reachability, h.Health.Status)
		if h.Configuration != nil {
			fmt.Fprintf(&b, "Assigned: %s · %s\n", h.Configuration.RepositoryID, h.Configuration.Name)
		} else {
			b.WriteString("No configuration assigned\n")
		}
		for _, reason := range h.Health.Reasons {
			fmt.Fprintf(&b, "%s: %s\n", reason.Code, reason.Message)
		}
		b.WriteString("\na assign from selected repository · p preview · v build candidate · h hosts")
	case "assign":
		fmt.Fprintf(&b, "Assign to %s from %s\n", m.host.Name, m.repo.Name)
		for i, c := range m.configurations {
			mark := "  "
			if i == m.selection {
				mark = "> "
			}
			fmt.Fprintf(&b, "%s%s · %s · %s\n", mark, c.Name, c.Readiness, c.InspectedCommit)
		}
		b.WriteString("\nEnter assign reviewed configuration")
	case "build-progress":
		if m.buildTask != nil {
			b.WriteString(presentation.Task(*m.buildTask) + "\n" + presentation.TaskProgress(*m.buildTask))
			if m.buildTask.Terminal() {
				b.WriteString("\n" + presentation.TaskResult(*m.buildTask))
			}
			b.WriteString("\nc cancel · h hosts")
		}
	}
	return b.String()
}
