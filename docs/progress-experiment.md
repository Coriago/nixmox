# One progress component: Go, go-app and terminal rendering

This is the user-authorized replacement experiment, not a restored gallery or a
production interface. The previous UI implementation remains deleted. Original
htmx/browser specifications are unchanged: go-app is being evaluated here before
making a project-wide architecture decision. No backend engines are started.

## Research

- [Dagger: replacing React with Go/WASM](https://dagger.io/blog/replaced-react-with-go/).
  The useful precedent is sharing expensive event interpretation and interaction
  logic, not merely DTOs. Dagger reports that its Go-app migration removed duplicate
  implementations and made optimizations benefit both interfaces. Its reported
  challenges include large-trace memory usage, JSON decoding and WASM download size
  (32 MB reduced to 4.6 MB with Brotli). Those are their results, not our benchmarks.
- [Current Dagger terminal source](https://github.com/dagger/dagger/blob/main/dagql/idtui/frontend.go)
  consumes `dagui` state and now uses Tuist/Ultraviolet. It would be inaccurate to
  describe today's source as simply a Bubble Tea frontend. This experiment uses
  Bubble Tea for its small, established event loop, not as an exact Dagger clone.
- [go-app](https://github.com/maxence-charriere/go-app/tree/v10.1.11): declarative Go
  DOM components, compiled to WASM. Context7 and pinned source confirm `OnMount`,
  `OnDismount`, `Context.Async` and `Context.Dispatch`: asynchronous work must marshal
  changes back to the UI loop. The timer is stopped on unmount; DOM focus/scroll
  belong to the browser adapter. Its HTTP handler supplies the PWA loader. We serve
  the compiler's matching `wasm_exec.js`, not an assumed compatible bundled version.
- [Bubble Tea](https://pkg.go.dev/github.com/charmbracelet/bubbletea): Model/Update/View
  and `tea.Tick` provide the native event loop. No background mutation of its model.
- [nix-output-monitor](https://github.com/maralorn/nix-output-monitor): borrow the
  information density—concurrent activities, explicit states, transfer counters,
  phases, remote builders, timers—not its rendered ANSI output. A Nix parser is a
  separate concern; the existing task/Nix engines remain untouched.

Pins: go-app 10.1.11, Bubble Tea 1.3.10, Lip Gloss 1.1.0; locked flake Go compiler.
No npm toolchain, CDN, handwritten browser controller or new universal UI framework.

## Exact sharing boundary

```
Batch observations ── Model.Apply ─┐
Action input ──────── Model.Dispatch ├── Model.View ── browser DOM
Frame ticks ───────── Model.Frame ─┘              └─ terminal cells
```

- `internal/ui/progress/model.go`: observation validation/version fencing, stable
  identity/order, selection, expansion, filtering, bounded history and animation.
- `view.go`: the common `View`, `Row`, `Bar`, `Stat`, `Control` and feed models;
  shared labels, counts, rates, durations, unknown states and semantic color tokens.
- `demo.go`: one deterministic observation producer and playback controller used
  by both adapters. It is separate from the reusable component. Pause affects only
  demo playback, never a real task; reset/failure/speed are also fixture controls.
- `browser/component.go`: DOM rendering, native focus, keyboard translation and
  timer lifecycle. All state transitions call the Go controller in WASM.
- `terminal/terminal.go`: the same controller with Bubble Tea keys/ticks, terminal
  width/height handling and clipping. No duplicated progress/interaction reducer.

Both use `Demo.Dispatch(Action)`, `Demo.Advance(delta)` and `Demo.View()`.
Each preview is an independent local playback, not synchronized through a server.

Observations are coalesced to 250 ms at normal playback speed; shared animation
steps are 50 ms. Long suspended-tab gaps are clamped for this simulated clock.
The overall bar measures finished activities, not time remaining. Builds have no
invented percentage; downloads have measured byte totals. Activity state must be
provided explicitly—no lifecycle inference from log text. Real task outcomes,
transport/reconnection, authorization and log-offset recovery are not implemented
by this experiment. Limits are 128 activities, 64 retained feed events, eight
retained log lines per activity and 240 characters per displayed string.

## Preview

```sh
nix run path:.#progress-preview --option substituters https://cache.nixos.org -- tui
nix run path:.#progress-preview --option substituters https://cache.nixos.org -- web
# Browser: http://127.0.0.1:63170/ (loopback only; --port can override)
```

Keys shared by both: Up/Down or j/k select; Enter expands; Space/p pauses; r
restarts; s cycles 1–4× speed; f switches failure/success scenarios; c toggles
completed activities; m toggles reduced motion. The browser also has buttons.
In the TUI, q/Esc/Ctrl+C exits. Small terminals show a selection-following window.
Restart preserves playback pause/speed and motion preference, but resets activity
selection, expansion and filtering. Failure mode restarts the simulation; the
failure appears at simulated 38 seconds, leaving the final dependent blocked.

The package includes WASM, a gzip variant and matching Go runtime assets. Node and
Python are test tools only; neither is needed to run the packaged preview.

## Evidence and limits

- Model tests cover stale/invalid batches, atomic updates, terminal-state guards,
  exact large counters, input ownership/sanitization, unknown totals, retention,
  selection/expansion, pause/reset/failure and shared animation/reduced motion.
- `TestReplayParity` hashes 1,600 complete view frames through every action. The
  identical expected digest is checked natively and under Go/WASM in package checks.
- Terminal adapter tests compare key/tick outcomes against direct controller calls
  and check size bounds. `terminal/testdata/pty_probe.py` exercises the actual
  executable in a PTY, including resize, interactions and alternate-screen cleanup.
- HTTP tests check local assets, gzip negotiation and the unavailable API boundary.
- Initial Chromium probes checked mounting, live updates, pause, selection,
  expansion and focus. `browser/testdata/probe.js` contains the broader repeatable
  browser probe; full browser/load/accessibility validation remains separate.

This demonstrates one bounded component, not large-trace production performance,
complete UI parity across the application, or a finished UI engine.
