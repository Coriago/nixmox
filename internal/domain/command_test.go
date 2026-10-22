package domain

import (
	"encoding/json"
	"testing"
)

func TestCanonicalCommandNestedObjectsAndExactIntegers(t *testing.T) {
	c := Command{ID: "cmd_test", Type: "configuration.commit", TargetID: "n_target", Payload: json.RawMessage(`{"values":{"a":9007199254740993,"b":{"x":1,"y":2}}}`)}
	canonical, first, err := CanonicalCommand(c)
	if err != nil {
		t.Fatal(err)
	}
	c.Payload = json.RawMessage(`{ "values": { "b": {"y":2,"x":1}, "a":9007199254740993 } }`)
	_, second, err := CanonicalCommand(c)
	if err != nil || first != second {
		t.Fatal("object ordering changed retry identity", err)
	}
	if string(canonical.Payload) != `{"values":{"a":9007199254740993,"b":{"x":1,"y":2}}}` {
		t.Fatal("integer rounded", string(canonical.Payload))
	}
	c.Payload = json.RawMessage(`{"values":{"a":1,"a":2}}`)
	if _, _, err := CanonicalCommand(c); err == nil {
		t.Fatal("nested duplicate field accepted")
	}
	c.Payload = json.RawMessage(`{"host_id":"n_other"}`)
	if _, _, err := CanonicalCommand(c); err == nil {
		t.Fatal("payload retargeted envelope")
	}
}
