package views

import (
	"fmt"
	"strings"

	"nixmox/internal/domain"
	"nixmox/internal/ui"
	"nixmox/internal/ui/components/table"
)

func (m *Model) credential(id string) *domain.Credential {
	return ui.Find(m.Credentials, func(c domain.Credential) bool { return string(c.ID) == id })
}
func (m *Model) repository(id string) *domain.Repository {
	return ui.Find(m.Repositories, func(r domain.Repository) bool { return string(r.ID) == id })
}
func (m *Model) credentialTable() table.Model {
	t := table.Model{Title: "Credentials", Columns: []string{"Name", "Kind", "Holders", "Bindings"}, Empty: "No credentials registered. Add a credential to a full member; values are never returned by reads."}
	for _, c := range m.Credentials {
		var holders []string
		for _, replica := range c.Replicas {
			holders = append(holders, fmt.Sprintf("%s · %s · %s", replica.NodeID, replica.State, replica.Source))
		}
		row := table.Record{ID: string(c.ID), Cells: []table.Cell{{Text: ui.Text(c.Name, 128, ' '), Detail: string(c.ID)}, {Text: c.Kind}, {Text: strings.Join(holders, "; ")}, {Text: fmt.Sprint(len(c.Scope.RepositoryIDs)) + " repositories"}}}
		row.Controls = append(row.Controls, m.resourceControl("Test access", "credential-check", string(c.ID)), m.resourceControl("Remove…", "credential-remove", string(c.ID)))
		t.Records = append(t.Records, row)
	}
	return t
}
func repositoriesPage(m *Model, v *View) {
	v.Subtitle = "Accepted sources and private Git access; fetching never publishes"
	v.HeaderActions = append(v.HeaderActions, control("Import repository", "repository-import", "", false))
	m.sourceFormBlocks(v)
	t := table.Model{Title: "Repositories", Columns: []string{"Repository", "Remote", "Upstream", "Credential"}, Empty: "No repositories registered."}
	for _, r := range m.Repositories {
		credential := "Public / none"
		if r.Remote.CredentialRef != nil {
			credential = string(*r.Remote.CredentialRef)
			if c := m.credential(credential); c != nil {
				credential = c.Name
			}
		}
		commit := "Not fetched"
		if r.Upstream.Commit != nil {
			commit = string(*r.Upstream.Commit)
		}
		row := table.Record{ID: string(r.ID), Cells: []table.Cell{{Text: ui.Text(r.Name, 128, ' '), Detail: string(r.ID)}, {Text: ui.Text(r.Remote.URL, 512, ' ')}, {Text: r.Upstream.Branch, Detail: commit}, {Text: ui.Text(credential, 128, ' ')}}}
		row.Controls = append(row.Controls, m.resourceControl("Fetch", "repository-fetch", string(r.ID)), control("Credential…", "repository-bind", string(r.ID), false))
		t.Records = append(t.Records, row)
	}
	v.Add(Block{Kind: Tables, Table: t})
}
