# Session Flow — Unified Session View

## Purpose

ccx exists to answer one question fast: **why did the agent do what it did?**

Today, answering that requires mentally combining five separate surfaces:
the flat conversation, chronologically-attached agent rows, the entity
tree, the standalone workflow preview, and per-artifact pages
(refs/images/changes/stats). Each has its own navigation, its own scope
semantics, and its own drill-down implementation.

This design replaces them with a single **Session Flow** view built on two
ideas:

1. **One coordinate system.** The conversation timeline is the spine;
   everything the session spawned or produced (agents, workflows,
   monitors, artifacts) attaches to the exact point on that spine where it
   originated.
2. **Altitude, not modes.** Overview and digging are not separate
   features — they are depths on one axis. `l`/`Enter` always goes deeper,
   `h`/`Esc` always comes back exactly one level, from session summary all
   the way down to raw JSON.

```text
altitude 5  session summary    18 turns · 7 agents · 1 wf · Δ12 · R4 · 183k
altitude 4  flow spine         turns + lifecycle nodes + facet badges
altitude 3  node inspector     Overview·Conversation·Δ·Files·R·I·Stats (z = zoom)
altitude 2  blocks/timelines   tool headlines, poll timeline, diffs, occurrences
altitude 1  source             raw JSON, full output, full diff
```

### Invariants

1. **Never lost.** Breadcrumb at every altitude; returning restores
   cursor, fold, and filter state exactly (extend the existing navStack).
2. **Never a dead end going down.** Every node, badge, and artifact has a
   deeper level, terminating at raw source.
3. **Lateral movement exists.** `J` jumps from any node/artifact to its
   exact origin turn; `[`/`]` cycles siblings/occurrences.
4. **High altitudes never lie.** Summaries and badges derive from exact
   edges (tool_use_id, workflow run/phase, shell correlation) — never
   timestamp heuristics.

## Core taxonomy

Everything in a session falls into one of three shapes, each with one
rendering rule:

| Shape | Examples | Rendering |
|---|---|---|
| **Lifecycle** (has a lifespan) | Agent, Workflow, Monitor, background Bash, Task | Flow node on the spine, with subtree facets |
| **Instantaneous** (point-in-time) | Read, Edit, Write, Bash, Grep, WebFetch, MCP | Tool renderer (headline + semantic body + raw fallback) |
| **Recurring event** (belongs to a lifecycle) | BashOutput poll, KillShell, hook firing, TaskUpdate | Absorbed into the owning node's timeline; hidden from the spine |

## Decision markers — surfacing the "why"

The spine must make **decision-significant turns** visually distinct, so a
reader scanning the session immediately sees where direction was set or
changed. A turn is decision-significant when it produced any of:

- **Plan** — ExitPlanMode / plan sidecar written
- **Task graph change** — TaskCreate, or TaskUpdate that changed state
- **Memory write** — a memory file created/updated
- **Change** — first Edit/Write to a file (subsequent edits to the same
  file are ordinary)
- **User steering** — a user turn that redirected the approach (heuristic:
  user turn followed by plan/task changes; keep conservative)

These get a gutter marker and a one-line label on the spine:

```text
◆ User      "flat/tree 모드가 따로 있는 것부터 불편하다"      ← steering
▼ Assistant "통합 Session Flow로 재설계합니다"
  ▣ plan updated: session-flow-redesign                       ← decision row
  ├─ ✓ Explore agent …
▼ Assistant "구현을 시작합니다"
  ▣ tasks +3: FlowIndex / renderer / spine                    ← decision row
  ▣ memory: svelte-effect-conflicts (new)
```

- Marker glyph `▣` + distinct color; ordinary turns stay visually quiet so
  markers pop.
- The session summary line shows a decision count; a dedicated filter
  (`/ is:decision`) collapses the spine to decision rows only — this is
  the "why did the agent do this" executive view: user steering + plans +
  task changes + memory + first-touches, in order.
- Each marker is an artifact occurrence with full provenance, so `J` jumps
  to the turn and `Enter` inspects the plan/task/memory/diff content.

This reframes the doc's facets: refs/images/changes/stats are *what was
produced*; decision markers are *why the production happened*. Both hang
off the same FlowIndex.

## Data layer: FlowIndex

### FlowNode

```go
type FlowNode struct {
    ID       string        // session:<id> | turn:<uuid> | agent:<id> | wf:<run> | phase:<run>:<n> | shell:<tool_use_id>
    Kind     FlowNodeKind  // session, turn, agent, workflow, phase, shelljob
    ParentID string
    Children []string

    Origin     FlowOrigin    // exact spawn edge
    Transcript *TranscriptRef // nil for summary-only nodes (wf phase, summary-only agents)

    DirectFacets  FacetSummary // produced by this node itself
    SubtreeFacets FacetSummary // aggregated over descendants
}

type FlowOrigin struct {
    MessageUUID string
    EntryIndex  int
    BlockIndex  int
    ToolUseID   string    // exact edge; empty only for legacy fallback
    Timestamp   time.Time
}
```

Turns are nodes too — artifact provenance needs to point at the exact
assistant/user turn, not just "somewhere in the parent".

### Exact edges (prerequisite for everything)

- **Agents**: the `tool_use_id → agentId` map is already built during load
  (`conversation.go:2263-2278`) but discarded. Persist it into `Subagent`
  as `SpawnToolUseID` + origin message UUID. Timestamp placement
  (`conversation_render.go:547-574`) becomes fallback-only for legacy
  transcripts.
- **Workflows**: join `WorkflowRun`/`WorkflowAgent` (run, phase index,
  label, state, metrics) onto agent nodes at conversation open, not only
  in the browser preview. A `Workflow` tool_use block links to its run
  node.
- **Shell jobs**: `LoadShellJobsFromEntries` already correlates
  Monitor/background-Bash with BashOutput/KillShell via tool_use_id — use
  that as the spawn edge directly. Additionally parse BashOutput *results*
  (not just inputs) to distinguish completed/failed from merely-polled.
- **Legacy `Task` tool**: unify with `Agent` resolution; one shared
  resolver, tool_use_id first, timestamp fallback second.

### Artifact

All extractions (refs, URLs, images, changes, files, tasks, plans, hooks,
commands, errors, decision markers) become occurrences of one type:

```go
type Artifact struct {
    ID     string
    Kind   ArtifactKind
    NodeID string       // owning FlowNode
    Key    string       // canonical identity: URL, file path, task ID…
    Origin ArtifactOrigin
    Data   any
}

type ArtifactOrigin struct {
    SessionID   string
    Transcript  string    // owning transcript path — NOT implicit currentSess
    MessageUUID string
    EntryIndex  int
    BlockIndex  int
    ToolUseID   string
    AgentID     string
    WorkflowRun string
    PhaseIndex  int
    Timestamp   time.Time
}
```

Rules:

- **Store every occurrence; dedupe only at presentation.** One canonical
  PR ref may have N occurrences across parent and agents. Current code
  loses this (refs keep first timestamp only; changes overwrite per-path
  diffs).
- **Changes need both views**: occurrence timeline (each Edit/Write, who,
  when) and net diff (final per-file state). Label transcript-recorded
  diffs distinctly from working-tree diffs.
- **Images resolve against `Origin.Transcript`**, fixing the current bug
  where agent images are looked up in the parent transcript
  (`app.go:3712-3718`).
- **Stats**: compute from transcripts where available; fall back to
  workflow summary metrics marked `estimated`; never double-count an
  agent that appears in both.

### Scope

```go
type Scope int // NodeOnly | Subtree | SessionWide
```

Explicit everywhere. No silent parent fallback (today's `x` menu falls
back to the parent transcript when the agent scope is empty — replace
with an explicit "No changes in this agent — press s for session scope").
Project memory is `InheritedProject`, shown as a session resource, not
badged onto every node.

### API sketch

```go
BuildSessionFlow(sess *Session) (*FlowIndex, error)
(*FlowIndex) Node(id string) (FlowNode, bool)
(*FlowIndex) Children(id string) []FlowNode
(*FlowIndex) Artifacts(nodeID string, kind ArtifactKind, scope Scope) []Artifact
(*FlowIndex) Facets(nodeID string, scope Scope) FacetSummary
(*FlowIndex) Stats(nodeID string, scope Scope) SessionStats
(*FlowIndex) Decisions(scope Scope) []Artifact   // ordered decision markers
```

Caches keyed by `nodeID + transcript modtime + facet + scope` (today's
single-entry session-ID caches go stale on live sessions).

## View layer

### Layout

```text
┌ Session Flow ───────────────────────────────────────────────────────────┐
│ ccx 89c3f · 18 turns · 7 agents · 1 wf · ▣5 decisions                  │
│ [Δ12] [R4] [I3] [Tasks 4/7] [183k tok]                                 │
├──────────────────────────────┬──────────────────────────────────────────┤
│ FLOW (spine)                 │ INSPECTOR (selected node)                │
│                              │ agent: tui-reader                        │
│ ◆ User  Improve session view │ [Overview] [Conversation] [Δ3] [R1] [S] │
│ ▼ Asst  Inspecting…          │ Scope: [Node] Subtree Session            │
│   ├─ ✓ Explore  Δ3 R1  18k  │                                          │
│   │  └─ ✓ nested   Δ1   4k  │ …facet content…                          │
│   └─ ✓ WF review  Δ5   71k  │                                          │
│      ├─ Understand           │ Origin: assistant turn 4 → Agent call    │
│      │  ├─ ✓ model-reader    │ Enter: conversation · J: origin          │
│      │  └─ ✓ tui-reader      │                                          │
│      └─ ✗ Verify             │                                          │
│         └─ ✗ reviewer !2     │                                          │
│ ▼ Asst  Findings…            │                                          │
│   ▣ plan updated             │                                          │
│ ◆ User  Remove flat/tree     │                                          │
└──────────────────────────────┴──────────────────────────────────────────┘
```

- The flat/entity-tree toggle is **removed**. One list, always.
- Lifecycle nodes indent under their exact origin turn; workflows nest
  run → phase → agent; agents nest recursively.
- Facet badges on rows: `Δ` changes, `R` refs, `I` images, `!` errors,
  token count. Dim/`Σ`-marked when the count is a subtree aggregate.
  Narrow terminals collapse to a single `◆N` glyph.
- Header badges are session-wide aggregates; selecting one opens the
  reverse-provenance view (e.g. changes grouped by workflow → phase →
  agent → parent conversation).

### Inspector

Fixed to the selected node — not a rotating preview mode. Tabs are the
node's non-empty facets. Overview first: status, prompt/output summary,
artifact counts, children, origin.

- Decision-marker rows inspect to the plan/task/memory/diff content.
- Workflow nodes inspect to run summary + phase list (absorbing the
  standalone workflow preview).
- Monitor/shell nodes inspect to a lifecycle timeline: start command,
  each poll with its result tail (errors auto-expanded), kill/exit,
  latest output. Poll blocks disappear from the spine.
- `z` toggles zoom: the same inspector state rendered full-width. This
  **replaces msgfull** — no separate keymap, drill-down, or `x` logic.

### Tool renderer registry

Replaces the generic `Tool: <name>` + raw JSON dump for instantaneous
tools:

```go
type ToolRenderer interface {
    Headline(input json.RawMessage, width int) string  // visible when folded
    Body(input json.RawMessage, result *ContentBlock, width int) []string
}
```

- Headline carries the essence while folded: `$ git log --oneline`,
  `Edit messages.go +3 -1`, `Grep "toolUseToAgent" → 4 matches`.
- tool_use + tool_result render as one visual unit (`⎿` result line),
  like Claude Code itself.
- **Errors auto-expand** their result tail; successes stay folded.
- Edit/Write render as diffs (reuse `diff.go`); raw JSON demoted to the
  deepest disclosure level (`l` on an expanded block), never removed.
- Existing Skill/Monitor/MCP special cases migrate into the registry.
- Disclosure ladder per block: headline → semantic body → raw source.

### Keys

| Key | Action |
|---|---|
| `j/k` | move on spine / in inspector |
| `h/l` | fold/unfold node or block; `l` on expanded block → raw |
| `Enter` | inspect node / open artifact / expand block |
| `Tab` | flow ↔ inspector focus |
| `z` | zoom inspector full-width |
| `J` | jump to exact origin turn |
| `[` `]` | prev/next sibling or occurrence |
| `s` | scope: Node → Subtree → Session |
| `/` | filter current pane (`is:decision`, `is:error`, `tool:Edit`…) |
| `Esc` | one level up, always |

`Shift+Enter` optionally focuses a subtree as the new spine root (with
breadcrumb), preserving today's recursive drill-down for very deep
sessions.

## What gets deleted

- `conversationPaneFlat` / `conversationPaneEntityTree` and the Tab toggle
- Entity tree's flat Agents/Jobs/Monitors sections
- Standalone workflow preview as a navigation destination
- msgfull as an independent navigation context (its zoom/read value moves
  to `z`)
- Duplicate `Agent` vs `Task` drill-down implementations
- Best-effort `J` origin jump (`conversation.go:2371-2433`) — replaced by
  exact edges
- Parent-transcript fallback in `x` menus
- Timestamp-based agent placement (kept only as legacy fallback)
- Known bug: agent duration always 0 (`conversation.go:2601-2603`,
  `ag.Timestamp - ag.Timestamp`)

## Implementation phases

All five phases are complete on `feature/session-flow`:

1. **FlowIndex** — exact edges, workflow joins, artifact provenance,
   scope aggregation, decision markers, and owning-transcript image
   resolution.
2. **Tool renderer registry** — semantic headlines/bodies, result pairing,
   error expansion, and raw-source disclosure.
3. **Unified flow spine** — lifecycle nodes at exact origins, decision
   markers, facet badges, and no separate entity-tree mode.
4. **Inspector + zoom** — non-empty facet tabs, Node/Subtree/Session scope,
   reverse provenance, and full-width rendering of the same inspector state.
5. **Cleanup** — removed standalone msgfull and artifact-page navigation,
   parent-transcript fallbacks, duplicate scoped menus, and stale tests;
   README now documents workflow-agent storage under
   `subagents/workflows/<run>/`.

## Product acceptance

The view must answer, without mode switches:

1. What happened, in what order? — spine
2. Who ran what? — lifecycle nodes
3. **Why? Where were decisions made?** — decision markers + `is:decision`
4. What did each execution produce? — node facets
5. Where did this change/ref/image come from? — provenance + `J`
6. What happened inside this agent? — inspector, subtree focus
