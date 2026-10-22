package tui

import (
	"encoding/json"
	"fmt"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"net/url"
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
	case "generations":
		m.generations = v.generations
	case "package-diff":
		m.deploymentDiff = v.diff
	case "build-progress":
		m.buildTask = v.task
		if v.diff != nil {
			m.deploymentDiff = v.diff
		}
		if v.task != nil && v.task.Approval != nil && (m.deploymentDiff == nil || m.deploymentDiff.ID != v.task.Approval.DiffID) {
			return m.pollBuild(v.task.ID)
		}
		if v.task != nil && !v.task.Terminal() && !v.task.RequiresAttention() {
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
		var diff *domain.PackageDiff
		if err == nil && task.Approval != nil {
			var d domain.PackageDiff
			err = c.Read(ctx, "/api/v1/diffs/"+task.Approval.DiffID, &d)
			diff = &d
		}
		return sourceMessage{mode: "build-progress", task: &task, diff: diff, err: err}
	}
}
func (m *sourceModel) hostKey(key tea.KeyMsg) (bool, tea.Cmd) {
	if m.mode == "adopt-confirm" {
		if key.String() == "enter" {
			task, confirmation, ctx, c := *m.buildTask, m.diffQuery.Value(), m.ctx, m.client
			m.busy = true
			return true, func() tea.Msg {
				var candidate domain.Candidate
				if err := json.Unmarshal(task.Result, &candidate); err != nil {
					return sourceMessage{err: err}
				}
				var h domain.Host
				if err := c.Read(ctx, "/api/v1/hosts/"+task.TargetID, &h); err != nil {
					return sourceMessage{err: err}
				}
				result, err := sourceSubmit(ctx, c, h.ID, "host.adopt", map[string]string{"host_id": h.ID, "candidate_id": candidate.ID, "confirm_host": confirmation}, map[string]int64{"host:" + h.ID: h.Version})
				return sourceMessage{mode: "build-progress", task: &result, err: err}
			}
		}
		var cmd tea.Cmd
		m.diffQuery, cmd = m.diffQuery.Update(key)
		return true, cmd
	}
	if m.mode == "diff-search" {
		if key.String() == "enter" {
			m.mode = "package-diff"
			return true, m.readDiffPage("")
		}
		var cmd tea.Cmd
		m.diffQuery, cmd = m.diffQuery.Update(key)
		return true, cmd
	}
	if key.String() == "h" {
		m.busy = true
		return true, func() tea.Msg {
			var page domain.Page[domain.Host]
			err := m.client.Read(m.ctx, "/api/v1/hosts?limit=500", &page)
			return sourceMessage{mode: "hosts", hosts: page.Items, err: err}
		}
	}
	if m.mode != "hosts" && m.mode != "host" && m.mode != "assign" && m.mode != "build-progress" && m.mode != "deploy-options" && m.mode != "generations" && m.mode != "package-diff" {
		return false, nil
	}
	switch key.String() {
	case "y":
		if m.mode == "build-progress" && m.buildTask != nil && m.buildTask.Kind == "deployment.build" && m.buildTask.State == "succeeded" {
			m.mode = "adopt-confirm"
			m.diffQuery = textinput.New()
			m.diffQuery.Placeholder = "Confirm host name or ID"
			m.diffQuery.CharLimit = 256
			return true, m.diffQuery.Focus()
		}
	case "l":
		if m.mode == "host" || m.mode == "hosts" {
			if m.mode == "hosts" {
				if len(m.hosts) == 0 {
					return true, nil
				}
				m.host = m.hosts[m.selection]
			}
			m.mode = "deploy-options"
			m.deployMode = "switch"
			m.deployAuto = false
			m.rollback = nil
		}
	case "g":
		if m.mode == "host" {
			host, ctx, c := m.host, m.ctx, m.client
			return true, func() tea.Msg {
				var p domain.Page[domain.Generation]
				err := c.Read(ctx, "/api/v1/hosts/"+host.ID+"/generations?limit=500", &p)
				return sourceMessage{mode: "generations", generations: p.Items, err: err}
			}
		}
	case "m":
		if m.mode == "deploy-options" {
			if m.deployMode == "switch" {
				m.deployMode = "boot"
			} else {
				m.deployMode = "switch"
			}
		}
	case " ":
		if m.mode == "deploy-options" {
			m.deployAuto = !m.deployAuto
		}
	case "d":
		if m.mode == "build-progress" && m.deploymentDiff != nil {
			m.mode = "package-diff"
		} else if m.mode == "build-progress" && m.buildTask != nil && m.buildTask.Kind == "deployment.build" && m.buildTask.State == "succeeded" {
			task, ctx, c := *m.buildTask, m.ctx, m.client
			return true, func() tea.Msg {
				var candidate domain.Candidate
				if err := json.Unmarshal(task.Result, &candidate); err != nil {
					return sourceMessage{err: err}
				}
				t, err := sourceSubmit(ctx, c, task.TargetID, "deployment.diff", map[string]string{"host_id": task.TargetID, "candidate_id": candidate.ID}, nil)
				if err != nil {
					return sourceMessage{err: err}
				}
				var d domain.PackageDiff
				if err = json.Unmarshal(t.Result, &d); err == nil {
					err = c.Read(ctx, "/api/v1/diffs/"+d.ID, &d)
				}
				return sourceMessage{mode: "package-diff", diff: &d, err: err}
			}
		}
	case "/":
		if m.mode == "package-diff" {
			m.diffQuery = textinput.New()
			m.diffQuery.Placeholder = "literal package / version / path"
			m.diffQuery.CharLimit = 512
			m.diffQuery.Focus()
			m.mode = "diff-search"
		}
	case "f":
		if m.mode == "package-diff" {
			m.diffKind = (m.diffKind + 1) % 6
			return true, m.readDiffPage("")
		}
	case "n":
		if m.mode == "package-diff" && m.deploymentDiff != nil && m.deploymentDiff.NextCursor != nil {
			return true, m.readDiffPage(*m.deploymentDiff.NextCursor)
		}
	case "b":
		return false, nil
	case "up", "k":
		m.selection = max(0, m.selection-1)
	case "down", "j":
		count := len(m.hosts)
		if m.mode == "generations" {
			count = len(m.generations)
		}
		if m.mode == "assign" {
			count = len(m.configurations)
		}
		m.selection = min(max(0, count-1), m.selection+1)
	case "enter":
		if m.mode == "package-diff" {
			m.mode = "build-progress"
			return true, nil
		}
		if m.mode == "generations" && len(m.generations) > 0 {
			g := m.generations[m.selection]
			m.rollback = &g
			m.mode = "deploy-options"
			m.deployMode = "switch"
			m.deployAuto = false
			return true, nil
		}
		if m.mode == "deploy-options" {
			host, ctx, c, mode, auto, generation := m.host, m.ctx, m.client, m.deployMode, m.deployAuto, m.rollback
			m.busy = true
			return true, func() tea.Msg {
				p := map[string]any{"mode": mode, "auto_approve": auto}
				kind := "deployment.apply"
				if generation != nil {
					kind = "deployment.rollback"
					p["generation_id"] = generation.ID
				}
				body, _ := json.Marshal(p)
				receipt, err := c.Submit(ctx, domain.Command{ID: domain.NewID("cmd_"), Type: kind, TargetID: host.ID, Payload: body, ExpectedVersions: map[string]int64{"host:" + host.ID: host.Version}})
				if err != nil {
					return sourceMessage{err: err}
				}
				task, err := c.Task(ctx, receipt.TaskID)
				return sourceMessage{mode: "build-progress", task: &task, err: err}
			}
		}
		if m.mode == "build-progress" && m.buildTask != nil && m.buildTask.Approval != nil && m.deploymentDiff != nil && m.deploymentDiff.ID == m.buildTask.Approval.DiffID && m.buildTask.Approval.State == "pending" && m.buildTask.State == "blocked" {
			task, ctx, c := *m.buildTask, m.ctx, m.client
			m.busy = true
			return true, func() tea.Msg {
				_, err := sourceSubmit(ctx, c, task.TargetID, "deployment.approve", map[string]any{"task_id": task.ID, "diff_id": task.Approval.DiffID, "diff_version": task.Approval.DiffVersion}, nil)
				if err == nil {
					task, err = c.Task(ctx, task.ID)
				}
				return sourceMessage{mode: "build-progress", task: &task, err: err}
			}
		}
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
		b.WriteString("\nEnter host detail · l Apply latest")
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
		b.WriteString("\nl Apply latest · g History / rollback · a assign · p preview · v build candidate · h hosts")
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
	case "deploy-options":
		fmt.Fprintf(&b, "Deploy to %s · mode %s\nAuto-approve: %t\n", m.host.Name, m.deployMode, m.deployAuto)
		if m.rollback != nil {
			fmt.Fprintf(&b, "Rollback generation %d · %s\n", m.rollback.Number, m.rollback.OutputPath)
		} else {
			b.WriteString("Latest assigned configuration\n")
		}
		b.WriteString("\nm change mode · Space toggle Auto-approve · Enter Start")
	case "generations":
		for i, g := range m.generations {
			mark := "  "
			if i == m.selection {
				mark = "> "
			}
			fmt.Fprintf(&b, "%s%d · running=%t · boot=%t · available=%t\n  %s\n", mark, g.Number, g.Running, g.Boot, g.Available, g.OutputPath)
		}
		b.WriteString("\nEnter review rollback")
	case "diff-search":
		b.WriteString(m.diffQuery.View() + "\nEnter search")
	case "package-diff":
		if m.deploymentDiff != nil {
			b.WriteString(presentation.PackageDiff(*m.deploymentDiff))
		}
		b.WriteString("\n/ search · f category · n next page · Enter task")
	case "build-progress":
		if m.buildTask != nil {
			b.WriteString(presentation.Task(*m.buildTask) + "\n" + presentation.TaskProgress(*m.buildTask))
			if m.buildTask.Terminal() {
				b.WriteString("\n" + presentation.TaskResult(*m.buildTask))
			}
			if m.deploymentDiff != nil {
				d := *m.deploymentDiff
				d.Changes = nil
				b.WriteString("\n" + presentation.PackageDiff(d))
			}
			if m.buildTask.Approval != nil && m.buildTask.Approval.State == "pending" {
				b.WriteString("\nEnter Approve this comparison")
			}
			b.WriteString("\nd packages / search · y verify running candidate · c cancel · h hosts")
		}
	case "adopt-confirm":
		b.WriteString("Verify exact running output without activation. Desired binding remains unchanged.\n" + m.diffQuery.View() + "\nEnter verify · Esc back")
	}
	return b.String()
}

func (m *sourceModel) readDiffPage(cursor string) tea.Cmd {
	if m.deploymentDiff == nil {
		return nil
	}
	d, ctx, c, kind, q := *m.deploymentDiff, m.ctx, m.client, m.diffKind, m.diffQuery.Value()
	return func() tea.Msg {
		var page domain.Page[domain.PackageChange]
		kinds := []string{"", "added", "removed", "version_changed", "output_changed", "ambiguous"}
		query := url.Values{"kind": {kinds[kind]}, "q": {q}, "cursor": {cursor}, "limit": {"8"}}
		err := c.Read(ctx, "/api/v1/diffs/"+d.ID+"/changes?"+query.Encode(), &page)
		d.Changes = page.Items
		d.NextCursor = page.NextCursor
		return sourceMessage{mode: "package-diff", diff: &d, err: err}
	}
}
