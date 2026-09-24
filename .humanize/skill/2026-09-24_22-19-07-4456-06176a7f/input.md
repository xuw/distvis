# Ask Codex Input

## Question

You are a senior frontend/UX engineer reviewing a planning draft BEFORE an implementation plan is written. Read the repo yourself as needed (read-only).

REPOSITORY CONTEXT
- Repo: /Users/xuw/projects/distvis - "DistVis", a local teaching workbench for distributed systems: users write Go protocols, run N nodes (Docker/K8s/simulation), inject faults, and replay events step-by-step.
- Frontend is framework-free vanilla JS: public/index.html (~79 lines), public/app.js (~1033 lines), public/style.css (~106 minified long lines). Server: server/index.js, server/engine.js (fault kinds crash/recover/link/heal), server/runtime.js, server/inputs.js.
- Tests: tests/browser-smoke.mjs, browser-input-smoke.mjs, browser-rpc-smoke.mjs, browser-hierarchy-smoke.mjs, browser-docs-smoke.mjs (Playwright, need live coordinator), tests/dom-smoke.mjs (linkedom). npm test only runs tests/*.test.js.
- docs/UI-REVIEW.md records a prior UI-compaction pass.

RAW DRAFT (verbatim):
-----
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
-----

TASK
Critique the draft assumptions, identify missing requirements and edge cases, and propose stronger plan directions. Verify key claims against the code where cheap. Consider: the unified node/link inspector, one-way vs bidirectional link faults, heal placement, redraw-every-frame input focus preservation, sidebar auto-hide vs existing persisted collapse preference and nav reachability (API docs, recent protocols), header compaction, run/stop control clarity including building/failed/blocked states, node input prominence, topology resize, mobile 390px, keyboard accessibility, replay vs live semantics (inputs/faults only apply to live run), and test updates.

OUTPUT FORMAT (use exactly these headings):
CORE_RISKS:
MISSING_REQUIREMENTS:
TECHNICAL_GAPS:
ALTERNATIVE_DIRECTIONS:
QUESTIONS_FOR_USER:
CANDIDATE_CRITERIA:
Be concrete and concise; cite file paths.

## Configuration

- Model: bh/gpt-6-astra
- Effort: high
- Timeout: 3600s
- Timestamp: 2026-09-24_22-19-07
- Tool: codex
