package views

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"nixmox/internal/client"
	"nixmox/internal/domain"
	"nixmox/internal/ui"
)

func (m *Model) Apply(update Update) {
	if update.Submitted {
		m.busy = false
	}
	if update.Err != nil {
		var uncertain *client.UnknownSubmission
		if errors.As(update.Err, &uncertain) {
			m.Notice = "Request outcome unknown. Refresh and inspect state before retrying; it was not resubmitted."
		} else {
			m.Notice = update.Err.Error()
		}
		if !update.Submitted && update.Resource == "" {
			m.connected = false
			m.connectionError = update.Err.Error()
		}
		return
	}
	if snapshot := update.Snapshot; snapshot != nil {
		m.connected = true
		m.connectionError = ""
		m.warnings = strings.Join(snapshot.Warnings, "; ")
		m.Health = snapshot.Health
		m.Cluster = snapshot.Cluster
		if snapshot.Hosts != nil {
			m.Hosts = nil
			for _, member := range snapshot.Hosts {
				address := ""
				if len(member.Endpoints) > 0 {
					address = member.Endpoints[0].HTTP
				}
				m.Hosts = append(m.Hosts, Host{Source: member, ID: member.NodeID, Name: ui.Text(member.Name, 120, ' '), Address: ui.Text(address, 200, ' '), Membership: member.Status})
			}
		}
		if snapshot.Tasks != nil {
			previous := m.Tasks
			m.Tasks = nil
			for _, task := range snapshot.Tasks {
				projected := projectTask(task)
				if old := ui.Find(previous, func(t Task) bool { return t.ID == task.ID }); old != nil {
					projected.Log = old.Log
					projected.Build = old.Build
					projected.LastLog = old.LastLog
				}
				projected.Build.Status = string(task.Status)
				m.Tasks = append(m.Tasks, projected)
			}
		}
		if snapshot.Repositories != nil {
			m.Repositories = snapshot.Repositories
		}
		if snapshot.Recipes != nil {
			m.Recipes = snapshot.Recipes
		}
		if snapshot.Configurations != nil {
			m.Configurations = snapshot.Configurations
		}
	}
	if update.Task != nil {
		projected := projectTask(*update.Task)
		if task := m.Task(projected.ID); task != nil {
			projected.Log = task.Log
			projected.Build = task.Build
			projected.LastLog = task.LastLog
			*task = projected
			task.Build.Status = task.State
		} else {
			m.Tasks = append(m.Tasks, projected)
		}
	}
	if len(update.Log) > 0 {
		m.applyLog(update)
	}
	if !update.Submitted {
		return
	}
	m.Form = nil
	m.formRequest = nil
	if len(update.Response) == 0 {
		m.Notice = "Request accepted"
		return
	}
	m.Notice = "Request completed"
	switch update.Target {
	case "task":
		var task domain.Task
		if json.Unmarshal(update.Response, &task) == nil && task.ID != "" {
			m.Apply(Update{Task: &task})
			m.Page = "Tasks"
			m.TaskID = task.ID
			m.HostID = ""
			m.Tab = "Overview"
		}
	case "recipe", "configuration", "repository":
		var document Document
		if json.Unmarshal(update.Response, &document) == nil {
			m.Document = &document
			m.DocumentID = document.ID
			m.Page = map[string]string{"recipe": "Recipes", "configuration": "Configurations", "repository": "Repositories"}[update.Target]
		}
	case "preview", "output":
		m.Preview = update.Response
	}
}

func projectTask(task domain.Task) Task {
	return Task{Source: task, ID: task.ID, Name: ui.Text(task.Type, 100, ' '), HostID: task.HostID, State: string(task.Status)}
}
func (m *Model) applyLog(update Update) {
	sequence, err := strconv.ParseUint(update.EventID, 10, 64)
	if err != nil {
		return
	}
	var payload struct {
		ResourceLogs []struct {
			ScopeLogs []struct {
				LogRecords []struct{ Body struct{ StringValue string } } `json:"logRecords"`
			} `json:"scopeLogs"`
		} `json:"resourceLogs"`
	}
	if json.Unmarshal(update.Log, &payload) != nil {
		return
	}
	var task *Task
	if strings.HasPrefix(update.Resource, "tasks/") {
		id := strings.TrimPrefix(update.Resource, "tasks/")
		task = m.Task(id)
		if task == nil {
			m.Tasks = append(m.Tasks, Task{ID: id})
			task = &m.Tasks[len(m.Tasks)-1]
		}
	}
	if task != nil {
		if sequence <= task.LastLog {
			return
		}
		task.LastLog = sequence
	} else {
		if update.Resource != "hosts/"+m.HostID || sequence <= m.lastHostLog[update.Resource] {
			return
		}
		m.lastHostLog[update.Resource] = sequence
	}
	for _, resource := range payload.ResourceLogs {
		for _, scope := range resource.ScopeLogs {
			for _, record := range scope.LogRecords {
				line := record.Body.StringValue
				if task == nil {
					m.HostLog.Append(line)
					continue
				}
				task.Build.ApplyLine(line)
				task.Build.Status = task.State
				task.Log = task.Build.Log
			}
		}
	}
}
