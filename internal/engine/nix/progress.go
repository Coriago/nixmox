package nix

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"nixmox/internal/domain"
	"nixmox/internal/engine/internal/shared"
)

type nixEvent struct {
	Action string            `json:"action"`
	ID     uint64            `json:"id"`
	Type   int               `json:"type"`
	Parent uint64            `json:"parent"`
	Fields []json.RawMessage `json:"fields"`
}

type nixActivity struct {
	activity      domain.Activity
	kind          int
	started, last time.Time
	lastBytes     uint64
}

// NixProgress normalizes the Nix 2.25 internal-json activity contract. Unknown
// records explicitly degrade telemetry and never become subprocess failures.
type Progress struct {
	mu         sync.Mutex
	task, node domain.ID
	activities map[uint64]*nixActivity
	attempts   map[string]uint64
	snapshot   domain.TaskProgress
	Log        func(domain.ID, []byte) // Optional sanitized per-build log sink.
}

func NewProgress(task, node domain.ID) *Progress {
	return &Progress{task: task, node: node, activities: map[uint64]*nixActivity{}, attempts: map[string]uint64{}, snapshot: domain.TaskProgress{SchemaVersion: 1, TaskID: task, Phase: "building", Observed: domain.Observed{Freshness: domain.Unknown}, Downloads: domain.DownloadProgress{ByteBasis: "download", TotalsKind: "known_so_far"}}}
}

func cleanSource(raw string) *string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return nil
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	value := u.String()
	return &value
}

func (p *Progress) Consume(line []byte) ([]domain.Activity, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !bytes.HasPrefix(line, []byte("@nix ")) {
		return nil, nil
	}
	p.snapshot.Version++
	p.snapshot.ObservedAt = shared.Ptr(time.Now().UTC())
	p.snapshot.Freshness = domain.Live
	degrade := func(reason string) ([]domain.Activity, error) {
		p.snapshot.Freshness = domain.Unknown
		p.snapshot.Unavailable = []domain.MeasurementError{{Field: "nix.activities", Reason: reason}}
		return nil, nil
	}
	var event nixEvent
	if len(line) > domain.CommandPayloadLimit || json.Unmarshal(line[5:], &event) != nil {
		return degrade("Malformed or oversized internal-json event")
	}
	text := func(index int) (string, bool) {
		if index >= len(event.Fields) {
			return "", false
		}
		var v string
		err := json.Unmarshal(event.Fields[index], &v)
		return v, err == nil
	}
	number := func(index int) (uint64, bool) {
		if index >= len(event.Fields) {
			return 0, false
		}
		var v uint64
		err := json.Unmarshal(event.Fields[index], &v)
		return v, err == nil
	}
	now := time.Now()
	a := p.activities[event.ID]
	switch event.Action {
	case "msg":
		return nil, nil
	case "start":
		if a != nil {
			return nil, nil
		}
		if len(p.activities) >= 4096 {
			return degrade("Activity retention bound reached")
		}
		a = &nixActivity{kind: event.Type, started: now, activity: domain.Activity{ID: domain.ID(fmt.Sprintf("activity_%d", event.ID)), TaskID: p.task, NodeID: p.node, State: "running", Version: 1}}
		switch event.Type {
		case 101:
			source, ok := text(0)
			if !ok {
				return degrade("Missing transfer source")
			}
			a.activity.Kind = "download"
			a.activity.Source = &domain.ActivitySource{Kind: "cache", URL: cleanSource(source)}
			a.activity.Name = "Download"
			if parent := p.activities[event.Parent]; parent != nil {
				a.activity.StorePath = parent.activity.StorePath
				if a.activity.StorePath != nil {
					a.activity.Name = filepath.Base(*a.activity.StorePath)
				}
			}
			a.activity.Progress = domain.Counter{Unit: "bytes", ByteBasis: shared.Ptr("download")}
		case 105:
			drv, ok := text(0)
			if !ok {
				return degrade("Missing build derivation")
			}
			a.activity.Kind = "build"
			a.activity.DerivationPath = &drv
			a.activity.Name = filepath.Base(drv)
			if remote, ok := text(1); ok && remote != "" {
				a.activity.Source = &domain.ActivitySource{Kind: "remote_builder", URL: cleanSource(remote)}
			}
			if p.Log != nil {
				ref := fmt.Sprintf("/api/v1/tasks/%s/activities/%s/logs", p.task, a.activity.ID)
				a.activity.LogRef = &ref
			}
		case 108:
			path, ok := text(0)
			if !ok {
				return degrade("Missing substitute path")
			}
			a.activity.Kind = "substitution"
			a.activity.StorePath = &path
			a.activity.Name = filepath.Base(path)
			if source, ok := text(1); ok {
				a.activity.Source = &domain.ActivitySource{Kind: "cache", URL: cleanSource(source)}
			}
		case 102, 103, 104:
			a.activity.Kind = "aggregate"
		default:
			return degrade("Unsupported Nix activity type")
		}
		key := a.activity.Kind + ":" + a.activity.Name
		p.attempts[key]++
		a.activity.Attempt = p.attempts[key]
		p.activities[event.ID] = a
	case "stop":
		if a == nil {
			return nil, nil
		}
		// Nix destructors emit stop on errors too; stop is NOT a successful outcome.
		a.activity.State = "unknown"
		a.activity.BytesPerSecond = nil
	case "result":
		if a == nil {
			return degrade("Result for unknown activity")
		}
		switch event.Type {
		case 101:
			if line, ok := text(0); ok && p.Log != nil && a.kind == 105 {
				p.Log(a.activity.ID, []byte(line+"\n"))
			}
		case 104:
			phase, ok := text(0)
			if !ok {
				return degrade("Invalid build phase")
			}
			a.activity.BuildPhase = &phase
		case 105:
			done, ok := number(0)
			total, ok2 := number(1)
			if !ok || !ok2 {
				return degrade("Invalid progress counters")
			}
			switch a.kind {
			case 104:
				running, ok3 := number(2)
				failed, ok4 := number(3)
				if !ok3 || !ok4 {
					return degrade("Invalid build counters")
				}
				p.snapshot.Builds = domain.BuildProgress{CompletedDerivations: &done, TotalDerivations: &total, ActiveDerivations: &running, FailedDerivations: &failed}
			case 103:
				running, ok := number(2)
				if !ok {
					return degrade("Invalid substitution counters")
				}
				p.snapshot.Downloads.CompletedPaths = &done
				p.snapshot.Downloads.TotalPaths = &total
				p.snapshot.Downloads.ActivePaths = &running
			case 101:
				a.activity.BytesPerSecond = shared.Rate(a.lastBytes, done, now.Sub(a.last).Seconds(), !a.last.IsZero())
				a.last, a.lastBytes = now, done
				a.activity.Progress.Completed = &done
				if total > 0 {
					a.activity.Progress.Total = &total
				} else {
					a.activity.Progress.Total = nil
				}
			}
		case 106:
			kind, ok := number(0)
			expected, ok2 := number(1)
			if !ok || !ok2 {
				return degrade("Invalid expected-work counters")
			}
			if kind == 101 {
				p.snapshot.Downloads.BytesTotal = &expected
			}
		default:
			return degrade("Unsupported Nix result type")
		}
	default:
		return degrade("Unsupported Nix event action")
	}
	if a == nil {
		return nil, nil
	}
	a.activity.Version++
	a.activity.ElapsedSeconds = now.Sub(a.started).Seconds()
	p.refresh()
	return []domain.Activity{shared.Clone(a.activity)}, nil
}

func (p *Progress) refresh() {
	active := []domain.ID{}
	latest := map[string]*nixActivity{}
	for _, a := range p.activities {
		if a.activity.State == "running" && a.activity.Kind != "aggregate" {
			active = append(active, a.activity.ID)
		}
		if a.kind == 101 {
			key := a.activity.Name
			if a.activity.Source != nil && a.activity.Source.URL != nil {
				key = *a.activity.Source.URL
			}
			if a.activity.StorePath != nil {
				key = *a.activity.StorePath
			}
			if old := latest[key]; old == nil || old.started.Before(a.started) {
				latest[key] = a
			}
		}
	}
	var received uint64
	var speed float64
	known := false
	for _, a := range latest {
		if a.activity.Progress.Completed != nil {
			received += *a.activity.Progress.Completed
			known = true
		}
		if a.activity.BytesPerSecond != nil {
			speed += *a.activity.BytesPerSecond
		}
	}
	if known {
		p.snapshot.Downloads.BytesReceived = &received
	} else {
		p.snapshot.Downloads.BytesReceived = nil
	}
	p.snapshot.Downloads.BytesPerSecond = nil
	p.snapshot.Downloads.ETASeconds = nil
	if speed > 0 {
		p.snapshot.Downloads.BytesPerSecond = &speed
		if total := p.snapshot.Downloads.BytesTotal; total != nil && *total >= received {
			p.snapshot.Downloads.ETASeconds = shared.Ptr(float64(*total-received) / speed)
		}
	}
	sort.Slice(active, func(i, j int) bool { return active[i] < active[j] })
	if len(active) > 16 {
		active = active[:16]
	}
	p.snapshot.ActiveActivityIDs = active
}

func (p *Progress) Snapshot() domain.TaskProgress {
	p.mu.Lock()
	defer p.mu.Unlock()
	return shared.Clone(p.snapshot)
}

// ConfirmActivity is called only after verifying the exact output/derivation.
func (p *Progress) ConfirmActivity(id uint64, success bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if a := p.activities[id]; a != nil {
		a.activity.State = "failed"
		if success {
			a.activity.State = "completed"
		}
		a.activity.Version++
		p.snapshot.Version++
		p.refresh()
	}
}

func (p *Progress) Activities() []domain.Activity {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := []domain.Activity{}
	for _, a := range p.activities {
		if !strings.Contains(a.activity.Kind, "aggregate") {
			result = append(result, shared.Clone(a.activity))
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
