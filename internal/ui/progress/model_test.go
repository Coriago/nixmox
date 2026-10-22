package progress

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func advance(d *Demo, frames int) {
	for range frames {
		d.Advance(FrameInterval)
	}
}
func TestInteractionAndPlayback(t *testing.T) {
	d := NewDemo()
	advance(d, 200)
	d.Dispatch(Action{Kind: Select, ID: "activity-1"})
	d.Dispatch(Action{Kind: Expand})
	advance(d, 200)
	v := d.View()
	if v.Selected != "activity-1" || !v.Rows[1].Expanded {
		t.Fatal("updates lost interaction state")
	}
	d.Dispatch(Action{Kind: Pause})
	before := d.View()
	advance(d, 100)
	if !reflect.DeepEqual(before, d.View()) {
		t.Fatal("paused playback changed")
	}
	d.Dispatch(Action{Kind: Restart})
	if d.clock != 0 || !d.paused || d.View().Selected != "activity-0" {
		t.Fatal("restart did not reset identity/clock or retain pause")
	}
	d.Dispatch(Action{Kind: Pause})
	advance(d, 1300)
	if d.View().Status != "COMPLETE" || d.View().Overall.Value != 1 {
		t.Fatal("success scenario did not finish")
	}
	d.Dispatch(Action{Kind: Completed})
	if len(d.View().Rows) != 0 || d.View().Selected != "" {
		t.Fatal("completed filtering")
	}
	d.Dispatch(Action{Kind: Next}) // Empty lists are valid.
	d.Dispatch(Action{Kind: Failure})
	advance(d, 1300)
	if d.View().Status != "FAILED" || d.View().Overall.Value == 1 || d.View().Rows[7].State != "queued" {
		t.Fatal("failure did not retain blocked dependent")
	}
	d.Dispatch(Action{Kind: Select, ID: "activity-5"})
	d.Dispatch(Action{Kind: Completed})
	if d.View().Selected != "activity-5" {
		t.Fatal("failed selection hidden")
	}
}
func TestAtomicObservationsAndBounds(t *testing.T) {
	m := New("task", "source")
	a := Activity{ID: "x", Name: "library", Kind: Download, State: Running, Known: true, Done: 9007199254740993, Total: 9007199254740994, Logs: []string{"before\x1b]52;payload\a"}}
	if !m.Apply(Batch{Sequence: 1, Activities: []Activity{a}}) {
		t.Fatal("valid observation rejected")
	}
	a.Logs[0] = "mutated"
	m.Dispatch(Action{Kind: Expand})
	v := m.View()
	if !strings.Contains(strings.Join(v.Rows[0].Detail, "\n"), "9007199254740993 / 9007199254740994 bytes") || strings.Contains(strings.Join(v.Rows[0].Detail, ""), "mutated") {
		t.Fatal("precision/ownership lost")
	}
	if strings.ContainsAny(strings.Join(v.Rows[0].Detail, ""), "\x1b\a") {
		t.Fatal("unsafe controls")
	}
	if m.Apply(Batch{Sequence: 1, Activities: []Activity{a}}) {
		t.Fatal("stale batch accepted")
	}
	bad := a
	bad.ID = "y"
	bad.Done = bad.Total + 1
	if m.Apply(Batch{Sequence: 2, Activities: []Activity{a, bad}}) || !reflect.DeepEqual(v, m.View()) {
		t.Fatal("invalid batch partly applied")
	}
	a.State = Succeeded
	if !m.Apply(Batch{Sequence: 2, Activities: []Activity{a}}) {
		t.Fatal("terminal state rejected")
	}
	a.State = Running
	if m.Apply(Batch{Sequence: 3, Activities: []Activity{a}}) {
		t.Fatal("terminal state regressed")
	}
	m = New("bounded", "")
	a.State = Running
	a.Known = false
	for i := 1; i < 300; i++ {
		a.Phase = fmt.Sprint(i)
		a.Logs = make([]string, 100)
		m.Apply(Batch{Sequence: uint64(i), Activities: []Activity{a}})
	}
	if len(m.feed) != MaxFeed || len(m.items[0].Logs) != MaxLogs {
		t.Fatal("unbounded history")
	}
	batch := Batch{Sequence: 400}
	for i := range MaxActivities {
		a.ID = fmt.Sprint(i)
		batch.Activities = append(batch.Activities, a)
	}
	if m.Apply(batch) || len(m.items) != 1 {
		t.Fatal("activity bound bypassed")
	}
	if !strings.Contains(m.View().Stats[1].Value, "unknown") {
		t.Fatal("unknown total manufactured")
	}
}
func TestSharedAnimation(t *testing.T) {
	d := NewDemo()
	advance(d, 100)
	v := d.View()
	if v.Rows[0].Progress.Display <= 0 || v.Rows[0].Progress.Display >= v.Rows[0].Progress.Value {
		t.Fatal("expected eased measured progress")
	}
	d.Dispatch(Action{Kind: Motion})
	v = d.View()
	if v.Rows[0].Progress.Display != v.Rows[0].Progress.Value || v.Rows[0].Marker != "●" {
		t.Fatal("reduced motion must snap and stop spinner")
	}
	if v.Rows[4].Progress.Known {
		t.Fatal("invented a build percentage")
	}
	before := d.clock
	d.Advance(time.Hour)
	if d.clock-before > 500*time.Millisecond {
		t.Fatal("suspended tab caused unbounded catchup")
	}
}

// This golden trace is executed natively AND under Go/WASM. It covers every
// interaction and animation frame, not just static fixtures or two Go objects.
func TestReplayParity(t *testing.T) {
	d := NewDemo()
	hash := sha256.New()
	actions := map[int]string{20: Next, 25: Expand, 35: Pause, 55: Pause, 250: Completed, 300: Motion, 350: Speed, 400: Failure, 600: Next, 650: Expand, 700: Restart, 800: Motion, 900: Speed, 1000: Failure}
	for i := range 1600 {
		if kind, ok := actions[i]; ok {
			d.Dispatch(Action{Kind: kind})
		}
		d.Advance(FrameInterval)
		data, err := json.Marshal(d.View())
		if err != nil {
			t.Fatal(err)
		}
		hash.Write(data)
	}
	got := hex.EncodeToString(hash.Sum(nil))
	const want = "6dfb235d10b925c9375fd468f449df1af05df2c9457826548b00da3d60b9dc79"
	if got != want {
		t.Fatalf("native/WASM replay digest = %s; want %s", got, want)
	}
}
func BenchmarkLiveFrame(b *testing.B) {
	d := NewDemo()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if i%600 == 0 {
			d.Dispatch(Action{Kind: Restart})
		}
		d.Advance(FrameInterval)
		_ = d.View()
	}
}
