# Simplified Experiment Visualization: Anchored Element Popover, Compact Shell, Clear Run Controls

## Goal Description

Simplify the DistVis experiment visualization view (`public/index.html`, `public/app.js`, `public/style.css`) so that the visualization takes up most of the screen and every action is close to the thing it acts on. The work covers all six user requirements in the original idea:

- **R1** Make the interface simpler and show less unneeded information: remove duplicated and decorative information.
- **R2** Put each node or edge's configuration and fault injection in **one** dialog. Remove the two tabs, and never ask the user to pick the target again inside fault injection.
  - **User decision:** the dialog is a **floating popover anchored next to the clicked node or edge**.
  - On phones (≤700px) it becomes a bottom sheet.
- **R3** Make the header smaller so the visualization gets more room.
- **R4** Make the start/end experiment buttons easy to find.
- **R5** Make node input easier to see.
- **R6** Remove the left sidebar or hide it automatically.

**Primary direction (from the draft):** a selection-driven, unified element surface.
- A selection is either a node or a link.
- The surface shows state, app input and faults for whatever is selected.
- The fault target comes from the selection, so the "Node details / Fault injection" tabs and the `#fault-kind`, `#fault-node`, `#fault-from`/`#fault-to` selects are removed.

**User's choice of form:** the floating popover (draft Alt-3), not the docked panel recommended by both Claude and Codex.

**Folded in from the draft's alternatives, as the synthesis notes suggested:**
- the Alt-1 compact shell, drawer sidebar and resizable topology
- the Alt-2 control strip driven by run state
- the Alt-3 on-graph input badge
- Alt-4 information pruning
- Alt-5 measurable layout budgets

**Scope clarification (user decision):** "node configuration" means app input, node faults and link settings only. Per-node program or code configuration stays in the protocol workspace.

**Behaviour clarification (user decision):** faults and app inputs stay available during replay.
- They always act on the live run.
- Their state comes from live data and is clearly labelled "Live".

This matches the documented rule in `README.md`: faults and writes always act on the current live experiment.

**No server fault or command API changes are required.** The one server change is a `lifecycle {action:'failed'}` event on startup failure.

## Acceptance Criteria

Following TDD philosophy, each criterion includes positive and negative tests for deterministic verification.

- AC-1: One anchored popover replaces the tabbed inspector and the fault panel.
  - Positive Tests (expected to PASS):
    - Clicking a topology node opens a popover next to that node containing, in order:
      1. the header (name, live online badge)
      2. Application input
      3. a single crash/recover toggle
      4. the peer-link list
      5. replay state rows
      6. a collapsed raw JSON disclosure
    - Clicking a topology edge (`.link-hit`) opens the same popover in link mode for that pair. The endpoints are already filled in and no target select is shown.
    - Crashing a node takes exactly two interactions: click the node, then click "Simulate crash".
    - Clicking a different node or edge while the popover is open switches its content in place.
    - Escape, the close button, or a click on empty graph background closes the popover.
  - Negative Tests (expected to FAIL):
    - Any of these still exists in the DOM: `.inspector-tabs`, `[data-tab="fault"]`, `#fault-kind`, `#fault-node`, `#fault-from`, `#fault-to`.
    - A link fault that requires choosing source/target nodes from a dropdown.
    - The popover is a descendant of `#graph` or `#spacetime`, so it would be destroyed by `innerHTML` redraws.
  - AC-1.1: Anchoring and redraw stability.
    - Positive:
      - The popover stays next to its anchor after live redraws, playback, window resize and space-time panning.
      - It flips side or clamps so it stays inside the visualization area.
      - It never covers its own anchor element.
    - Negative:
      - The popover flickers, loses focus or resets when `#graph` is rebuilt during a live run.
      - The popover extends past the viewport and causes page-level horizontal scroll.
  - AC-1.2: Opening from the space-time view.
    - Positive: clicking a space-time lane label opens the node popover anchored to that label. Its peer-link list gives access to link mode.
    - Negative: link faults can only be reached from the topology view.

- AC-2: Link rule direction semantics are explicit and never destroy data silently.
  - Positive Tests:
    - Link mode shows the live rule for both directions (A→B and B→A: latency, bandwidth, blocked, or default).
    - A direction control offers A→B | B→A | Both.
      - Opening from a graph edge defaults to lower-index → higher-index.
      - Opening from the peer list defaults to selected-node → peer.
    - Editing only A→B leaves an existing different B→A rule unchanged.
    - When "Both" is chosen and the two rules differ, a visible notice says both directions will be overwritten with the same settings.
    - Latency (0–30000) and bandwidth (1–100000) accept only integers within range.
  - Negative Tests:
    - Applying A→B changes B→A.
    - Empty or out-of-range values are sent to `/api/runs/:id/faults`.

- AC-3: Heal and fault history are always reachable.
  - Positive Tests:
    - A persistent "Network" control in the run control strip shows "Heal all links" (`heal`) and the last faults, whether or not a popover is open.
    - The heal explanation is visible text: "Resets all link latency/bandwidth/blocking to defaults; does not recover crashed nodes".
  - Negative Tests:
    - Heal is reachable only through a specific selection state.
    - The heal meaning is available only via a `title` tooltip.

- AC-4: Mutations use live state and never replay state.
  - Positive Tests:
    - While replay is paused at a time when node-2 was online but node-2 is currently crashed live, the popover's toggle shows "Recover node", marked "Live".
    - The state rows in the same popover show replay-time values, labelled "Replay @ mm:ss".
    - Link-mode prefill, peer-link status and fault history come from live state.
    - The latest `command_result` for the node is shown from live events regardless of the replay cursor.
    - Replay and live snapshots use one shared pure event reducer.
  - Negative Tests:
    - Any mutation control reads the replay `snapshot()` cache for its enabled state, toggle choice or prefill.
    - A command error does not appear until the cursor reaches it.

- AC-5: Node input is prominent.
  - Positive Tests:
    - Nodes whose live input schema declares at least one action show an input badge (⌨) on the topology node card.
    - Activating the badge opens the popover with focus in the first input field.
    - Application input is the first content section of the node popover.
    - With more than one action, a compact action chooser shows one expanded form.
    - When the run is not running or the node is offline, submit is disabled and the reason is shown as visible text.
  - Negative Tests:
    - A badge appears on a node with no declared actions.
    - A disabled submit has no visible reason.
    - Every form of a 64-action schema is expanded at once.

- AC-6: Drafts, focus and pending state survive live updates and navigation races.
  - Positive Tests:
    - Typed input values, caret position, IME composition and open disclosures survive live event redraws.
    - Drafts are keyed by run plus node plus action for inputs, and by run plus canonical pair (`min>max`) plus direction for links.
    - When a schema changes, draft values are kept for fields whose name and type still exist.
    - If the live link rule changes while a link draft is dirty, the draft is kept and "Live rule changed · use latest values" is offered.
    - Pending state is keyed per target. While any fault request is in flight, all fault controls are disabled, matching the server's run-level mutation lock. A 409 shows "Previous fault operation still in progress".
    - Drafts survive a failed request.
    - Every mutation captures `{loadGen, runId, targetKey}`. Result notices and inline renders apply only if all three still match.
  - Negative Tests:
    - A live redraw overwrites a field the user is editing.
    - A delayed response from a previous run, or from a previously selected target, clears drafts, clears pending state or shows an inline result on the current target.

- AC-7: The run control strip makes start and end obvious in every run state.
  - Positive Tests:
    - One strip at the top of the visualization panel holds a primary button, a status badge, the "Network" control, and a visually separate playback group (rewind, play, step, speed, Live/Replay toggle, timecode, timeline).
    - The primary button follows this precedence:
      1. The inspected run is active:
         - starting → "Starting…" (disabled)
         - running → "End experiment" (danger style; targets only the inspected run)
      2. The inspected run is not active but any run is active (same or another experiment, including starting) → "Go to running experiment".
      3. Nothing is active:
         - no run → "Run experiment"
         - completed or interrupted → "Run again" (current experiment settings plus current saved protocol revision; opens `#new-dialog`)
         - failed → "View error" plus "Run again"
    - The client maps lifecycle actions explicitly: start → running, stop → completed, failed → failed.
    - The server emits `lifecycle {action:'failed'}` when startup fails, so the failure shows without waiting for the poll.
    - Lifecycle events update `run.status` and the cached active run immediately.
    - Stale `refreshRuns` responses (older request sequence) are discarded.
    - A terminal status is never overwritten by a stale non-terminal one.
  - Negative Tests:
    - "End experiment" is enabled while the status is starting.
    - Viewing a historical run while another run of the same experiment is active offers "Run again".
    - A `failed` lifecycle event is shown as "Ended".

- AC-8: Compact shell and visualization space budget. These are hard requirements with ±4px tolerance.
  - Positive Tests (1440×900, experiment visualization view, simulation run):
    - All chrome above the graph area (header row plus control strip) is ≤104px tall in total.
    - The graph container height is ≥60% of the viewport height.
    - The primary run button is fully visible without scrolling.
  - Positive Tests (390×844):
    - Page `scrollWidth` ≤ `innerWidth`.
    - Graph container height ≥300px.
    - The collapsed or closed bottom sheet occupies ≤56px.
    - Interactive targets in the strip and sheet are ≥40px.
  - Negative Tests:
    - `.workspace-heading`, `.experiment-bar`, `.metrics` and the graph heading are still stacked as separate rows above the graph.
    - Graph height is still derived from a hard-coded chrome estimate such as `calc(100vh - 480px)`.

- AC-9: The sidebar auto-hides in the visualization view without losing navigation.
  - Positive Tests:
    - In the experiment visualization view the sidebar's rendered width is 0.
    - A ☰ toggle in the header opens it as an overlay drawer containing My protocols, New protocol, Recent protocols and API docs.
    - Library and protocol views still honour the saved `distvis-sidebar-collapsed` preference.
    - Opening or closing the temporary drawer does not change that saved value.
  - Negative Tests:
    - Entering the visualization view writes to `distvis-sidebar-collapsed`.
    - `#nav-guide` or the recent protocols cannot be reached from the visualization view.

- AC-10: Topology geometry follows the container.
  - Positive Tests:
    - A ResizeObserver on the graph container recomputes the viewBox and node layout. Resize callbacks are coalesced with requestAnimationFrame.
    - Layouts for 2, 5 and 12 nodes keep node cards at least 76×52 with label clearance.
    - If 12 nodes cannot fit at the minimum size, the graph area scrolls internally while the page has no horizontal overflow.
    - `graphStamp` includes the serialized selection, a live-revision counter and the geometry.
  - Negative Tests:
    - Node positions stay fixed at the 800×430 constants when the container changes size.
    - A resize-render feedback loop occurs, where the observed size keeps changing.

- AC-11: Unneeded information is removed.
  - Positive Tests:
    - The graph hint, graph legend, timeline caption and events footer move into one "Legend" disclosure.
    - The duplicate experiment name and duplicate config summary are shown once each.
    - The node subtitle ("teaching simulation node" / "Go · standalone container process") and the "view position" row are removed.
    - Metrics are reduced to online nodes, messages and faults, shown inline.
    - Overwrite semantics, disabled reasons, errors and live/replay markers stay as visible text.
  - Negative Tests:
    - Essential guidance (disabled reasons, overwrite notice, errors) exists only in `title` attributes.
    - Replay time appears both in `#metric-time` and in `#timecode`.

- AC-12: Mobile sheet and keyboard accessibility.
  - Positive Tests:
    - At ≤700px the popover is a bottom sheet with max-height 60vh and internal scroll. Default node selection does not open it; tapping a node, edge or badge opens it.
    - Focus handling:
      - Opening via the badge focuses the first input field.
      - Other openings focus the popover heading.
      - Closing restores focus to the originating element. If that element was redrawn, focus goes to the element with the same data identity; if it no longer exists, to the graph container.
    - Escape precedence: modal dialog > drawer > link mode (returns to the node it was entered from) > popover/sheet close.
    - Links are reachable by keyboard through the peer-link list.
    - `.link-hit` has an accessible name but is not a mandatory Tab stop.
  - Negative Tests:
    - The sheet opens automatically when a run is opened.
    - Escape closes the popover while a modal dialog is open.
    - Focus is lost to `document.body` after the popover closes.

- AC-13: Regression gate.
  - Positive Tests:
    - `npm run check`, `npm test` and `node tests/dom-smoke.mjs` all pass.
    - These browser suites pass with updated selectors: `tests/browser-smoke.mjs`, `tests/browser-input-smoke.mjs`, `tests/browser-rpc-smoke.mjs`, `tests/browser-hierarchy-smoke.mjs`, `tests/browser-workspace-smoke.mjs`, `tests/browser-docs-smoke.mjs`.
    - The new `tests/browser-layout-smoke.mjs` asserts AC-8, AC-9, AC-10 (12-node fallback), AC-1.1 and AC-12 (mobile Escape and focus).
    - Behavioural assertions cover:
      - asymmetric link preservation
      - explicit two-direction overwrite
      - replay/live isolation
      - blocking by a same-experiment active run
      - failed startup via the lifecycle event
      - draft survival across live updates
      - a delayed response after switching runs or targets
    - An `npm run test:browser` script runs the browser suites.
  - Negative Tests:
    - Any existing smoke suite is deleted or skipped instead of updated.
    - The layout test uses `fullPage` screenshots only, with no geometry assertions.

## Path Boundaries

### Upper Bound (Maximum Acceptable Scope)

The implementation delivers everything in AC-1 to AC-13:
- the anchored popover with node and link modes, a peer-link list and the space-time entry point
- a live-state projection built on a shared pure reducer
- target-keyed drafts, pending state and response guards
- the run control strip with full state precedence and the server `lifecycle failed` event
- the compact header with a temporary drawer sidebar
- the ResizeObserver-driven topology with an internal-scroll fallback
- information pruning
- the mobile bottom sheet with defined focus and Escape behaviour
- the new layout test plus updated smoke suites and an `npm run test:browser` script

### Lower Bound (Minimum Acceptable Scope)

The implementation still meets every AC, with the simplest mechanisms that do so:
- popover positioning by bounding-rect math with side flip and clamp
- a plain action `<select>` as the action chooser
- a fixed-order drawer
- metrics as one inline text line
- the 12-node fallback done purely with CSS overflow on the graph area

### Allowed Choices

- Can use:
  - vanilla JS, HTML and CSS consistent with the existing framework-free frontend
  - native `<dialog>` (non-modal `show()`) or a positioned `<div role="dialog">` for the popover
  - the Popover API if the target browsers support it
  - ResizeObserver and requestAnimationFrame
  - the existing `api()`, `guard()`, `notify()`, `renderApplicationInput` form generator and `inputDrafts` pattern
  - Playwright for browser tests, as existing suites do
  - linkedom for DOM smoke
- Cannot use:
  - new runtime frontend dependencies or frameworks (the README states the frontend needs no third-party runtime packages)
  - changes to the fault or command API payloads
  - a docked right-hand inspector as the primary surface (user chose the anchored popover)
  - per-node program or code configuration inside the popover
  - forcing the user back to live mode before a mutation

## Feasibility Hints and Suggestions

> **Note**: This section is for reference and understanding only. These are conceptual suggestions, not prescriptive requirements.

### Conceptual Approach

```
state:
  selection = {kind:'node', id} | {kind:'link', a, b, dir, origin}
  popoverOpen = bool
  live = reduceAll(events)                 // shared pure reduceEvent(acc, e)
  replay = snapshot() using the same reduceEvent up to cursor
  liveRev++ on node / fault / input_schema events

render():
  if graphStamp(view, cursor, playTime, selection, liveRev, geometry) changed:
     redraw SVG (innerHTML) including input badges from live.schemas
  renderControlStrip(inspectedRun, activeRun)       // AC-7 precedence table
  if popoverOpen:
     renderPopover(selection)   // stable form DOM; rebuild only on selection/schema change
     anchorPopover()            // getBoundingClientRect of [data-node=id] or .link-hit[data-from=a][data-to=b]
                                // prefer right side, flip left, clamp to .graph-area; sheet mode at ≤700px

mutation(targetKey, request):
  token = {loadGen, runId, targetKey}; pending.set(targetKey)
  try   await api(...)
  catch keep draft; show inline error if token still current
  finally clear pending[targetKey]; show notices only if token still current
```

Layout: `body` has the header row (breadcrumb/title, view switch, inline metrics, ☰) and the control strip (primary, status, "Network", playback group, timeline). Below them `.graph-area` fills the remaining height via flex or grid. There is no right column. The event log is a collapsible drawer below the graph.

### Relevant References

- `public/index.html`: current topbar, workspace heading, experiment bar, metrics, graph heading, `.inspector-tabs`, `#node-panel`, `#application-panel`, `#fault-panel`, `.transport`, `.timeline-wrap`, `#event-log`, sidebar.
- `public/app.js`, grouped by area:
  - **Event state:** `snapshot`/`resetCache` (replay reducer), `receiveEvent` (lifecycle mapping), `openRun` (default selection, load generation via `loadingRun`).
  - **Graph drawing and selection:** `drawGraph`/`replaceGraph` (SVG string rendering, `.link-hit`, node card badge slot), `graphClick` and the keydown handler.
  - **Inspector and faults:** `renderApplicationInput`/`nodeInputSchema`/`inputDrafts`, `renderInspector` (focus guard, `rawOpen`), `setTab`/`updateFaultFields`/`#inject-fault`.
  - **Page chrome:** `render` (graphStamp, status and metrics writes), the playback handlers, `#stop-run`, `renderLibrary` (active-run banner, `#new-run`), `refreshRuns` (poll), `setSidebar`.
  - **Space-time:** the ResizeObserver precedent.
- `public/style.css`: sidebar/main layout and its override blocks, `.topbar` height, the graph height `clamp(... calc(100vh - 480px) ...)`, `.lab-grid` columns, mobile breakpoints.
- `server/engine.js`: `validateFault` (kinds and ranges), `fault()` (per-direction link rules, heal clears links only), `stop()` lifecycle.
- `server/index.js`: `launch()` catch (add the `lifecycle failed` event), the stop-during-starting rejection, the faults route with the `run.mutating` lock (409), the single-active-run check.
- `server/inputs.js`: input schema limits (64 actions × 16 fields).
- `tests/browser-smoke.mjs`, `tests/dom-smoke.mjs`: current fault-tab selectors to replace.
- `tests/browser-input-smoke.mjs`, `tests/browser-rpc-smoke.mjs`, `tests/browser-hierarchy-smoke.mjs`, `tests/browser-workspace-smoke.mjs`, `tests/browser-docs-smoke.mjs`: selectors to keep working. Keep `#application-panel` and `#node-panel [data-field]` ids inside the popover.
- `docs/UI-REVIEW.md`: prior compaction policy. It already requires that user-opened disclosures not collapse on refresh.
- `README.md`: UI description and test instructions to update after implementation.

## Dependencies and Sequence

### Milestones

1. **State foundation.** Shared pure reducer, live projection, selection model, response-guard token.
   - Phase A: extract `reduceEvent`; build the live projection hydrated in `openRun`, reset in `clearRun`, updated incrementally in `receiveEvent`; add `liveRev`.
   - Phase B: explicit lifecycle mapping; server `lifecycle failed` event; request sequencing for `refreshRuns`; active-run cache.
2. **Anchored popover.** Depends on Milestone 1.
   - Phase A: popover container outside the SVG; node mode (input first, live toggle, peer-link list, replay state rows); anchoring, flip and clamp; close and switch behaviour; remove the tabs and the fault panel.
   - Phase B: link mode with direction control, live prefill, overwrite notice and validation; edge and peer-list entry; space-time lane entry.
   - Phase C: stable form DOM, target-keyed drafts and pending state, dirty-link reconciliation, schema-change draft reconciliation.
3. **Run control strip.** Depends on Milestone 1, Phase B.
   - Step 1: strip markup combining the primary action, status, "Network" control (heal and history), playback group and timeline.
   - Step 2: precedence table between the inspected run and the active run; pending and error handling for stop.
4. **Compact shell and geometry.** Depends on Milestone 3 for the final strip height.
   - Step 1: merge the header rows; inline metrics; remove the right column; the graph area fills the remaining height.
   - Step 2: temporary drawer sidebar in the visualization view; saved preference untouched elsewhere.
   - Step 3: ResizeObserver topology with minimum sizes and internal-scroll fallback; geometry in `graphStamp`.
5. **Pruning, badge, mobile, accessibility.** Depends on Milestones 2 and 4.
   - Step 1: input badges on node cards; badge-to-first-field focus.
   - Step 2: "Legend" disclosure; remove duplicates.
   - Step 3: bottom-sheet mode ≤700px; focus entry and restore; Escape precedence; peer-list keyboard path.
6. **Regression gate.** Selector updates land with each milestone; the new layout suite and behavioural assertions land last.

Milestone 1 blocks everything that mutates. Milestones 2 and 3 can proceed in parallel after Milestone 1. Milestone 4 needs the final strip. Milestone 5 needs the popover and the final layout.

## Task Breakdown

Each task must include exactly one routing tag:
- `coding`: implemented by Claude
- `analyze`: executed via Codex (`/humanize:ask-codex`)

| Task ID | Description | Target AC | Tag (`coding`/`analyze`) | Depends On |
|---------|-------------|-----------|----------------------------|------------|
| task1 | Extract shared pure `reduceEvent`; build the live projection (hydrate, reset, incremental) and `liveRev`; switch replay `snapshot()` to the shared reducer | AC-4, AC-10 | coding | - |
| task2 | Explicit lifecycle mapping on the client; server `lifecycle {action:'failed'}` in the `launch()` catch; `refreshRuns` request sequencing and terminal-status protection; active-run cache | AC-7 | coding | task1 |
| task3 | Response-guard token `{loadGen, runId, targetKey}` and target-keyed pending registry used by all mutations | AC-6 | coding | task1 |
| task4 | Popover container outside the SVG; node mode (input first, live crash/recover toggle, peer-link list, replay state rows, raw JSON); anchor, flip and clamp; open, switch and close; remove `.inspector-tabs` and `#fault-panel` | AC-1, AC-1.1, AC-4 | coding | task1, task3 |
| task5 | Link mode: both-direction live rules, direction control and defaults, overwrite notice, integer range validation; edge, peer-list and space-time lane entry points | AC-1.2, AC-2 | coding | task4 |
| task6 | Stable form DOM; drafts keyed per input action and link pair plus direction; dirty-link reconciliation; schema-change draft reconciliation; action chooser; visible disabled reasons | AC-5, AC-6 | coding | task4 |
| task7 | Run control strip: primary button precedence table, status badge, "Network" control (heal with visible explanation, fault history), playback group and timeline; stop pending and error handling | AC-3, AC-7 | coding | task2, task3 |
| task8 | Compact shell: merge header rows, inline metrics, remove the right column, graph area fills the remaining height; temporary drawer sidebar with ☰, saved preference untouched | AC-8, AC-9 | coding | task7 |
| task9 | ResizeObserver topology geometry, minimum node size, internal-scroll fallback for 12 nodes, geometry in `graphStamp` | AC-10 | coding | task1, task8 |
| task10 | Input badges on node cards from the live schema map; badge opens the popover and focuses the first field | AC-5 | coding | task4, task9 |
| task11 | Information pruning: "Legend" disclosure, remove duplicate name/config, drop node subtitle and "view position" row, reduce metrics; keep essential guidance visible | AC-11 | coding | task8 |
| task12 | Mobile bottom-sheet mode ≤700px, focus entry and restore with identity fallback, Escape precedence, peer-list keyboard path, accessible names on `.link-hit` | AC-12 | coding | task4, task8 |
| task13 | Update the selectors and flows in the existing smoke suites (browser-smoke, dom-smoke, input, rpc, hierarchy, workspace, docs); add the `npm run test:browser` script | AC-13 | coding | task4, task7, task8 |
| task14 | New `tests/browser-layout-smoke.mjs` with the geometry budgets, 12-node fallback, anchoring stability, mobile Escape/focus and behavioural assertions (asymmetric link, replay/live isolation, same-experiment block, failed lifecycle, draft survival, stale response) | AC-8, AC-9, AC-10, AC-12, AC-13 | coding | task9, task10, task11, task12, task13 |
| task15 | Independent review of the finished UI against AC-1 to AC-13 and of the popover redraw and focus robustness; list gaps | AC-1 to AC-13 | analyze | task14 |
| task16 | Update `README.md` and `docs/UI-REVIEW.md` to describe the new popover, control strip, drawer and test command | AC-13 | coding | task15 |

## Claude-Codex Deliberation

### Codex First-Pass Findings (Codex Analysis v1)

**Core risks:**
- Replay-derived `cache.online` and `cache.links` could drive live mutations.
- "Both" (both-direction) apply could silently destroy an asymmetric rule.
- Run controls must distinguish the inspected run from the active run.
- The draft's size estimate covered only the inspector change.

**Missing requirements:**
- selection lifecycle (default node-1, no deselection today)
- global heal/history placement
- non-graph link access (a peer list)
- sidebar preference versus temporary auto-hide, and nav reachability (`.mobile-docs` is hidden above 760px)
- bounded input growth (64 actions × 16 fields)
- mobile presentation

**Technical gaps:**
- The current focus guard only covers `input` in `#node-panel`.
- `tabindex` alone is insufficient for links.
- `graphStamp` invalidation needs selection and schema data.
- The client maps every non-start lifecycle action to completed.
- The topology needs a geometry policy.
- Mutations need pending and error handling.

### Agreements

- A selection-driven unified surface replaces the tabs, and the fault target comes from the selection.
- Link editing is directional. Both rules are shown. "Both" requires an explicit overwrite notice.
- Heal and fault history are reachable regardless of selection. Heal's meaning (links only, not crashed nodes) is visible text.
- Mutations stay available during replay (documented README behaviour), provided the safeguards hold:
  - live-derived state
  - Live/Replay labels
  - command results shown from live events regardless of the cursor
- One pure reducer is shared by the live and replay projections. The live revision and the selection are part of `graphStamp`.
- The sidebar is hidden temporarily in the visualization view without overwriting `distvis-sidebar-collapsed`.
- Keyboard access to links goes through the peer list, so 66 edges are not all Tab stops.
- The run control precedence uses the inspected run versus the active run, including same-experiment and starting runs.
- Stop already awaits the API. What is missing is pending state, status handling and race protection.
- "Run again" uses the current experiment settings plus the current saved protocol revision.
- Guidance is not moved to tooltip-only. Disabled reasons, overwrite semantics and errors stay visible.
- The test gate covers `npm run check`, `npm test`, DOM smoke and all browser suites including `browser-workspace-smoke`, plus behavioural assertions.

### Resolved Disagreements

- **Mutations during replay.** Codex v1 recommended requiring a return to live mode; Claude kept current behaviour with safeguards. Codex accepted in round 1 on condition that the safeguards become ACs (now AC-4). The user confirmed: allowed, labelled as acting on live.
- **Active-run scope.** Codex said "another experiment active" was too narrow. Resolved by making any active run (same or other experiment, including starting) override rerun.
- **Default link direction.** Codex said "direction clicked" is ambiguous for an undirected hit area. Resolved: graph edge → lower-index → higher-index; peer list → selected node → peer.
- **Tooltip-only help.** Codex objected. Resolved: only decorative or tutorial prose folds into "Legend"; essential guidance stays visible.
- **Response guards.** Rounds 2 and 3 required guarding by selection/action plus load generation, not only run id. Resolved: `{loadGen, runId, targetKey}` gates notices, inline renders, pending and drafts. This last refinement was adopted verbatim after round 3 without a further Codex pass.
- **Status authority.** Resolved: lifecycle events update the active-run cache immediately; stale polls are discarded by request sequence; terminal statuses are never downgraded.
- **Mobile lifecycle and focus.** Resolved:
  - Default selection does not open the sheet.
  - Badge-to-first-field focus takes precedence.
  - Focus falls back by data identity, then to the graph container.
  - Escape precedence is defined.
- **Geometry fallback.** Resolved: minimum node card size, then internal graph-area scroll. Page-level horizontal overflow is prohibited; internal graph scroll is allowed.

### Post-Convergence User Decision That Diverges From Both Reviewers

- **Presentation form.** Claude and Codex both recommended a docked non-modal inspector on desktop plus a mobile sheet. The user chose a floating popover anchored beside the element. The plan adopts the user's choice and absorbs the risks Codex raised for this form:
  - survives redraws (AC-1.1)
  - must not cover its anchor
  - defined focus and Escape handling (AC-12)
  - peer-list access for dense graphs and the space-time view (AC-1.2)

  This form was not re-reviewed by Codex. task15 schedules an independent Codex review of it after implementation.

### Convergence Status

- Final Status: `partially_converged`. Three rounds ran (the maximum). After round 3, Codex listed one remaining required change, response notices gated by target key. Claude adopted it verbatim, but no fourth review confirmed it. The later user choice of the anchored popover also departs from the reviewed docked design. There are no open Claude/Codex disagreements.

## Pending User Decisions

- DEC-1: Mutations during replay
  - Claude Position: allow them, with live-derived state and a "Live" marker
  - Codex Position: initially required live mode; accepted Claude's position with safeguards in round 1
  - Tradeoff Summary: convenience and consistency with README versus stricter protection against confusion
  - Decision Status: `Allowed, labelled as acting on live — mutations allowed during replay, always act on the live run, clearly labelled`
- DEC-2: Form of the unified dialog
  - Claude Position: docked non-modal inspector on desktop plus a mobile bottom sheet
  - Codex Position: same as Claude (docked is reasonable; a dialog is not established by the draft)
  - Tradeoff Summary: a docked panel is lower risk and keeps the graph unobscured; an anchored popover keeps actions next to the element and frees the full graph width, but needs robust anchoring under per-frame redraws
  - Decision Status: `Anchored floating popover beside the element; bottom sheet at ≤700px`
- DEC-3: Scope of "node configuration"
  - Claude Position: application input, node faults and link settings; code stays in the protocol workspace
  - Codex Position: N/A - open question (asked to keep it explicitly provisional)
  - Tradeoff Summary: including per-node program configuration would greatly expand scope and applies only to the next run
  - Decision Status: `Application input + faults + link settings`
- DEC-4: Numeric layout budgets
  - Claude Position:
    - 1440×900: chrome ≤104px, graph ≥60% of viewport height, sidebar width 0, primary button visible
    - 390×844: no horizontal overflow, graph ≥300px, collapsed sheet ≤56px, targets ≥40px
  - Codex Position: N/A - open question (asked for concrete defaults before implementation)
  - Tradeoff Summary: hard thresholds make "simpler" testable but depend on font rendering
  - Decision Status: `Hard requirements — hard acceptance criteria with ±4px rendering tolerance`
- DEC-5: Sidebar policy in the visualization view
  - Claude Position: temporary auto-hide; saved preference untouched
  - Codex Position: agreed (round 1)
  - Tradeoff Summary: reclaims width without surprising users in other views
  - Decision Status: `Adopted per Claude/Codex agreement (not separately asked; consistent with draft "remove it or hide it automatically")`

## Implementation Notes

### UI Copy Language
- Labels in this plan are written as English glosses. The implemented UI keeps the application's existing Chinese copy style; implementers translate each gloss into matching Chinese UI text consistent with current strings in `public/app.js` and `public/index.html`.

### Code Style Requirements
- Implementation code and comments must NOT contain plan-specific terminology such as "AC-", "Milestone", "Step", "Phase", or similar workflow markers.
- These terms are for plan documentation only, not for the resulting codebase.
- Use descriptive, domain-appropriate naming in code instead.
- Match the existing style of `public/app.js`: compact vanilla JS, `$`/`$$` helpers, `guard()` for async handlers, Chinese UI strings, sparse comments that explain intent.
- Keep existing element ids used by tests (`#application-panel`, `#node-panel [data-field]`, `#run-status`, `#metric-messages`, `#play`, `#step`, `#go-live`, `#speed`, `#timeline`, `#nav-guide`) or update every referencing test in the same change.

## Output File Convention

This template is used to produce the main output file (e.g., `plan.md`).

### Translated Language Variant

When `alternative_plan_language` resolves to a supported language name through merged config loading, a translated variant of the output file is also written after the main file. Humanize loads config from merged layers in this order: default config, optional user config, then optional project config; `alternative_plan_language` may be set at any of those layers. The variant filename is constructed by inserting `_<code>` (the ISO 639-1 code from the built-in mapping table) immediately before the file extension:

- `plan.md` becomes `plan_<code>.md` (e.g. `plan_zh.md` for Chinese, `plan_ko.md` for Korean)
- `docs/my-plan.md` becomes `docs/my-plan_<code>.md`
- `output` (no extension) becomes `output_<code>`

The translated variant file contains a full translation of the main plan file's current content in the configured language. All identifiers (`AC-*`, task IDs, file paths, API names, command flags) remain unchanged, as they are language-neutral.

When `alternative_plan_language` is empty, absent, set to `"English"`, or set to an unsupported language, no translated variant is written. Humanize does not auto-create `.humanize/config.json` when no project config file is present.

--- Original Design Draft Start ---

# Selection-Driven Unified Inspector For Node And Link Faults

## Original Idea

我希望界面更加简洁一些，不需要的信息要减少，与某个节点/边的配置和故障注入都放到同意的一个对话框，而不是有两个tab（在故障注入里边还需要选），另外页头也要小一些，留出更大空间来给visualization的空间，启动结束实验的按钮位置要清晰，节点上input输入的地方也应该更明显，左边的边栏也可以不要或者自动隐藏。


## Primary Direction: Unified Element Inspector Panel

### Rationale

Attacks the core structural complaint — merge the current node inspector tab and the separate fault-injection tab (where the target must be re-selected) into a single selection-driven panel/dialog that shows state, app input, and faults for whichever node or edge (link) is selected.

### Approach Summary

Replace the two-tab inspector (`节点详情` / `故障注入`) with one selection-driven inspector. A `selection` object is either `{kind:'node', id}` or `{kind:'link', from, to}`. The fault target comes from the selection, so the target selects and the fault-kind dropdown are removed.

- **Node selected:** a header with the name, online badge and sent/received counts. Below it, a prominent **应用输入** block: the existing schema-generated forms, moved to the top and styled as the primary call to action. Then the key state rows, then the collapsed raw JSON. Node faults become a direct toggle: `模拟崩溃` when online, `恢复节点` when offline. These buttons already exist in the node panel.
- **Link selected (edge click):** a header `from ↔ to` showing the current per-direction rule from `cache.links` (latency, bandwidth, blocked). Inline controls: latency, bandwidth, a 中断 (blocked) toggle, a direction choice (A→B / B→A / both) and an 应用 button. It posts the same `{kind:'link', ...}` payload the fault form posts today, with from/to filled in from the selection.
- **Nothing selected:** a short hint, the global `恢复全部链路` (`heal`) action, and the recent-fault history.

Files affected:
- `public/index.html`: drop `.inspector-tabs` and the `#fault-panel` selects.
- `public/app.js`:
  - Replace `setTab`, `nodeTab`, `updateFaultFields` and the fault parts of `populateNodes` with `selection`.
  - `renderInspector` switches on the selection kind.
  - `graphClick` sets a link selection instead of setting the old form fields.
  - `drawGraph` highlights the selected link and gives `.link-hit` a tabindex so links can be reached by keyboard.
- `public/style.css`: remove the tab styles and add a selected-link style.
- Server: no change. `/api/runs/:id/faults` and `/commands` already accept the needed payloads.

Estimated size:
- `app.js`: about 60–100 lines changed
- HTML: about 10 lines removed
- CSS: about 10 lines
- Smoke tests: the fault-tab selectors must be updated

### Objective Evidence

- **Current tabbed inspector:** `public/index.html:55` defines `.inspector-tabs` with `data-tab="node"` and `data-tab="fault"`. `#node-panel` is at :56, `#application-panel` at :57, and `#fault-panel` at :58–64.
- **The fault form forces the target to be picked again:**
  - `public/index.html:60` has the `#fault-kind` select (crash/recover/link/heal).
  - `:61` has `#fault-node`.
  - `:62` has `#fault-from`/`#fault-to` plus latency, bandwidth, blocked and bidirectional.
  - `populateNodes()` at `public/app.js:145-148` fills these selects.
- **Tab switching:** `setTab()` at `public/app.js:150-155` is wired at :584. `nodeTab` is checked in `renderInspector` at :418.
- **Links are already clickable:** `public/app.js:181` draws a `.link-hit` path per unordered pair, with `data-from`/`data-to` and the tooltip "点击配置". `graphClick` at `public/app.js:510-515` currently switches to the fault tab and pre-fills the form. This is the starting point for a link selection. Links have no tabindex, and the keydown handler at :520 only handles `[data-node], [data-event]`.
- **Per-selection faults already exist for nodes:** `public/app.js:427` renders `data-node-action="crash"` and `"recover"` in `#node-panel`, handled at :585-588 and posting `{kind, node: selected}`.
- **Fault API:**
  - `POST /api/runs/:id/faults` is routed at `server/index.js:250,276-282`.
  - `server/engine.js:150-160` accepts only four kinds:
    - `crash` and `recover`, per node
    - `link`, per directed link: from/to, latency 0–30000, bandwidth 1–100000, blocked, bidirectional
    - `heal`, global
  - A partition is expressed as links with `blocked:true`.
  - `server/runtime.js:231-251` sends the same kinds to the Docker/K8s runtimes.
- **Link rules are stored per direction:** `server/engine.js:179-181` keys them as `from>to`, and `snapshot()` at `public/app.js:101-107` mirrors them. `drawGraph` already reads both directions to style blocked links. This is enough to pre-fill a link editor.
- **App-input forms can be reused as they are:**
  - `renderApplicationInput()` at `public/app.js:361-388` builds the forms from the latest `input_schema` event (`nodeInputSchema` at :357-359).
  - Field types are validated in `server/inputs.js:3-39`.
  - Drafts are kept per `run:node` in `inputDrafts` (:356, :389-396).
  - Submit goes to `/api/runs/:id/commands` (:397-413).
- **CSS to remove or adjust:** the `.inspector-tabs` sticky rule and the inspector max-height (`public/style.css:94,102`), the `#application-panel`/`.application-form` styles (:73-79), and `.link-hit`, which has hover styling only and no selected style.
- **Tests that depend on the current DOM:**
  - `tests/browser-smoke.mjs:41-47` uses `[data-tab="fault"]`, `#fault-kind`, `#fault-node` and `#inject-fault`.
  - `tests/dom-smoke.mjs:57-58` uses `#fault-node`, `#fault-kind` (with an `onchange` call) and `#inject-fault`.
  - `#application-panel` and `#node-panel [data-field]` are used in `tests/browser-smoke.mjs:29-32,101`, `tests/browser-input-smoke.mjs:20-54`, `tests/browser-rpc-smoke.mjs:46-54` and `tests/dom-smoke.mjs:114-123`. Keeping these ids avoids most test churn.

### Known Risks

- The fault tests in `tests/browser-smoke.mjs:41-47` and `tests/dom-smoke.mjs:57-58` will break and must be rewritten to use node or link selection.
- One drawn edge covers both directions (`app.js:181` draws only pairs with i<j). The link editor needs an explicit direction control, or one-way faults can no longer be expressed.
- `heal` is global and has no natural selection target. It needs a home in the empty-selection state or in a toolbar, or users won't find it.
- `renderInspector` skips re-rendering while an input has focus (`app.js:422-423`). A link editor pre-filled from `cache.links` needs the same draft protection, or live events will overwrite values the user is typing.
- With 12 nodes, 66 overlapping hit paths make edge selection ambiguous on dense graphs. This may need a wider hit area or a per-edge midpoint handle.
- In the space-time view links are not clickable (only nodes and events are), so link faults can only be reached from the topology view unless a second entry point is added, such as a "links from this node" section in the node view.

## Alternative Directions Considered

### Alt-1: Canvas-First Layout Shell
- Gist: Rebuild the page shell so the visualization fills the screen.
  - Collapse the rows now stacked above the graph (`.topbar`, `.workspace-heading`, `.experiment-bar`, `.metrics`, graph heading) into one toolbar of about 40px: breadcrumb and title on the left, the topology/space-time switch in the middle, status and one primary run/stop button on the right.
  - Turn the fixed `.sidebar` into an overlay drawer that is hidden by default, and drop the `main` left margin.
  - Make `#visual-panel` a full-height grid and move the event log into a bottom drawer.
  - Make the topology resize with its container using a ResizeObserver. Today the fixed 800×430 viewBox means extra space only adds margins.
- Objective Evidence:
  - `public/style.css:2` sets `.sidebar{position:fixed;width:222px}` and `main{margin-left:222px}`. It is overridden at :4, :5, :101, :103 and :104, so six places define the layout.
  - `public/style.css:101` sets `.topbar{height:44px}` and `.graph-area,#graph{height:clamp(280px,calc(100vh - 480px),500px)}`, which hard-codes about 480px of chrome.
  - Up to six rows sit above the graph: `public/index.html:22–47`. `.transport` and `.timeline-wrap` are at :54–55.
  - `public/index.html:48` has `<svg id="graph" viewBox="0 0 800 430">`. `drawGraph` at `public/app.js:172-175` uses fixed centre 400/218 and radii 255/151, with no resize handling.
  - Precedent to copy: the space-time view already uses `ResizeObserver` at `public/app.js:578-582`.
  - `setSidebar()` and localStorage `distvis-sidebar-collapsed` exist at `public/app.js:1019-1026`.
  - Everything in the sidebar is reachable elsewhere: the breadcrumb, `#create-experiment`, and `.mobile-docs` (`public/app.js:654,740,786-787`).
  - `tests/browser-docs-smoke.mjs:11,50` clicks `#nav-guide`. The 390px overflow checks are at `tests/browser-smoke.mjs:117-119` and `tests/browser-hierarchy-smoke.mjs:78`.
- Why not primary: It frees the most room but doesn't fix the two-tab fault workflow. Its large CSS consolidation and topology re-parameterization are best done after the inspector structure settles.

### Alt-2: Run-State-Driven Control Bar
- Gist: One sticky strip at the top of `#visual-panel` replaces the scattered `#new-run` (heading), `#stop-run` (`.experiment-bar`), `.transport` and `.timeline-wrap` rows.
  - A single `#run-primary` button changes with the derived state:
    - idle → 运行实验
    - building (`starting` plus the latest runtime event) → 构建中… (disabled)
    - running → 结束实验
    - completed/interrupted → 重新运行
    - failed → 查看错误
    - blocked → 前往运行中的实验
  - Playback, speed, a live/replay pill and a thin timeline scrubber share the strip.
  - Metrics shrink to one inline line.
  - Optionally emit a `lifecycle failed` event so a failure is not only detected by polling.
- Objective Evidence:
  - The run button, stop button and playback controls are in separate places: `#new-run` at `public/index.html:28`, `#stop-run` in `.experiment-bar`, `.transport` at `:51`, `.timeline-wrap` at `:52`, `#mode-label` in the graph heading. The empty state tells users to click 「运行实验」 top right (`:50`).
  - Client statuses are defined at `public/app.js:31`. Server transitions are at `server/engine.js:45,134-136,143-148` and `server/index.js:35-37,92-99,237`.
  - There is no explicit building state. It shows only in runtime logs (`server/runtime.js:82,141`), and stopping during a build is rejected (`server/index.js:273`).
  - Updates come from SSE (`public/app.js:139-142`), but lifecycle events only set running and completed (`:118`). Failed and interrupted arrive only from the 1.5 s poll (`:671,1030`). The stop handler sets completed optimistically (`:622`).
  - The single global run lock is at `server/index.js:172`, mirrored at `public/app.js:656-660,726-727`.
  - Tests that depend on these: `tests/dom-smoke.mjs:45,53-59`, `tests/browser-smoke.mjs:20-49`, `tests/browser-hierarchy-smoke.mjs:28,39,45`.
- Why not primary: It directly fixes the "start/stop placement" complaint, but it covers only one of the requested changes. It is a strong candidate to fold into the primary.

### Alt-3: On-Canvas Direct Manipulation
- Gist: Move the interactions onto the graph.
  - A single popover, a DOM sibling of the SVG, is anchored at the clicked node or edge. It holds the input forms plus crash/recover for nodes, or the latency/bandwidth/blocked controls for edges.
  - Nodes that declare inputs get a "⌨ n" badge drawn on the node card. Clicking the badge opens the popover at its forms.
  - The 288px inspector column is removed, so the graph gets the full width.
  - This needs a per-node `input_schema` map instead of the current `findLast` scan.
- Objective Evidence:
  - The topology SVG is rebuilt with `innerHTML` in `drawGraph`/`replaceGraph` (`public/app.js:168-228`), about 30 times a second while live (`:442-447,996-1008`). A popover therefore must live outside the SVG.
  - `.link-hit` has a 17px-wide transparent stroke, with `data-from`/`data-to` (`public/app.js:177-182`).
  - The delegated `graphClick` is shared by the topology and space-time views (`public/app.js:510-524`). Space-time drag suppression is at `:536-565`.
  - The leader badge slot on the node card (`public/app.js:214-219`) is precedent for an input badge.
  - Input schemas come from the `input_schema` events at `server/engine.js:184-188` (`sdk/node.go:50`, `sdk/rpc/host.go:144`). The client looks them up only for the selected node (`public/app.js:357-359`).
  - Existing popover-like UI: the `details.more-menu` outside-click close at `public/app.js:788` and the dialog backdrop-click close at `:790-791`.
- Why not primary: It gives the most visible input affordance, but the popover has to survive being redrawn every frame, needs focus and anchor handling, and every form-based smoke test would need an extra click. Its surface and risk are higher than a docked unified panel.

### Alt-4: Information Pruning via Progressive Disclosure
- Gist: A content-only pass over the roughly 60–65 visible text elements in the visualization view.
  - Keep live data visible.
  - Fold the graph hint, legend and timeline caption into one "图例" disclosure.
  - Turn fixed help sentences into tooltips.
  - Remove duplicates: the experiment name is shown three times, the config summary twice, and "视图位置" repeats `#timecode`/`#mode-label`.
  - Reduce the metrics to the counts that actually change.
- Objective Evidence:
  - `docs/UI-REVIEW.md:33` records an earlier pass that removed repeated titles and taglines and folded secondary panels by default. This is a direct precedent.
  - Existing disclosure patterns: `#spacetime-options.compact-disclosure` (`public/index.html:46`), `#run-details.more-menu` (`:38`), `details.recent-faults` (`:63`), `#event-log` (`:67`), `details.state-raw` with persisted `rawOpen` (`public/app.js:424-427`).
  - Fixed tutorial text: `public/index.html:44,50,52,56,59,67`, `public/app.js:380,385,427,467`.
  - Duplicates:
    - experiment name: `public/app.js:932,935,451`
    - config summary: `:933,452`
    - playback position: `:456` vs the node panel "视图位置" row (`:427`)
    - replay time: `#metric-time` (`:471`) vs `#timecode`
  - Precedent for hiding by context: `public/app.js:350-351` already hides `.graph-hint` and `.graph-legend` in the space-time view.
- Why not primary: It addresses only "不需要的信息要减少". It doesn't change the dialog structure, header, sidebar or button placement, so it works best as a companion pass.

### Alt-5: Measurable Layout Budgets With Browser Tests
- Gist: Add `tests/browser-layout-smoke.mjs`, reusing the Playwright setup from `browser-smoke`. At 1440×900 and 390×844 it asserts:
  - topbar height of 40px or less
  - graph area as a share of the viewport, about 55% on desktop and 40% on mobile
  - sidebar hidden in the experiment view
  - `#new-run` and `#stop-run` inside the first screen and next to each other
  - the first `#application-panel` input visible and at least 32px tall
  - two clicks or fewer to inject a fault
  - a cap on visible text nodes

  It also updates the selectors in the existing smoke tests and adds viewport-only screenshots.
- Objective Evidence:
  - No current test measures header height, graph area, sidebar width or where the start/stop buttons sit.
  - Only `tests/browser-docs-smoke.mjs:24` asserts a position. The 390px overflow checks are in `tests/browser-smoke.mjs:117-119`, `tests/browser-rpc-smoke.mjs:66-67` and `tests/browser-hierarchy-smoke.mjs:78`.
  - Screenshots use `fullPage:true` (`tests/browser-smoke.mjs:34,92,118`). `artifacts/compact-visual.png` and `artifacts/experiment-visual.png` have no generating script.
  - `docs/UI-REVIEW.md:33` claims "主要回放和保存操作在桌面首屏可见" and "侧栏可收起并记住选择", but no test enforces either.
  - The browser tests aren't part of `npm test` (`package.json` runs `tests/*.test.js`), per `README.md:245-262`.
  - The full list of at-risk selectors is `.inspector-tabs`, `[data-tab=*]`, `#fault-kind/-node/-from/-to`, `#node-fault-fields`, `#link-fault-fields`, `#inject-fault`, `#fault-history`, `#metric-*`, `#nav-guide` and `#toggle-sidebar`.
- Why not primary: It is how to verify the redesign, not the redesign itself. It should be adopted as the acceptance harness for whichever direction ships.

## Synthesis Notes

The primary unified inspector fixes the most concrete pain point: faults sit in a separate tab where the target has to be picked again. It does this with no server work, reusing the existing clickable `.link-hit` edges, the crash/recover buttons already in the node panel, and the `renderApplicationInput` forms. Moving the input forms to the top of the node view also covers "节点上 input 更明显". The other requests fit around it:
- **Alt-2's control bar:** a single state-driven `#run-primary` button plus a `lifecycle failed` event. This fixes start/stop placement and absorbs `.transport`/`.timeline-wrap`.
- **Alt-1's shell changes:** a ~40px toolbar, an overlay sidebar drawer, and a ResizeObserver-driven topology. These are what actually give the graph more room. Use the control bar as the toolbar's right-hand cluster.
- **Alt-4's pruning:** folding the legend and hints and removing duplicate titles, metadata and the "视图位置" row. This should run as a final content pass.
- **Alt-3's per-node input badge:** a cheap addition to `drawGraph` using a per-node `input_schema` map. It makes the input affordance visible on the canvas without the full floating-popover risk. If a docked panel costs too much width, the unified inspector could later become Alt-3's anchored popover.
- **Alt-5's layout-budget test:** the acceptance gate for the whole bundle, together with the needed selector updates in `browser-smoke`/`dom-smoke`.

A sensible plan order is: inspector merge, then control bar, then shell/drawer and a resizable topology, then pruning, with budget tests landing alongside each step.

--- Original Design Draft End ---
