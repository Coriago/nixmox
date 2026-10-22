package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"io"
	"net/url"
	"nixmox/internal/client"
	"nixmox/internal/domain"
	"os"
	"strings"
	"time"
)

type settingsMessage struct {
	credentials       []domain.Credential
	hosts             []domain.Host
	repositories      []domain.Repository
	cache             domain.CacheStatus
	receipt           *domain.Receipt
	task              *domain.Task
	locations         []domain.CacheLocation
	err               error
	refresh, imported bool
}
type settingsTick struct{}
type settingsModel struct {
	ctx                             context.Context
	client                          *client.Client
	credentials                     []domain.Credential
	hosts                           []domain.Host
	repositories                    []domain.Repository
	cache                           domain.CacheStatus
	selection, field                int
	mode, notice, taskID, locations string
	inputs                          []textinput.Model
	labels                          []string
	busy                            bool
	pending                         *domain.Command
	importRequest                   *domain.SecretImport
	reviewed                        *domain.Credential
}

func (m *settingsModel) fetch() tea.Cmd {
	m.busy = true
	taskID := m.taskID
	return func() tea.Msg {
		message := settingsMessage{refresh: true}
		var credentials domain.Page[domain.Credential]
		var hosts domain.Page[domain.Host]
		var repos domain.Page[domain.Repository]
		for _, read := range []struct {
			path string
			into any
		}{
			{"/api/v1/credentials?limit=500", &credentials}, {"/api/v1/hosts?limit=500", &hosts}, {"/api/v1/repositories?limit=500", &repos}, {"/api/v1/cache/status", &message.cache},
		} {
			if err := m.client.Read(m.ctx, read.path, read.into); err != nil {
				message.err = err
				return message
			}
		}
		message.credentials, message.hosts, message.repositories = credentials.Items, hosts.Items, repos.Items
		if taskID != "" {
			var task domain.Task
			if m.client.Read(m.ctx, "/api/v1/tasks/"+taskID, &task) == nil {
				message.task = &task
			}
		}
		return message
	}
}
func (m *settingsModel) form(mode string, labels, values []string, hidden int) {
	m.mode, m.labels, m.field = mode, labels, 0
	m.inputs = make([]textinput.Model, len(labels))
	for i := range labels {
		input := textinput.New()
		input.Width = 65
		input.CharLimit = 4096
		if i == hidden {
			input.EchoMode = textinput.EchoPassword
			input.CharLimit = 65536
		}
		if i < len(values) {
			input.SetValue(values[i])
		}
		m.inputs[i] = input
	}
	m.inputs[0].Focus()
}
func (m *settingsModel) send(command domain.Command) tea.Cmd {
	m.pending = &command
	m.busy = true
	return func() tea.Msg {
		receipt, err := m.client.Submit(m.ctx, command)
		return settingsMessage{receipt: &receipt, err: err}
	}
}
func (m *settingsModel) submit(kind, target string, payload any, versions map[string]int64) tea.Cmd {
	data, _ := json.Marshal(payload)
	return m.send(domain.Command{ID: domain.NewID("cmd_"), Type: kind, TargetID: target, Payload: data, ExpectedVersions: versions})
}
func (m *settingsModel) update(msg tea.Msg) tea.Cmd {
	if result, ok := msg.(settingsMessage); ok {
		m.busy = false
		if result.err != nil {
			m.notice = result.err.Error()
		} else {
			if result.refresh {
				m.credentials, m.hosts, m.repositories, m.cache = result.credentials, result.hosts, result.repositories, result.cache
				m.selection = min(m.selection, max(0, len(m.credentials)-1))
			}
			if result.receipt != nil {
				m.taskID = result.receipt.TaskID
				m.notice = "Accepted: " + m.taskID
				m.pending = nil
				m.mode = ""
				if result.imported {
					m.importRequest = nil
				}
			}
			if result.task != nil {
				m.notice = result.task.ID + " · " + result.task.State + " · " + result.task.Phase
				if result.task.Terminal() {
					m.notice += "\n" + string(result.task.Result)
					if result.task.Error != nil {
						m.notice += "\n" + result.task.Error.Message
					}
				}
			}
			if result.locations != nil {
				data, _ := json.MarshalIndent(result.locations, "", "  ")
				m.locations = string(data)
				m.mode = ""
			}
		}
		if result.refresh {
			return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return settingsTick{} })
		}
		return nil
	}
	if _, ok := msg.(settingsTick); ok {
		if !m.busy {
			return m.fetch()
		}
		return tea.Tick(5*time.Second, func(time.Time) tea.Msg { return settingsTick{} })
	}
	key, ok := msg.(tea.KeyMsg)
	if !ok || m.busy {
		return nil
	}
	if m.mode != "" {
		switch key.String() {
		case "esc":
			for i := range m.inputs {
				m.inputs[i].SetValue("")
			}
			m.mode = ""
			return nil
		case "tab", "shift+tab":
			m.inputs[m.field].Blur()
			delta := 1
			if key.String() == "shift+tab" {
				delta = -1
			}
			m.field = (m.field + delta + len(m.inputs)) % len(m.inputs)
			return m.inputs[m.field].Focus()
		case "enter":
			return m.submitForm()
		default:
			var cmd tea.Cmd
			m.inputs[m.field], cmd = m.inputs[m.field].Update(msg)
			return cmd
		}
	}
	switch key.String() {
	case "up":
		m.selection = max(0, m.selection-1)
	case "down":
		m.selection = min(max(0, len(m.credentials)-1), m.selection+1)
	case "r":
		return m.fetch()
	case "p":
		if m.pending != nil {
			return m.send(*m.pending)
		}
	case "a":
		node := m.cache.NodeID
		for _, h := range m.hosts {
			if h.Membership == "full" && h.Reachability == "online" {
				node = h.ID
				break
			}
		}
		values := []string{"", "git-http-token", node, "", ""}
		if m.importRequest != nil {
			values[0], values[1], values[2] = m.importRequest.Name, m.importRequest.Kind, m.importRequest.StorageNodeID
		}
		m.form("import", []string{"Name", "Kind", "Storage node ID", "Value (hidden)", "Or local secret file path"}, values, 3)
	case "l":
		m.form("locate", []string{"Store output path"}, nil, -1)
	case "c", "d", "k":
		if len(m.credentials) == 0 {
			return nil
		}
		credential := m.credentials[m.selection]
		m.reviewed = &credential
		if key.String() == "k" {
			if m.cache.Declared {
				m.notice = "Nix-declared cache selection is read-only."
				return nil
			}
			if credential.Kind != "nix-cache-signing-key" {
				m.notice = "Select a Nix signing credential."
				return nil
			}
			return m.submit("cache.credential.set", m.cache.NodeID, map[string]string{"credential_id": credential.ID}, map[string]int64{"settings:cache:" + m.cache.NodeID: m.cache.SettingsVersion})
		}
		if key.String() == "d" {
			m.form("remove", []string{"Type credential name to remove"}, nil, -1)
			return nil
		}
		if strings.HasPrefix(credential.Kind, "git-") {
			repo := ""
			if len(m.repositories) > 0 {
				repo = m.repositories[0].ID
			}
			m.form("check", []string{"Repository ID"}, []string{repo}, -1)
			return nil
		}
		return m.check(credential, "")
	}
	return nil
}
func (m *settingsModel) holder(c domain.Credential) string {
	for _, r := range c.Replicas {
		if r.State == "available" {
			return r.NodeID
		}
	}
	return ""
}
func (m *settingsModel) check(c domain.Credential, repo string) tea.Cmd {
	p := map[string]string{"credential_id": c.ID}
	if repo != "" {
		p["repository_id"] = repo
	}
	if m.holder(c) == "" {
		m.notice = "No credential holder is available."
		return nil
	}
	return m.submit("credential.check", m.holder(c), p, nil)
}
func (m *settingsModel) submitForm() tea.Cmd {
	switch m.mode {
	case "locate":
		path := m.inputs[0].Value()
		m.busy = true
		return func() tea.Msg {
			var rows []domain.CacheLocation
			err := m.client.Read(m.ctx, "/api/v1/cache/locations?path="+url.QueryEscape(path), &rows)
			return settingsMessage{locations: rows, err: err}
		}
	case "check":
		return m.check(*m.reviewed, m.inputs[0].Value())
	case "remove":
		c := *m.reviewed
		if m.inputs[0].Value() != c.Name {
			m.notice = "Credential name does not match."
			return nil
		}
		return m.submit("credential.remove", m.holder(c), map[string]string{"credential_id": c.ID}, map[string]int64{"credential:" + c.ID: c.Version})
	case "import":
		request := domain.SecretImport{Name: m.inputs[0].Value(), Kind: m.inputs[1].Value(), StorageNodeID: m.inputs[2].Value()}
		if m.importRequest != nil {
			request.ID = m.importRequest.ID
			if request != *m.importRequest {
				m.notice = "Retry the uncertain import with its original name, kind and storage node."
				return nil
			}
		} else {
			request.ID = domain.NewID("cmd_")
			m.importRequest = &request
		}
		value, path := m.inputs[3].Value(), m.inputs[4].Value()
		m.inputs[3].SetValue("")
		m.busy = true
		return func() tea.Msg {
			var reader io.Reader = strings.NewReader(value)
			if path != "" {
				f, err := os.Open(path)
				if err != nil {
					return settingsMessage{err: domain.Err("CREDENTIAL_UNAVAILABLE", "Secret file could not be opened.", 503)}
				}
				defer f.Close()
				reader = f
			}
			receipt, err := m.client.ImportCredential(m.ctx, request, reader)
			return settingsMessage{receipt: &receipt, err: err, imported: true}
		}
	}
	return nil
}
func (m *settingsModel) view() string {
	lines := []string{"Nixmox · Settings", "Credentials · ↑/↓ select · a add · c check · d remove · k select cache signer · l locate · r refresh · p retry command · Esc back", ""}
	for i, c := range m.credentials {
		marker := "  "
		if i == m.selection {
			marker = "› "
		}
		lines = append(lines, marker+c.Name+" · "+c.Kind)
		if i == m.selection {
			for _, r := range c.Replicas {
				lines = append(lines, "    "+r.NodeID+" · "+r.Source+" · "+r.State)
			}
			data, _ := json.Marshal(c.Scope)
			lines = append(lines, "    Bindings: "+string(data))
		}
	}
	lines = append(lines, fmt.Sprintf("\nCache · gateway %s · serving %t · active %d", m.cache.GatewayState, m.cache.Serving, m.cache.ActiveTransfers))
	if m.cache.ServingReason != nil {
		lines = append(lines, *m.cache.ServingReason)
	}
	for _, p := range m.cache.Peers {
		lines = append(lines, p.NodeID+": "+p.State)
	}
	if m.mode != "" {
		lines = append(lines, "\n"+m.mode+" · Tab next field · Enter submit · Esc cancel")
		for i, label := range m.labels {
			lines = append(lines, label+": "+m.inputs[i].View())
		}
	}
	lines = append(lines, m.locations, "\n"+m.notice)
	return strings.Join(lines, "\n") + "\n"
}
