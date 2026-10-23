package views

import "testing"

func TestDiscoveryHiddenWhenEmpty(t *testing.T) {
	for _, enrolled := range []bool{false, true} {
		m := New()
		if enrolled {
			for _, d := range m.Discoveries {
				m.Hosts = append(m.Hosts, Host{ID: d.ID, Name: d.Name, Membership: "pending"})
			}
		} else {
			m.Discoveries = nil
		}
		v := m.View()
		if len(tables(v)) != 2 || tables(v)[0].Title != "Cluster hosts" || tables(v)[1].Title != "Active tasks" {
			t.Fatal("empty Discovery should be omitted without hiding other Cluster sections", enrolled)
		}
	}
}
