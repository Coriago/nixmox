# Reusable interface core

## Scope and decisions

This is stage 3 foundation work, not a CLI command tree, browser application,
TUI screen hierarchy, or cluster validation exercise. Build reusable mechanics
and prove their local contracts with small tests. Feature composition is stage 4;
VM/distributed/failure acceptance remains stage 5.

The authoritative requirements are [interface contracts](api-and-interfaces.md),
[component boundaries](technical-requirements.md#component-contracts),
[editing](configuration-generation.md#live-preview-and-quick-edits), and
[task reads](task-engine.md#interfaces). This design adds no mutation endpoint,
permission, approval shortcut, or alternative source of task truth.

**Share a reusable component vocabulary, semantic models, state transitions and
protocols, with platform-specific renderers.** This is a small application component
kit, not a general-purpose widget/workflow framework. Use the libraries for the
difficult platform mechanics.

- All clients submit the same typed commands through the console admission path.
  Browser HTML handlers call that service; CLI/TUI reach it over HTTP. No direct
  engine calls from a UI. Scope never changes when a connection fails.
- Go presentation code is shared by native CLI/TUI and server-side HTML rendering.
  Only preview/editor rendering is exposed through browser WASM; no WASM SPA.
- Small browser-only glue is unavoidable (DOM, EventSource, editor events).
  Specify and test its protocol behavior against the same fixtures, rather than
  duplicating feature rules or compiling the entire client into WASM.
- Prefer ordinary typed Go functions and small structs. Do not add a generic
  action bus, reflective form generator, second operation catalog, global UI store,
  runtime plugin loader, or general workflow interpreter.

## Research: capabilities, limits and reuse

Context7 libraries used: `/websites/pkg_go_dev_github_com_charmbracelet_bubbletea`,
`/bigskysoftware/htmx/v2.0.4`, `/golang/go/go1.26.0`. Supplemented with the primary
sources below. Existing pins are Bubble Tea 1.3.10, Bubbles 0.21.1, Lip Gloss 1.1.0,
and Go 1.26. Do not accidentally mix their APIs with newer Charm major versions.
The htmx 2.0.4 documentation was a research reference, not a newly bundled asset;
choose and lock the asset version when implementing the browser adapter.

| Library/source | What it supplies | What Nixmox still owns |
| --- | --- | --- |
| [Bubble Tea commands](https://charm.land/blog/commands-in-bubbletea/) and [API](https://pkg.go.dev/github.com/charmbracelet/bubbletea) | Model/Update/View, commands returning messages, Batch/Sequence | Request generations, context cancellation, stale-result guards and task outcome policy. Batch has no result-order guarantee. Sequence is not a domain transaction. |
| [Bubbles](https://github.com/charmbracelet/bubbles/tree/v0.21.1) | Inputs, textarea, viewport, list/table, help/key bindings | Stable component identity, routing messages only to the focused control, retaining the existing control across resize, semantic values/errors |
| [Lip Gloss](https://github.com/charmbracelet/lipgloss/tree/v1.1.0) | Terminal layout/style primitives | Semantic status and accessible text independent of color; no ANSI in domain/view DTOs |
| [htmx synchronization](https://htmx.org/attributes/hx-sync/) | Replace/abort/drop/queue overlapping HTTP requests | Read-result fencing and durable mutation identity. Aborting the browser request does not cancel an accepted task. |
| [htmx preservation](https://htmx.org/attributes/hx-preserve/) | Preserves elements matched by stable ID | It explicitly warns of input focus/caret limitations. Keep the editor outside refreshed regions; do not rely on this attribute alone. |
| [HTML EventSource](https://html.spec.whatwg.org/dev/server-sent-events.html) | Event framing, reconnect, Last-Event-ID transport | Snapshot reconciliation, coalescing and log gap reads. An event ID is not a promise of durable application replay. |
| [Go WASM](https://go.dev/wiki/WebAssembly), [syscall/js](https://pkg.go.dev/syscall/js#FuncOf) | Shared Go execution, JS interop | Ship matching wasm_exec.js from the compiler toolchain. Blocking JS callbacks can block/deadlock the event loop; bound synchronous preview and plan a worker boundary for expensive previews. Release callbacks when their owner is disposed. |
| [html/template](https://pkg.go.dev/html/template), [htmx security](https://htmx.org/docs/#security) | Context-aware HTML escaping; documented attribute/script security controls | Trusted embedded HTML templates only. Never mark log text, repository content or error strings as trusted HTML, or let them produce hx-* attributes. |
| [WAI keyboard practices](https://www.w3.org/WAI/ARIA/apg/practices/keyboard-interface/) | Native control and composite-widget keyboard conventions | Stable logical focus, visible focus/error association, separate selection from focus, predictable return from dialogs |

The htmx SSE extension is designed around HTML swaps. Our protocol is shared JSON
SSE, so a small invalidation bridge is a better fit than a second HTML event stream.
Use server-rendered status/progress fragments; do not mirror their feature state
in browser JavaScript. Avoid selecting a large browser editor or morphing library
until a control probe demonstrates a need beyond an isolated textarea/editor mount.

## Ownership and package boundaries

| Layer | Owns | Does not own |
| --- | --- | --- |
| `domain`, `service` | Wire types, typed payloads, authoritative validation, reviews, capabilities and admission | Widgets, key bindings, DOM, cursor positions |
| `presentation` | Resource items/collections, fields/actions/reviews, metrics/charts, theme tokens, pure editing transitions, async guards, byte-offset log reconciliation | HTTP, files, reading clocks, secrets, goroutines, process execution, terminal or DOM types |
| `console` adapter infrastructure (later) | HTTP client, pending-command persistence, subscriptions, cancellation and running effects | New domain rules or independent scope/approval policy |
| CLI adapter (later) | Argument syntax, stdin/stdout, JSON mode, exit codes and launching shared terminal interactions | Different payload/default/validation semantics or a second set of interactive controls |
| Charm adapter (later) | Bubble Tea command/message plumbing, Bubbles controls, terminal focus/viewport/layout | Reimplementing field codecs or task transitions |
| HTML/JS adapter (later) | Escaped templates, htmx targets, DOM focus, SSE bridge and editor mount | Authorization, a second task store, unpinned source selection |

Keep a few focused files inside `presentation`; split packages only when a real
import/lifecycle boundary requires it. Existing `console/editor.go` is an early
component prototype, not a completed TUI or the definition of the shared core.
Its field parsing, save state and async handling must eventually delegate to these
contracts rather than become a second source of truth.

### Three independent kinds of state

1. **Authoritative observations:** resource versions, task state, reviews, scope,
   server freshness, source commits. Project them; never mutate them optimistically
   into "succeeded" or use an old snapshot to authorize an action.
2. **Interaction state:** raw field text, selected IDs, dirty buffers, local edit
   revisions, in-flight request tokens and pending command IDs. Local to a session.
3. **Platform state:** terminal cell cursor/scroll, DOM caret/selection/IME state,
   measured widths and accessibility focus. Owned by the adapter, keyed by stable
   field/file IDs. Terminal cells, UTF-8 byte offsets and DOM UTF-16 positions are
   different units and must not share a misleading integer cursor contract.

No progress event or collection refresh may replace unsaved interaction state.
No local edit revision is interchangeable with a domain version or telemetry
sequence. Observation freshness and connection state are distinct: a reconnected
socket does not make an old observation live.

## Extension without duplicating feature workflows

When adding a feature later:

1. Define the domain payload and register the operation once in `service.Catalog`.
2. Add a typed projection into the shared resource item/detail components, plus a
   semantic interaction descriptor: operation key, field metadata, labels/help,
   primary resource, review projection, typed input binding, and result links.
   A new domain model normally needs different data/columns/actions, not new list,
   status, selector, detail or confirmation implementations.
3. Bind to the existing payload type, then use the registry/admission service.
   Do not maintain a parallel JSON schema or switch on every operation in each UI.
   Reflection cannot infer mutual exclusions, destructive reviews or safe defaults.
4. Each adapter renders supported semantic fields and the same disabled reasons.
   Unsupported controls are explicit capabilities, not hidden safety requirements.
5. Run shared binding/projection fixtures; add a platform control only if existing
   Bubbles/native HTML controls genuinely cannot express the interaction.

Template fields reuse `domain.Field` and the existing renderer. Command-specific
selectors/reviews compose additional semantic projections; they are not fabricated
as editable template fields. A host/disk selector carries an opaque ID and evidence,
not a label parsed back into identity. Secret controls are a separate ingress;
secret values never enter general form values, pending commands, previews or traces.

Presentation may explain why an action is unavailable; the service rechecks all
facts. The existing hard-coded `Shared.HostActions` is a provisional projection,
not an exhaustive authoritative feature availability table.

## Shared component kit

These contracts describe data and behavior; HTML, terminal and plain-text renderers
choose the physical arrangement. Prefer composition over a different bespoke view
model for every domain model. Do not flatten domain objects into untyped bags or
require every resource to implement a UI interface.

### Resource lists, items and detail blocks

A typed projector converts a domain snapshot into a small common `ResourceItem`:

- **Identity:** resource kind, opaque ID and scope; name/title and optional subtitle.
  Scope is part of the key so different endpoints/clusters cannot share cached rows.
- **Statuses:** several labeled status badges, each with a semantic tone and optional
  explanation/freshness. Membership, reachability and health remain separate facts.
- **Facts:** ordered labeled values (text, exact ID/reference, timestamp, count,
  amount/unit, status or progress), with importance and optional detail links.
- **Metrics:** optional current metric/sparkline projections from already selected
  telemetry, not automatic per-row sampling or subscriptions.
- **Actions:** the same action descriptors/disabled reasons used in details/reviews.

A `CollectionSpec` supplies labels, available columns/cell kinds, column priority,
filter/sort capabilities, selection mode and empty-state explanation. The same item
can become a wide table row, narrow two-line item/card, searchable picker result,
or plain CLI row. Reuse its facts/statuses in a detail summary. Keep full IDs in
identity/copy/selection actions even when displayed text is shortened.

| Resource | Example shared item configuration |
| --- | --- |
| Host | Name/ID; membership, reachability and health badges; assigned configuration, environment, last seen; optional already-watched metric summaries |
| Repository | Name/ID; remote and upstream branch/commit, fetched time; explicit synchronization observations only when available, not inferred from an old fetch |
| Credential | Name/ID; kind, repository scope and holder/replica states; no value. Stored replicas do not prove that remote authentication currently works. |
| Task | Kind/ID; state and phase; target reference, created time, progress and eligible actions |
| Configuration or future model | Name/ID, appropriate status/facts/references and registered actions, using the same collection and detail renderers |

Collection state owns selection by resource key, query generation, cursor/pinned
revision, loaded pages, loading/error state and counts with known/unknown coverage.
Preserve selection and focus through item updates; never reinterpret an old row
index as a newly selected resource. Handle disappearance explicitly. Do not reorder
an actively navigated list on each metric tick. Semantic sorting uses underlying
numbers/timestamps, not human-formatted strings.

Only expose server-supported global filters/sorts. Local filtering of loaded pages
must say that it covers loaded items, not pretend to search the whole collection.
Likewise, loaded-item counts are not total counts. Do not infer bulk mutation or
select-all-across-pages authorization from a selection widget.

Reuse Bubbles `list.ItemDelegate` for selectable items and `table` for dense tables;
common item renderers supply the content. Its pinned `SetItems` returns a filtering
command and index selection must be restored from the resource key. HTML uses the
same columns/facts as semantic tables or lists, rather than terminal-formatted text.

### Telemetry graphs and metric components

Provide `MetricValue`, `CapacityBar`, `TimeSeries` and `ChartSpec` primitives.
A chart specifies series IDs/labels, units, timestamps, nullable samples, scale
policy, freshness and theme roles; it never collects measurements or derives
health/approval authority. Current values, legends and a textual equivalent are
part of the component, not optional decorations on a graph.

Use two graph families initially: **capacity bars** and **time-series lines**, with
sparklines as a compact line rendering. Avoid pie/donut charts and speedometer
widgets: they compare poorly at small terminal sizes and add little information.

| Measurement | Default rendering and scale |
| --- | --- |
| CPU utilization | Current percent plus line/sparkline; fixed 0–100% axis from the normalized 0–1 sample |
| Load average | Labeled 1/5/15-minute values; optional three-series line on a nonnegative count axis. Logical CPU count may be a labeled reference, never a percent conversion or automatic health verdict. |
| RAM | Used/total capacity bar with available bytes labeled; used-percent trend on 0–100%. Do not invent cache/buffer segments absent from the schema. |
| Swap | Separate used/total bar and optional trend. Known total zero means "not configured", not 0/0%; missing data remains unknown. |
| Network | RX/TX paired lines per interface, nonnegative bytes/s with common units/scale and distinct labels/styles; no fabricated link-utilization percentage without link capacity |
| Disk I/O | Read/write paired lines per device, bytes/s, same policy as network |
| Filesystems | Per-mount used/total bars plus available bytes and independent sample age; do not assume used + available = total because of reserved space |
| Uptime | Duration value with freshness, not a graph; freeze the displayed value on stale data |

Shared series logic owns a bounded rolling window, discontinuities, deterministic
bucketing, scale policy and labels. Pass observation/current time in explicitly;
no background sampler or wall-clock reads in `presentation`. Retain at most the
specified **five minutes of viewed history**, with a sample/series count bound as
well. This is an ephemeral view buffer, not a telemetry database or new history API.

- Use executor-provided rates; do not rederive them from coalesced UI events.
- Null/reset/unavailable measurements are gaps, never zero. Disconnection and
  accepted stream/epoch changes break or reset series; do not join unrelated runs.
  A stream change follows snapshot/session policy, not guessed membership changes.
- Preserve real observation timestamps. Fast samples must not add artificial new
  filesystem samples or renew their freshness. Reject duplicate/older samples;
  clock regressions produce a discontinuity rather than backward line segments.
- Bucket by elapsed time, not sample index. Bound output by available graph width;
  retain min/max extrema when downsampling so short spikes are not averaged away.
  Preserve gap markers. Label downsampled data; the latest value remains the actual
  latest observation, not a bucket mean.
- Fixed percent scales permit meaningful comparisons. Rates use a zero baseline
  and a shared deterministic nice-ceiling policy, expanding immediately and
  shrinking conservatively to avoid a jumping axis; show scale changes and units.
  Do not stack RX/TX or sum interfaces/devices, which can double-count activity.
- Subscribe only for explicitly watched hosts. A large host list must not create a
  sampler/subscription per item. Metric slots without a selected sample show an
  honest unavailable state. View disposal releases history/subscriptions.

The browser renderer should initially produce small server-rendered SVG graphs
from the same chart projection, using themed axes/series and a labeled value/table
alternative. Graphs stay outside editor swap targets. No browser-wide chart state
framework, mandatory chart WASM or external chart service is needed.

Terminal renderers use sparklines for compact rows and line plots when space permits;
CLI watch mode can mount the same component as the TUI. Plain/noninteractive output
uses labeled values and optionally a static compact graph, never cursor animation.
Evaluate `NimbleMarkets/ntcharts` for terminal drawing instead of writing a plotting
engine. Its v0.4.0 go.mod uses the same Charm v1 generation; this is a candidate,
not a new dependency pin or a compatibility claim. Probe missing-data segments,
fixed axes, bucketing and resizing before adoption; do not let library defaults
silently interpolate gaps or change the shared scale policy. JSON stays raw API data.

### Other composable components

| Component | Reused behavior |
| --- | --- |
| Status badge + freshness marker | Label, tone, observed time/age and explanation; status is not just a colored dot |
| Labeled facts / detail sections | The same typed cells and references used in resource items, arranged as metadata groups |
| Resource reference / source identity | Consistent name, exact ID/commit, owner/scope and navigation/copy representation |
| Searchable single/multi-select picker | Resource items, selection and eligibility/disabled reasons; selected IDs survive refresh |
| Action group / contextual help | Registered action names, availability, help and platform-specific shortcuts |
| Feedback / empty state | Loading, empty, no matches, partial results, unavailable, disconnected and retry explanation; distinguish these cases |
| Review / confirmation panel | Labeled facts, warnings, exact versions and explicit acknowledgements; never infer approval from dismissal |
| Task phase / progress panel | Shared phase, known/unknown counters, activity rows and links; receipt acceptance is distinct from completion |
| Diff summary / change list | Structured added/removed/changed/ambiguous items with a legend; reuse list cells and detail sections |
| Form / editor / log panel | Existing shared field, buffer and log behavior; adapters own only controls and physical arrangement |

Avoid a giant component union with arbitrary embedded HTML/ANSI callbacks. Add a
new semantic cell or component only when existing composition cannot express it.

### Interactive CLI reuses the terminal kit

The CLI may launch an inline or temporary Bubble Tea interaction: a resource picker,
form, review panel, progress watcher, log viewer or telemetry chart. These are the
same models/controllers and terminal renderers embedded in the full TUI, not a
separate prompt library with separate validation or color rules. Full-screen TUI
navigation is just another owner/composition of those components.

A common terminal runner owns input, cancellation, effects and cleanup; do not nest
independent Bubble Tea programs inside a running TUI. A component returns a typed
selection/input/review result to its owner, which uses the shared command path.
CLI-specific policy decides when to prompt, detach or return an exit code.

Respect input/output capabilities: do not consume piped file/secret input as prompt
keystrokes, probe a redirected terminal, or prompt in --json/noninteractive mode.
Without an interactive terminal, return the documented validation/approval outcome.
Cancelling the interaction stops waiting, not an already admitted task. Noninteractive
text remains useful without color, Unicode graphs or full-screen controls.

### One semantic theme across all renderers

Use **one canonical theme definition**, planned as embedded
`internal/presentation/theme.json`, validated into typed theme values. It contains
light/dark variants and explicit terminal fallbacks. CSS variables and Lip Gloss
styles are derived from this source, not maintained as separate hand-written palettes.
The same resolved theme feeds TUI and interactive/static CLI rendering.

Token groups:
- Surfaces: background, elevated, selected; text: primary, secondary, muted,
  on-accent; borders: normal, focus; interaction: accent, selected, disabled.
- Status: success, warning, danger, info, pending, unknown and stale. Tone does not
  replace status text or supply new domain health thresholds.
- Charts: grid, axis, label and stable series roles (including RX/TX and read/write).
  Series identities keep their style across refresh/reordering; data-series colors
  are not implicitly health colors. Pair colors with legends and line patterns.
- Change views: added, removed, changed and ambiguous. All include textual markers.

The browser consumes generated CSS custom properties; SVG uses those same properties.
Terminal adapters use renderer-local Lip Gloss styles and its pinned v1.1.0
`CompleteAdaptiveColor`/`CompleteColor` support for truecolor, ANSI256 and ANSI.
Do not copy v2-only APIs into the v1 stack or mutate a process-global renderer.
For the same variant, truecolor uses the same RGB values; reduced-color terminals
preserve semantic intent, not an impossible guarantee of identical RGB appearance.

Theme selection is explicit light/dark or environment-appropriate auto, independent
of domain state. Honor NO_COLOR and terminal capability; JSON is always unstyled.
An explicit color preference can override automatic selection, never contaminate
JSON. Avoid terminal capability queries for noninteractive/redirected output.
Native HTML controls, labels, patterns and readable no-color output remain valid
when styling is absent. Browser contrast targets are at least 4.5:1 for normal text
and 3:1 for essential graph/focus indicators; check palette pairs, not just swatches.
User-configured ANSI palettes cannot guarantee exact contrast, so retain non-color
cues and allow explicit variant/profile selection.

Changing a token updates every consumer built from that theme. Future runtime theme
selection is a shared presentation preference, not three independent settings systems;
if changed while an interface is open, restyle existing controls without recreating
buffers, selections or task state. Do not add a settings endpoint in this core pass.

Additional research: [Bubbles v0.21.1 list source](https://github.com/charmbracelet/bubbles/blob/v0.21.1/list/list.go),
[Lip Gloss v1.1.0 colors](https://github.com/charmbracelet/lipgloss/blob/v1.1.0/color.go),
[ntcharts](https://github.com/NimbleMarkets/ntcharts),
[ntcharts v0.4.0 dependencies](https://github.com/NimbleMarkets/ntcharts/blob/v0.4.0/go.mod),
[WAI use of color](https://www.w3.org/WAI/WCAG22/Understanding/use-of-color.html),
[WAI non-text contrast](https://www.w3.org/WAI/WCAG22/Understanding/non-text-contrast.html).
Context7 was queried for Lip Gloss, Bubbles and ntcharts; returned examples included
newer major versions, so pinned local sources were checked for the list and color
APIs above. ntcharts remains a renderer candidate pending a small compatibility probe.

## Reusable behavior contracts

### Fields, errors and reviews

- Preserve raw incomplete input separately from the last successfully decoded
  typed value. `-`, an empty integer or unfinished JSON must remain editable.
- Share field decoding across adapters. Integer values use `json.Number`/int64,
  not float64. Booleans are explicit true/false; absence and false are distinct.
  Multiselect is a list, never an ambiguous comma-separated string.
- Decoding is not semantic validation: required/default/choice/cross-field rules
  remain in the shared renderer or feature validator, and repeat server-side.
- Errors carry stable field/file keys and codes. Presentation supplies labels and
  explanations; neither UI nor automation extracts authoritative status from logs.
- Review projections retain exact IDs, bindings, expected versions and required
  acknowledgements. Reading, closing, refreshing or generic --yes never approves
  a destructive review. A conflict preserves input and offers explicit re-review.

### Asynchronous reads and preview

Use a small request gate per replaceable result slot, not one global request ID.
Tokens contain session generation plus request sequence. Navigation, source/template
change or filter change invalidates the generation. A completion is applicable only
if its generation and latest sequence still match. Cancellation saves work; token
checks establish correctness even if cancellation loses a race.

Separately track resource versions for snapshot/update merging. On a gap, refresh
rather than trying to reconstruct truth by counting events. Subscribe first, then
read a snapshot; retain bounded invalidations while the snapshot is in flight. If
that bound is exceeded, mark dirty and refresh rather than dropping a final update
silently. Telemetry additionally checks membership epoch, stream ID and sequence;
never replace those server-owned rules with a generic version comparator.

One stream per active console/tab; derive topics from visible subscriptions. Dispose
subscriptions when the owner closes. Coalesce latest state within the specified
250 ms bound, but never coalesce log bytes as though they were gauge samples.
Keep safety refresh independent of live notifications. Request gates are for reads
and previews, not a "latest mutation wins" policy.

### Submission and uncertain outcomes

The future client controller should implement this small explicit protocol, without
network calls inside a pure reducer:

`prepared -> pending identity persisted -> sending -> accepted | rejected | unknown`

Unknown transitions to a receipt query, not a fresh command. Retry only with the
same frozen command ID/payload/versions. Definitive rejection requires explicit
editing/review before a new intent. Transport retries cannot resolve a newer source
or replace expected versions. Preserve pending identity before sending, not after
receiving a response. Do not persist secret bodies. Reads of a receipt may themselves
be unavailable; keep the outcome unknown rather than treating that as rejection.

Acceptance does not mean execution success. Client request cancellation or leaving a
screen stops waiting, not the task. Task cancellation is an explicit command with
its own preconditions. CLI waiting/JSON exit policy consumes the same task/review
projection; raw JSON output remains the API representation, not formatted view DTOs.

### Editor save and regeneration

Track independently: pinned template/source identity, local edit revision, saved
revision/version, generated baseline, manual content, proposed regeneration, and
validation bound to the exact saved version. Preserve manual content when form
changes generate a different proposal. Removal/rename also requires review.

A save captures the submitted local revision. Completing that save must not clear
Dirty if the user edited again meanwhile. HTTP 202 is not a saved-file acknowledgement;
use the task/result's exact saved version. A version conflict retains buffers and
shows a separately loaded server version, never silently rebases expected versions.
Saving invalid draft text is different from validating it for commit. Preview
results are non-authoritative and cannot certify Nix validity or authorize effects.

Keep invalid preview results from replacing the last coherent file set. Use request
and source identity, not just a small revision counter that can repeat on reopening.
Do not reverse-map arbitrary edited Nix into form values. Cursor/IME state stays in
the control; refresh only content whose accepted semantic version actually changed.

### Logs and safe text

A log cursor is scoped to owner/task/optional activity. Advance by original byte
count, not characters or rendered text. Ignore duplicates, trim overlaps, and return
a gap-read request for missing bytes. Do not buffer an unbounded out-of-order queue.
An explicit retained-start/truncation response can skip unavailable bytes with a
visible marker; a disconnected owner cannot be treated as EOF.

Incremental UTF-8 decoding must retain an incomplete rune across chunks. Escape
terminal control sequences (including OSC/clipboard controls) and directional
formatting controls in displayed text; use text nodes/HTML escaping in the browser.
Never reinterpret logs as HTML, ANSI UI instructions, task completion or reviews.
Keep raw downloads and offset arithmetic separate from safe displayed text.

`LogChunk.complete` means the source log is closed, not that this bounded page
contains its end. Drain pages; an underfilled/empty closed read establishes EOF.
When a full page is returned, another read may still be necessary. Keep view tails
bounded independently of the cursor; a large log must not become a large model.

### Browser numeric precision

Go versions, sequences and byte offsets can exceed JavaScript's safe integer range.
The wire contract is not silently changed to strings. The future event bridge must
use lossless decoding for comparison/offset fields (with decimal BigInt comparison),
or treat resource events only as invalidations and never install rounded values.
Log offsets still require lossless handling. Raw integer form text must reach the
shared codec before any JavaScript Number conversion. Add large-integer fixtures
when selecting the bridge decoder; do not solve this by shipping a global WASM UI.

## Work slices and evidence

1. **Research/design (this pass):** decisions above, boundaries, failure cases and
   extension path; no screens, command verbs, HTTP routes or bundled htmx assets.
2. **Planned portable component slice:** typed cells/statuses, resource items and
   collection state, canonical theme tokens, metric/series/chart projections and
   bounded history; shared field decoding, request guards and log recovery. These
   additions are planned here, not claimed implemented by this document change.
   Use focused Go tests and compile preview WASM to check dependency boundaries.
3. **Planned interaction slice:** editor save/validation/regeneration transitions,
   immutable submission intent/outcome reducer, shared review/progress projections
   and paginated selection/version guards. Small fake effects, not feature workflows.
4. **Adapter probes (when core contracts are stable):** reusable Charm controls
   embedded both in a CLI interaction and a TUI harness; browser editor/htmx race
   harness; SVG/terminal graph rendering from common fixtures and theme changes.
   They are component tests, not actual pages. Verify native/WASM preview fixtures with the
   matching runtime, rather than treating a successful WASM compile as parity proof.
5. **Application composition (stage 4):** actual CLI verbs, browser pages, TUI
   navigation, gateways/assets and real feature bindings. VM/cluster validation is
   not a prerequisite for any of the reusable interface work above.

Unit tests should target observable invariants: same input -> same typed value and
field errors; old completion cannot replace newer data; duplicate/gapped logs do not
corrupt offsets; invalid intermediate edits survive; delayed saves do not erase newer
edits; unknown mutation outcomes cannot turn into implicit retries or success.
Add component fixtures proving that different resource projectors use the same list
kit, selection survives reorder/removal, graph gaps/resets and null/zero semantics
are preserved, filesystem freshness is independent, history stays bounded, and theme
changes reach CSS/terminal/chart renderers without resetting interaction state.
Test light/dark, truecolor/reduced-color and no-color representations. CLI and TUI
harnesses should exercise the same component rather than compare separate workflows.

Do not build an all-purpose UI framework to anticipate hypothetical interfaces.
A future adapter implements the existing semantic/control and client ports; a new
feature extends its typed descriptor and service registration, not three workflows.
