package progress

import (
	"fmt"
	"time"
)

// Demo is a deterministic input source and playback controller, not a second
// implementation of the component. Both adapters use these exact methods.
// Replace this with task observations later; Model itself never simulates work.
type Demo struct {
	Model           *Model
	paused, failure bool
	speed           int
	clock, pending  time.Duration
	sample          uint64
}
type planned struct {
	name, host    string
	kind          Kind
	start, length int
	mib           uint64
}

var plan = []planned{
	{"glibc-2.40", "cache.nixos.org", Download, 0, 9, 78},
	{"gcc-14.2.0", "cache.nixos.org", Download, 2, 20, 174},
	{"rustc-1.85.0", "cache.nixos.org", Download, 4, 26, 220},
	{"zlib-1.3.1", "cedar · local", Build, 9, 11, 0},
	{"openssl-3.4.1", "builder-02 · remote", Build, 13, 16, 0},
	{"linux-modules-6.12", "builder-02 · remote", Build, 20, 30, 0},
	{"nixmox-agent", "cedar · local", Build, 30, 23, 0},
	{"nixos-system-cedar", "cedar · local", Build, 53, 7, 0},
}

func NewDemo() *Demo { d := &Demo{speed: 1}; d.reset(); return d }
func (d *Demo) reset() {
	reduced := d.Model != nil && d.Model.reduced
	d.Model = New("Building cedar", "nixosConfigurations.cedar · x86_64-linux")
	d.Model.reduced = reduced
	d.clock = 0
	d.pending = 0
	d.sample = 0
	d.observe()
}

// Advance uses fixed animation steps and 250ms observations. Long suspended-tab
// gaps are clamped; this is playback, not an authoritative task clock.
func (d *Demo) Advance(delta time.Duration) {
	if d.paused || delta <= 0 {
		return
	}
	d.pending += min(delta, 500*time.Millisecond)
	for d.pending >= FrameInterval {
		d.pending -= FrameInterval
		d.clock = min(d.clock+FrameInterval*time.Duration(d.speed), 60*time.Second)
		if uint64(d.clock/(250*time.Millisecond)) >= d.sample {
			d.observe()
		}
		d.Model.Frame()
	}
}
func (d *Demo) Dispatch(a Action) {
	switch a.Kind {
	case Pause:
		d.paused = !d.paused
	case Restart:
		d.reset()
	case Speed:
		d.speed = d.speed%4 + 1
	case Failure:
		d.failure = !d.failure
		d.reset()
	default:
		d.Model.Dispatch(a)
	}
}
func (d *Demo) View() View {
	v := d.Model.View()
	label := "Pause demo"
	if d.paused {
		label = "Resume demo"
	}
	scenario := "Inject failure"
	if d.failure {
		scenario = "Success scenario"
	}
	v.Controls = append([]Control{{Pause, label, "space", d.paused}, {Restart, "Restart", "r", false}, {Speed, fmt.Sprintf("Speed %d×", d.speed), "s", d.speed != 1}, {Failure, scenario, "f", d.failure}}, v.Controls...)
	if d.paused {
		v.Status = "PAUSED DEMO"
		v.Tone = "warning"
	}
	return v
}
func (d *Demo) observe() {
	tick := uint64(d.clock / (250 * time.Millisecond))
	d.sample = tick + 1
	batch := Batch{Sequence: tick + 1, At: d.clock}
	for i, p := range plan {
		elapsed := d.clock - time.Duration(p.start)*time.Second
		a := Activity{ID: fmt.Sprintf("activity-%d", i), Name: p.name, Host: p.host, Kind: p.kind, State: Queued, Phase: "waiting for dependencies"}
		if p.kind == Download {
			a.Known = true
			a.Total = p.mib << 20
		}
		if elapsed >= 0 {
			a.Elapsed = min(elapsed, time.Duration(p.length)*time.Second)
			ratio := min(1, float64(elapsed)/float64(time.Duration(p.length)*time.Second))
			a.State = Running
			if p.kind == Download {
				a.Phase = "substituting"
				// Piecewise rates vary deterministically; counters remain exact integers.
				weights := []uint64{8, 12, 6, 16, 14, 8, 10, 6}
				segmentMS := uint64(p.length * 1000 / len(weights))
				ms := min(uint64(elapsed.Milliseconds()), uint64(p.length*1000))
				segment := min(7, int(ms/segmentMS))
				var units uint64
				for j := 0; j < segment; j++ {
					units += weights[(j+i)%8] * segmentMS
				}
				weight := weights[(segment+i)%8]
				units += weight * (ms - uint64(segment)*segmentMS)
				a.Done = a.Total * units / (80 * segmentMS)
				a.BytesPerSecond = float64(a.Total*weight*1000) / float64(80*segmentMS)
				a.Logs = []string{"GET https://cache.nixos.org/…nar.xz", "Verifying signed narinfo · streaming compressed payload", fmt.Sprintf("Received %d bytes", a.Done)}
			} else {
				phases := []string{"configure", "build", "check", "install"}
				a.Phase = phases[min(3, int(ratio*4))]
				n := int(elapsed.Seconds() / 2)
				for line := max(0, n-3); line <= n; line++ {
					a.Logs = append(a.Logs, fmt.Sprintf("[%03d] %s: %s", line+1, p.name, []string{"checking build inputs", "compiling source objects", "linking outputs", "checking reproducibility"}[line%4]))
				}
			}
			if ratio >= 1 {
				a.State = Succeeded
				a.Phase = "output available"
				a.BytesPerSecond = 0
			}
		}
		if d.failure && i == 5 && elapsed >= 18*time.Second {
			a.State = Failed
			a.Elapsed = 18 * time.Second
			a.Phase = "check phase failed"
			a.Logs = []string{"Running module compatibility checks", "error: expected symbol missing in fixture module", "builder exited with code 1 · simulated failure"}
		}
		if d.failure && i == 7 {
			a.State = Queued
			a.Elapsed = 0
			a.Phase = "blocked by linux-modules failure"
			a.Logs = nil
		}
		batch.Activities = append(batch.Activities, a)
	}
	d.Model.Apply(batch)
}
