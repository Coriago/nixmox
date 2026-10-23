package ui

import "testing"

func TestText(t *testing.T) {
	for _, tt := range []struct {
		input, want string
		limit       int
		replacement rune
	}{
		{"a\x1bb\nc\u200d", "a b c ", 100, ' '},
		{"a\x1bb\nc\u200d", "abc", 100, -1},
		{"é界abc", "é界a", 3, -1},
		{"abc", "", 0, ' '},
	} {
		if got := Text(tt.input, tt.limit, tt.replacement); got != tt.want {
			t.Errorf("%q: got %q, want %q", tt.input, got, tt.want)
		}
	}
}
func TestCycle(t *testing.T) {
	for _, tt := range []struct{ index, step, count, want int }{{-1, 1, 3, 0}, {-1, -1, 3, 2}, {2, 1, 3, 0}, {0, -1, 3, 2}, {-1, 1, 0, -1}} {
		if got := Cycle(tt.index, tt.step, tt.count); got != tt.want {
			t.Errorf("%+v: got %d", tt, got)
		}
	}
}
func TestFindReturnsSliceElement(t *testing.T) {
	items := []int{1, 2, 3}
	found := Find(items, func(n int) bool { return n == 2 })
	if found == nil {
		t.Fatal("missing element")
	}
	*found = 4
	if items[1] != 4 || Find(items, func(n int) bool { return n == 5 }) != nil {
		t.Fatal("lookup copied element or fabricated a match")
	}
}
func TestActive(t *testing.T) {
	for _, state := range []string{"running", "reconciling", "cancelling"} {
		if !Active(state) {
			t.Fatal("inactive", state)
		}
	}
	for _, state := range []string{"queued", "blocked", "failed", "succeeded", "cancelled", "online"} {
		if Active(state) {
			t.Fatal("active", state)
		}
	}
}
