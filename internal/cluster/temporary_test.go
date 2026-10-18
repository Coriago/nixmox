package cluster

import "testing"

func TestTemporaryInventoryDoesNotCountAsMember(t *testing.T) {
	v := NewView()
	v.Upsert(HostPayload{NodeID: "permanent"})
	v.Upsert(HostPayload{NodeID: "installer", Temporary: true})
	if v.Count() != 1 || len(v.List()) != 2 {
		t.Fatal("temporary participant changed membership or disappeared from inventory")
	}
	v.Remove("installer")
	if v.Count() != 1 || len(v.List()) != 1 {
		t.Fatal("temporary cleanup changed permanent membership")
	}
}
