package domain

import "testing"

func TestStrictJSON(t *testing.T) {
	for _, s := range []string{`{"id":"a","id":"b"}`, `{"id":"a"} {}`, `{"unknown":1}`, `{"id":1}`} {
		var v struct {
			ID string `json:"id"`
		}
		if StrictJSON([]byte(s), &v) == nil {
			t.Errorf("accepted %s", s)
		}
	}
	var v Command
	if err := StrictJSON([]byte(`{"id":"a","type":"network.check","target_id":"n_a","payload":{},"expected_versions":{}}`), &v); err != nil {
		t.Fatal(err)
	}
}
func TestTerminalIsImmutable(t *testing.T) {
	task := Task{State: "queued", Version: 1}
	for _, s := range []string{"running", "reconciling", "running", "succeeded"} {
		if err := task.Transition(s, "inspect"); err != nil {
			t.Fatal(err)
		}
	}
	if task.Version != 5 || !task.Terminal() || task.Cancellation.Allowed {
		t.Fatal(task)
	}
	if task.Transition("running", "inspect") == nil {
		t.Fatal("terminal task resumed")
	}
}
