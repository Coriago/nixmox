// Package progress owns one renderer-independent progress/feed component.
// It has no transport, terminal, DOM, timers or goroutines. Call it on one UI loop.
package progress

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode"
)

type State string

const (
	Queued    State = "queued"
	Running   State = "running"
	Succeeded State = "succeeded"
	Failed    State = "failed"
)

type Kind string

const (
	Build    Kind = "build"
	Download Kind = "download"
)
const (
	MaxActivities = 128
	MaxFeed       = 64
	MaxLogs       = 8
	FrameInterval = 50 * time.Millisecond
)

// Activity is an observation, not an executable task. Unknown totals stay unknown.
type Activity struct {
	ID, Name, Host, Phase string
	Kind                  Kind
	State                 State
	Done, Total           uint64
	Known                 bool
	BytesPerSecond        float64
	Elapsed               time.Duration
	Logs                  []string
}
type Batch struct {
	Sequence   uint64
	At         time.Duration
	Activities []Activity // incremental upserts; absence does not mean deletion
}
type Action struct{ Kind, ID string }

const (
	Select    = "select"
	Next      = "next"
	Previous  = "previous"
	Expand    = "expand"
	Completed = "completed"
	Motion    = "motion"
	Pause     = "pause"
	Restart   = "restart"
	Speed     = "speed"
	Failure   = "failure"
)

type FeedEntry struct{ At, Text, Tone string }
type Model struct {
	title, subtitle        string
	items                  []Activity
	sequence               uint64
	elapsed                time.Duration
	selected               string
	expanded               map[string]bool
	hideCompleted, reduced bool
	frame                  uint64
	bars                   map[string]float64
	feed                   []FeedEntry
}

func New(title, subtitle string) *Model {
	return &Model{title: clean(title), subtitle: clean(subtitle), expanded: map[string]bool{}, bars: map[string]float64{}}
}

// Apply rejects stale or invalid batches atomically. A terminal activity cannot
// change outcome; a new attempt needs a new identity. Retention is explicit/bounded.
func (m *Model) Apply(b Batch) bool {
	if b.Sequence <= m.sequence || b.At < m.elapsed || len(b.Activities) > MaxActivities {
		return false
	}
	seen := map[string]bool{}
	added := 0
	for _, a := range b.Activities {
		if a.ID == "" || len(a.ID) > 128 || clean(a.ID) != a.ID || seen[a.ID] || (a.Kind != Build && a.Kind != Download) || (a.State != Queued && a.State != Running && a.State != Succeeded && a.State != Failed) || a.Elapsed < 0 || a.BytesPerSecond < 0 || math.IsNaN(a.BytesPerSecond) || math.IsInf(a.BytesPerSecond, 0) || (a.Known && a.Done > a.Total) {
			return false
		}
		seen[a.ID] = true
		i := m.index(a.ID)
		if i < 0 {
			added++
		} else if terminal(m.items[i].State) && a.State != m.items[i].State {
			return false
		}
	}
	if len(m.items)+added > MaxActivities {
		return false
	}
	m.sequence = b.Sequence
	m.elapsed = b.At
	for _, a := range b.Activities {
		a.Name = clean(a.Name)
		a.Host = clean(a.Host)
		a.Phase = clean(a.Phase)
		logs := a.Logs[max(0, len(a.Logs)-MaxLogs):]
		a.Logs = make([]string, len(logs))
		for i, s := range logs {
			a.Logs[i] = clean(s)
		}
		i := m.index(a.ID)
		var old Activity
		if i < 0 {
			m.items = append(m.items, a)
		} else {
			old = m.items[i]
			m.items[i] = a
		}
		if a.State != Queued && (old.State != a.State || old.Phase != a.Phase) {
			m.feed = append(m.feed, FeedEntry{duration(b.At), a.Name + " · " + string(a.State) + " · " + a.Phase, tone(a.State)})
		}
	}
	m.feed = slices.Clone(m.feed[max(0, len(m.feed)-MaxFeed):])
	m.reconcileSelection()
	return true
}
func terminal(s State) bool { return s == Succeeded || s == Failed }
func (m *Model) index(id string) int {
	return slices.IndexFunc(m.items, func(a Activity) bool { return a.ID == id })
}
func (m *Model) visible() []Activity {
	var rows []Activity
	for _, a := range m.items {
		if !m.hideCompleted || a.State != Succeeded {
			rows = append(rows, a)
		}
	}
	return rows
}
func (m *Model) reconcileSelection() {
	rows := m.visible()
	if slices.ContainsFunc(rows, func(a Activity) bool { return a.ID == m.selected }) {
		return
	}
	m.selected = ""
	if len(rows) > 0 {
		m.selected = rows[0].ID
	}
}
func (m *Model) Dispatch(a Action) {
	rows := m.visible()
	switch a.Kind {
	case Select:
		if slices.ContainsFunc(rows, func(r Activity) bool { return r.ID == a.ID }) {
			m.selected = a.ID
		}
	case Next, Previous:
		if len(rows) == 0 {
			return
		}
		i := slices.IndexFunc(rows, func(r Activity) bool { return r.ID == m.selected })
		if a.Kind == Next {
			i++
		} else {
			i--
		}
		i = (i + len(rows)) % len(rows)
		m.selected = rows[i].ID
	case Expand:
		if m.selected != "" {
			m.expanded[m.selected] = !m.expanded[m.selected]
		}
	case Completed:
		m.hideCompleted = !m.hideCompleted
		m.reconcileSelection()
	case Motion:
		m.reduced = !m.reduced
	}
}

// Frame advances the same easing and spinner in both runtimes, independently of
// observation frequency. Animations never manufacture measured percentages.
func (m *Model) Frame() {
	m.frame++
	for id, target := range m.targets() {
		value := m.bars[id]
		if m.reduced || math.Abs(target-value) < 0.001 {
			value = target
		} else {
			value += (target - value) * 0.28
		}
		m.bars[id] = value
	}
}
func (m *Model) targets() map[string]float64 {
	targets := map[string]float64{}
	finished := 0
	for _, a := range m.items {
		if terminal(a.State) {
			finished++
		}
		if a.Known && a.Total > 0 {
			targets["row/"+a.ID] = float64(a.Done) / float64(a.Total)
		}
	}
	if len(m.items) > 0 {
		targets["overall"] = float64(finished) / float64(len(m.items))
	}
	return targets
}
func (m *Model) fraction(id string, actual float64) float64 {
	if m.reduced {
		return actual
	}
	return math.Round(m.bars[id]*10000) / 10000
}
func clean(s string) string {
	r := []rune(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, s))
	if len(r) > 240 {
		return string(r[:239]) + "…"
	}
	return string(r)
}
func duration(d time.Duration) string {
	s := int(d.Seconds())
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}
func bytes(n uint64) string { return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20)) }

// Key bindings are shared too. Adapters normalize native keys, not their meaning.
func Key(key string) (Action, bool) {
	bindings := map[string]string{"ArrowDown": Next, "down": Next, "j": Next, "ArrowUp": Previous, "up": Previous, "k": Previous, "Enter": Expand, "enter": Expand, " ": Pause, "space": Pause, "p": Pause, "r": Restart, "s": Speed, "f": Failure, "c": Completed, "m": Motion}
	kind, ok := bindings[key]
	return Action{Kind: kind}, ok
}
