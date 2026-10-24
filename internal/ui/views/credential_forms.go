package views

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"nixmox/internal/domain"
	"nixmox/internal/engine"
	"nixmox/internal/ui/components/button"
	"nixmox/internal/ui/components/form"
)

type CredentialSubmission struct {
	Ingress engine.SecretIngress
	Value   []byte
}

func (m *Model) openSourceForm(kind, id string) {
	if !m.connected || m.busy || m.uncertain != "" || m.identity.ClusterID == nil {
		m.Notice = "Connect to a cluster and reconcile pending commands first"
		return
	}
	m.closeSourceForm()
	m.formKind = kind
	f := &form.Model{ID: uuid.NewString()}
	switch kind {
	case "credential-import":
		f.Title = "Import credential"
		var nodes []form.Choice
		for _, h := range m.Hosts {
			if h.Membership == "full" && h.Reachability == "online" {
				nodes = append(nodes, form.Choice{Value: h.ID, Label: h.Name})
			}
		}
		var kinds []form.Choice
		for _, k := range []string{"git-http-token", "git-ssh-key", "ssh-private-key", "nix-cache-signing-key", "sops-age-key"} {
			kinds = append(kinds, form.Choice{Value: k, Label: k})
		}
		f.Fields = []form.Field{{ID: "name", Label: "Name", Required: true}, {ID: "kind", Label: "Kind", Kind: "select", Value: "git-http-token", Choices: kinds, Required: true}, {ID: "holder", Label: "Storage node", Kind: "select", Choices: nodes, Required: true}, {ID: "secret", Label: "Token / single-line secret", Kind: "password"}, {ID: "file", Label: "Or select a private key file", Kind: "file"}}
		f.Note = "Only the selected full member stores the value. For multiline SSH/age keys use a file. The console HTTP connection is unencrypted; use a trusted management network."
	case "credential-check":
		credential := m.credential(id)
		if credential == nil {
			return
		}
		m.formCredentialID = credential.ID
		f.Title = "Test Git read access · " + credential.Name
		var repositories []form.Choice
		for _, r := range m.Repositories {
			repositories = append(repositories, form.Choice{Value: string(r.ID), Label: r.Name})
		}
		selected := ""
		if len(credential.Scope.RepositoryIDs) == 1 {
			selected = string(credential.Scope.RepositoryIDs[0])
		}
		f.Fields = []form.Field{{ID: "repository", Label: "Repository", Kind: "select", Required: true, Value: selected, Choices: repositories}}
		f.Note = "Lists remote refs without fetching or pushing. Success does not imply write permission."
	case "repository-import", "repository-bind":
		f.Title = "Import repository"
		var credentials []form.Choice
		for _, c := range m.Credentials {
			if strings.HasPrefix(c.Kind, "git-") {
				credentials = append(credentials, form.Choice{Value: string(c.ID), Label: c.Name})
			}
		}
		f.Fields = []form.Field{{ID: "name", Label: "Name", Required: true}, {ID: "url", Label: "Remote HTTPS / SSH URL", Required: true}, {ID: "branch", Label: "Upstream branch", Value: "main", Required: true}, {ID: "credential", Label: "Credential (blank for public access)", Kind: "select", Choices: credentials}, {ID: "username", Label: "Git username", Value: "git"}, {ID: "host-key", Label: "Verified Git SSH server public key"}}
		f.Note = "Never put tokens in URLs. SSH uses the imported Git credential, not a machine key or agent. Supply the server's verified public key for SSH."
		if kind == "repository-bind" {
			r := m.repository(id)
			if r == nil {
				return
			}
			m.formRepository = *r
			f.Title = "Repository credential · " + r.Name
			f.Fields = f.Fields[3:]
			f.Fields[0].Required = true
			if r.Remote.CredentialRef != nil {
				f.Fields[0].Value = string(*r.Remote.CredentialRef)
			}
			f.Fields[1].Value, f.Fields[2].Value = r.Remote.Username, r.Remote.HostKey
		}
	default:
		return
	}
	m.Form = f
}
func (m *Model) closeSourceForm() {
	clear(m.formSecret)
	m.formSecret = nil
	m.Form = nil
	m.formKind = ""
	m.formRepository = domain.Repository{}
	m.formCredentialID = ""
}
func (m *Model) SetFormValue(formID, fieldID, value string) {
	if m.Form == nil || m.Form.ID != formID || len(value) > domain.CommandPayloadLimit {
		return
	}
	for i := range m.Form.Fields {
		if m.Form.Fields[i].ID == fieldID {
			m.Form.Fields[i].Value = value
			return
		}
	}
}
func (m *Model) SetFormSecret(formID string, value []byte) {
	if m.Form == nil || m.Form.ID != formID || len(value) > domain.CommandPayloadLimit {
		return
	}
	clear(m.formSecret)
	m.formSecret = append([]byte(nil), value...)
}
func (m *Model) submitSourceForm() {
	if m.Form == nil || m.busy || m.uncertain != "" || !m.connected {
		return
	}
	values := map[string]string{}
	for _, f := range m.Form.Fields {
		if f.Required && strings.TrimSpace(f.Value) == "" {
			m.Notice = f.Label + " is required"
			return
		}
		values[f.ID] = f.Value
	}
	id := domain.ID("cmd_" + uuid.NewString())
	if m.formKind == "credential-import" {
		if len(m.formSecret) == 0 {
			m.Notice = "Select a private file or enter the secret"
			return
		}
		m.secretSubmission = &CredentialSubmission{Ingress: engine.SecretIngress{CommandID: id, HolderID: domain.ID(values["holder"]), Name: values["name"], Kind: values["kind"]}, Value: append([]byte(nil), m.formSecret...)}
	} else {
		command := domain.Command{ID: id, TargetID: m.identity.NodeID}
		var payload any
		credential := domain.ID(values["credential"])
		if m.formKind == "credential-check" {
			command.Type = "credential.check"
			repository := domain.ID(values["repository"])
			payload = domain.CredentialCheckPayload{CredentialID: m.formCredentialID, RepositoryID: &repository}
		} else if m.formKind == "repository-import" {
			command.Type = "repository.import"
			p := domain.RepositoryImport{Name: values["name"], RemoteURL: values["url"], UpstreamBranch: values["branch"], Username: values["username"], HostKey: values["host-key"]}
			if credential != "" {
				p.CredentialRef = &credential
			}
			payload = p
		} else {
			command.Type = "repository.credential.set"
			command.ExpectedVersions = domain.Versions{"repository:" + string(m.formRepository.ID): m.formRepository.Version}
			payload = domain.RepositoryCredentialSet{RepositoryID: m.formRepository.ID, CredentialID: credential, Username: values["username"], HostKey: values["host-key"]}
		}
		var err error
		command.Payload, err = json.Marshal(payload)
		if err != nil {
			m.Notice = "Form could not be encoded"
			return
		}
		m.command = &command
	}
	m.busy = true
	m.closeSourceForm()
	m.Notice = fmt.Sprintf("Submitting command %s", id)
}
func (m *Model) Close() {
	m.closeSourceForm()
	if m.secretSubmission != nil {
		clear(m.secretSubmission.Value)
		m.secretSubmission = nil
	}
}
func (m *Model) TakeCredentialSubmission() *CredentialSubmission {
	value := m.secretSubmission
	m.secretSubmission = nil
	return value
}
func (m *Model) sourceFormBlocks(v *View) {
	if m.Form != nil {
		v.Add(Block{Kind: Forms, Form: m.Form}, Block{Kind: Controls, Buttons: []button.Model{control("Save", "source-form-save", "", false), control("Cancel", "source-form-close", "", false)}})
	}
}
