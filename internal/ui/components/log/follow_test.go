package log

import "testing"

func TestFollowRequiresExplicitResume(t *testing.T) {
	state := FollowState{Follow: true}
	state.Scrolled(true)
	if !state.Follow {
		t.Fatal("tail updates paused follow")
	}
	state.Scrolled(false)
	state.Scrolled(true)
	if state.Follow {
		t.Fatal("scrolling to bottom silently resumed follow")
	}
	if state.Control().Action.Kind != "follow" || state.Control().Active {
		t.Fatal("resume control does not reflect shared state")
	}
}
