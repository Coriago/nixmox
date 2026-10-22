package nixstore

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Nix 2.34's activity/result tags and fields:
// https://github.com/NixOS/nix/blob/2.34.9/src/libutil/include/nix/util/logging.hh
// https://github.com/NixOS/nix/blob/2.34.9/src/libmain/progress-bar.cc
type Activity struct {
	ID             string           `json:"id"`
	TaskID         string           `json:"task_id"`
	Attempt        int              `json:"attempt"`
	Kind           string           `json:"kind"`
	State          string           `json:"state"`
	NodeID         string           `json:"node_id"`
	Name           string           `json:"name"`
	StorePath      *string          `json:"store_path"`
	DerivationPath *string          `json:"derivation_path"`
	Source         *ActivitySource  `json:"source"`
	Progress       ActivityProgress `json:"progress"`
	BytesPerSecond *float64         `json:"bytes_per_second"`
	ElapsedSeconds float64          `json:"elapsed_seconds"`
	BuildPhase     *string          `json:"build_phase"`
	LogRef         *string          `json:"log_ref"`
}
type ActivitySource struct {
	Kind string `json:"kind"`
	URL  string `json:"url"`
}
type ActivityProgress struct {
	Completed uint64  `json:"completed"`
	Total     *uint64 `json:"total"`
	Unit      string  `json:"unit"`
	ByteBasis string  `json:"byte_basis,omitempty"`
}
type BuildCounts struct {
	Completed uint64  `json:"completed_derivations"`
	Total     *uint64 `json:"total_derivations"`
	Active    uint64  `json:"active_derivations"`
	Failed    uint64  `json:"failed_derivations"`
}
type DownloadCounts struct {
	CompletedPaths uint64   `json:"completed_paths"`
	TotalPaths     *uint64  `json:"total_paths"`
	ActivePaths    uint64   `json:"active_paths"`
	BytesReceived  uint64   `json:"bytes_received"`
	BytesTotal     *uint64  `json:"bytes_total"`
	BytesPerSecond *float64 `json:"bytes_per_second"`
	RetryBytes     uint64   `json:"retry_bytes"`
	ByteBasis      string   `json:"byte_basis"`
	TotalsKind     string   `json:"totals_kind"`
}
type Progress struct {
	SchemaVersion     int            `json:"schema_version"`
	TaskID            string         `json:"task_id"`
	Version           int64          `json:"version"`
	ObservedAt        time.Time      `json:"observed_at"`
	Phase             string         `json:"phase"`
	Freshness         string         `json:"freshness"`
	PlanComplete      bool           `json:"plan_complete"`
	Downloads         DownloadCounts `json:"downloads"`
	Builds            BuildCounts    `json:"builds"`
	ActiveActivityIDs []string       `json:"active_activity_ids"`
	Availability      string         `json:"availability"`
	DroppedRecords    uint64         `json:"dropped_records"`
}
type event struct {
	Action string            `json:"action"`
	ID     uint64            `json:"id"`
	Type   int               `json:"type"`
	Parent uint64            `json:"parent"`
	Fields []json.RawMessage `json:"fields"`
}
type progressRecord struct {
	kind          int
	parent        uint64
	activity      *Activity
	started, last time.Time
	lastBytes     uint64
	stopped       bool
}
type Tracker struct {
	task, node    string
	records       map[uint64]*progressRecord
	attempts      map[string]int
	counts        BuildCounts
	version       int64
	seen, dropped uint64
	finished      bool
	success       bool
}

func NewTracker(task, node string) *Tracker {
	return &Tracker{task: task, node: node, records: map[uint64]*progressRecord{}, attempts: map[string]int{}}
}
func fieldString(e event, index int) string {
	var s string
	if index < len(e.Fields) {
		_ = json.Unmarshal(e.Fields[index], &s)
	}
	return s
}
func fieldNumber(e event, index int) (uint64, bool) {
	var value uint64
	if index >= len(e.Fields) || json.Unmarshal(e.Fields[index], &value) != nil {
		return 0, false
	}
	return value, true
}
func origin(raw string) *ActivitySource {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil
	}
	return &ActivitySource{Kind: "cache", URL: u.Scheme + "://" + u.Host}
}
func (t *Tracker) Observe(line []byte) {
	if !strings.HasPrefix(string(line), "@nix ") {
		return
	}
	var e event
	if json.Unmarshal(line[5:], &e) != nil || len(e.Fields) > 16 {
		return
	}
	t.seen++
	now := time.Now()
	switch e.Action {
	case "start":
		if _, ok := t.records[e.ID]; ok {
			return
		}
		if len(t.records) >= 10000 {
			t.dropped++
			return
		}
		record := &progressRecord{kind: e.Type, parent: e.Parent, started: now, last: now}
		t.records[e.ID] = record
		if e.Type == 105 || e.Type == 108 {
			path := fieldString(e, 0)
			if !PathPattern.MatchString(path) {
				return
			}
			kind := "build"
			unit := "derivations"
			if e.Type == 108 {
				kind = "download"
				unit = "bytes"
			}
			key := kind + ":" + path
			t.attempts[key]++
			a := &Activity{ID: "activity_" + strconv.FormatUint(e.ID, 10), TaskID: t.task, NodeID: t.node, Attempt: t.attempts[key], Kind: kind, State: "running", Name: strings.TrimSuffix(filepath.Base(path)[33:], ".drv"), Progress: ActivityProgress{Unit: unit}}
			if e.Type == 105 {
				a.DerivationPath = &path
			} else {
				a.StorePath = &path
				a.Source = origin(fieldString(e, 1))
				a.Progress.ByteBasis = "download"
			}
			record.activity = a
		}
	case "result":
		record := t.records[e.ID]
		if record == nil {
			return
		}
		if e.Type == 104 && record.activity != nil && record.activity.Kind == "build" {
			phase := fieldString(e, 0)
			switch phase {
			case "unpackPhase", "patchPhase", "configurePhase", "buildPhase", "checkPhase", "installPhase", "fixupPhase", "installCheckPhase", "distPhase":
				record.activity.BuildPhase = &phase
			}
		}
		if e.Type != 105 {
			return
		}
		done, ok := fieldNumber(e, 0)
		total, ok2 := fieldNumber(e, 1)
		if !ok || !ok2 {
			return
		}
		if record.kind == 104 {
			active, _ := fieldNumber(e, 2)
			failed, _ := fieldNumber(e, 3)
			t.counts = BuildCounts{Completed: done, Active: active, Failed: failed}
			if total > 0 {
				t.counts.Total = &total
			}
		}
		if record.kind == 101 {
			parent := record.parent
			var owner *Activity
			for depth := 0; parent != 0 && depth < 32; depth++ {
				r := t.records[parent]
				if r == nil {
					break
				}
				if r.kind == 109 {
					return
				}
				if r.activity != nil && r.activity.Kind == "download" {
					owner = r.activity
					break
				}
				parent = r.parent
			}
			if owner != nil {
				owner.Progress.Completed = done
				if total > 0 {
					owner.Progress.Total = &total
				}
				elapsed := now.Sub(record.last).Seconds()
				if elapsed > 0 && done >= record.lastBytes {
					rate := float64(done-record.lastBytes) / elapsed
					owner.BytesPerSecond = &rate
				} else {
					owner.BytesPerSecond = nil
				}
				record.last = now
				record.lastBytes = done
			}
		}
	case "stop":
		if record := t.records[e.ID]; record != nil {
			record.stopped = true
			if record.activity != nil {
				record.activity.State = "verifying"
				record.activity.BytesPerSecond = nil
				record.activity.ElapsedSeconds = now.Sub(record.started).Seconds()
			}
		}
	}
}
func (t *Tracker) Finish(success bool) {
	t.finished = true
	t.success = success
	for _, r := range t.records {
		if r.activity == nil {
			continue
		}
		r.activity.BytesPerSecond = nil
		if success {
			r.activity.State = "completed"
		} else if !r.stopped {
			r.activity.State = "failed"
		} else {
			r.activity.State = "unknown"
		}
	}
}
func (t *Tracker) Snapshot() (Progress, []Activity) {
	t.version++
	now := time.Now()
	p := Progress{SchemaVersion: 1, TaskID: t.task, Version: t.version, ObservedAt: now.UTC(), Phase: "building", Freshness: "live", Builds: t.counts, ActiveActivityIDs: []string{}, Availability: "available", DroppedRecords: t.dropped}
	p.Downloads.ByteBasis = "download"
	p.Downloads.TotalsKind = "known_so_far"
	if t.seen == 0 {
		p.Availability = "unavailable"
	}
	if t.finished {
		p.PlanComplete = t.success
		p.Freshness = "complete"
		p.Phase = "built"
		if !t.success {
			p.Phase = "failed"
		}
	}
	if t.dropped > 0 {
		p.Availability = "incomplete"
	}
	rows := []Activity{}
	latest := map[string]Activity{}
	for _, r := range t.records {
		if r.activity == nil {
			continue
		}
		a := *r.activity
		if !r.stopped {
			a.ElapsedSeconds = now.Sub(r.started).Seconds()
		}
		rows = append(rows, a)
		if a.State == "running" && len(p.ActiveActivityIDs) < 64 {
			p.ActiveActivityIDs = append(p.ActiveActivityIDs, a.ID)
		}
		if a.Kind == "download" && a.StorePath != nil {
			key := *a.StorePath
			if old, ok := latest[key]; !ok || old.Attempt < a.Attempt {
				if ok {
					p.Downloads.RetryBytes += old.Progress.Completed
				}
				latest[key] = a
			} else {
				p.Downloads.RetryBytes += a.Progress.Completed
			}
		}
	}
	var total uint64
	allKnown := len(latest) > 0
	rate := float64(0)
	hasRate := false
	for _, a := range latest {
		p.Downloads.BytesReceived += a.Progress.Completed
		if a.Progress.Total != nil {
			total += *a.Progress.Total
		} else {
			allKnown = false
		}
		if a.State == "running" {
			p.Downloads.ActivePaths++
			if a.BytesPerSecond != nil {
				rate += *a.BytesPerSecond
				hasRate = true
			}
		}
		if a.State == "completed" {
			p.Downloads.CompletedPaths++
		}
	}
	if allKnown {
		p.Downloads.BytesTotal = &total
	}
	if hasRate {
		p.Downloads.BytesPerSecond = &rate
	}
	if len(latest) > 0 {
		n := uint64(len(latest))
		p.Downloads.TotalPaths = &n
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	sort.Strings(p.ActiveActivityIDs)
	return p, rows
}
