package session

import (
	"fmt"
	"sort"
	"time"
)

// FlowNodeKind classifies a node on the session flow spine.
type FlowNodeKind string

const (
	FlowNodeSession  FlowNodeKind = "session"
	FlowNodeTurn     FlowNodeKind = "turn"
	FlowNodeAgent    FlowNodeKind = "agent"
	FlowNodeWorkflow FlowNodeKind = "workflow"
	FlowNodePhase    FlowNodeKind = "phase"
	FlowNodeShell    FlowNodeKind = "shelljob"
)

// Node ID scheme: "session:<id>" | "turn:<uuid>" | "agent:<id>" | "wf:<run>" |
// "phase:<run>:<n>" | "shell:<tool_use_id>".
func FlowSessionNodeID(sessionID string) string { return "session:" + sessionID }
func FlowTurnNodeID(uuid string) string         { return "turn:" + uuid }
func FlowAgentNodeID(agentID string) string     { return "agent:" + agentID }
func FlowWorkflowNodeID(runID string) string    { return "wf:" + runID }
func FlowPhaseNodeID(runID string, n int) string {
	return fmt.Sprintf("phase:%s:%d", runID, n)
}
func FlowShellNodeID(toolUseID string) string { return "shell:" + toolUseID }

// FlowOrigin is the exact spawn edge of a node: the point in the parent
// transcript where the node came into existence. ToolUseID is the exact edge;
// it is empty only when a legacy transcript forced a timestamp fallback.
type FlowOrigin struct {
	MessageUUID string
	EntryIndex  int
	BlockIndex  int
	ToolUseID   string
	Timestamp   time.Time
}

// TranscriptRef points at the JSONL transcript backing a node. Nil Transcript
// on a FlowNode means the node is summary-only (workflow phases, workflow
// agents whose transcript was cleaned up, shell jobs).
type TranscriptRef struct {
	Path string // absolute path to the .jsonl transcript
	ID   string // session or agent id (file base without extension prefix)
}

// Scope selects how far artifact/facet/stat queries aggregate.
type Scope int

const (
	ScopeNode    Scope = iota // the node itself only
	ScopeSubtree              // the node and all descendants
	ScopeSession              // the whole session tree
)

// FacetSummary is the per-node artifact rollup: occurrence counts per kind,
// approximate token volume, and error count. Tokens are output tokens for
// transcript-backed nodes/turns; for summary-only workflow agents they come
// from the workflow summary (see FlowNode.Estimated).
type FacetSummary struct {
	Counts map[ArtifactKind]int
	Tokens int64
	Errors int
}

func (f *FacetSummary) add(kind ArtifactKind, n int) {
	if f.Counts == nil {
		f.Counts = make(map[ArtifactKind]int)
	}
	f.Counts[kind] += n
}

func (f *FacetSummary) merge(o FacetSummary) {
	for k, n := range o.Counts {
		f.add(k, n)
	}
	f.Tokens += o.Tokens
	f.Errors += o.Errors
}

// FlowNode is one node on the session flow spine: the session root, a
// conversation turn, an agent, a workflow run/phase, or a shell job.
type FlowNode struct {
	ID       string
	Kind     FlowNodeKind
	ParentID string
	Children []string

	Origin     FlowOrigin
	Transcript *TranscriptRef // nil for summary-only nodes

	// Label is a short human identity: agent type/workflow label for agents,
	// run name for workflows, phase title for phases, command description for
	// shell jobs. Empty for turns (presentation derives previews from entries).
	Label string

	// Estimated marks summary-only workflow agents whose metrics come from the
	// workflow summary rather than a transcript.
	Estimated bool

	DirectFacets  FacetSummary // produced by this node itself
	SubtreeFacets FacetSummary // aggregated over this node and descendants
}

// FlowStats is SessionStats plus the estimated components contributed by
// summary-only workflow agents. Estimated metrics are kept in separate fields
// so transcript-derived numbers stay exact and nothing is double-counted.
type FlowStats struct {
	SessionStats
	Estimated           bool // some aggregated component used summary metrics
	EstimatedTokens     int64
	EstimatedToolCalls  int64
	EstimatedDurationMS int64
}

// FlowIndex is the queryable index over one session's flow: nodes with exact
// spawn edges, artifact occurrences with provenance, and decision markers.
// Build with BuildSessionFlow. Not safe for concurrent mutation.
type FlowIndex struct {
	SessionID string
	RootID    string

	nodes     map[string]*FlowNode
	order     []string // node insertion order (chronological build order)
	artifacts []Artifact
	byNode    map[string][]int // node ID → artifact indices

	agents []Subagent    // discovered agents (with spawn origins filled)
	runs   []WorkflowRun // workflow run summaries
	shells []ShellJob    // shell/monitor jobs from the parent transcript

	statsCache  map[string]SessionStats // transcript path → scanned stats
	wfAgentByID map[string]WorkflowAgent
}

// Node returns the node with the given ID.
func (fi *FlowIndex) Node(id string) (FlowNode, bool) {
	n, ok := fi.nodes[id]
	if !ok {
		return FlowNode{}, false
	}
	return *n, true
}

// Children returns the child nodes of the given node in build order.
func (fi *FlowIndex) Children(id string) []FlowNode {
	n, ok := fi.nodes[id]
	if !ok {
		return nil
	}
	out := make([]FlowNode, 0, len(n.Children))
	for _, cid := range n.Children {
		if c, ok := fi.nodes[cid]; ok {
			out = append(out, *c)
		}
	}
	return out
}

// Nodes returns all nodes in build order.
func (fi *FlowIndex) Nodes() []FlowNode {
	out := make([]FlowNode, 0, len(fi.order))
	for _, id := range fi.order {
		out = append(out, *fi.nodes[id])
	}
	return out
}

// Agents returns the session's discovered subagents with spawn origins filled.
func (fi *FlowIndex) Agents() []Subagent { return fi.agents }

// Workflows returns the session's workflow run summaries.
func (fi *FlowIndex) Workflows() []WorkflowRun { return fi.runs }

// ShellJobs returns the parent transcript's shell/monitor jobs.
func (fi *FlowIndex) ShellJobs() []ShellJob { return fi.shells }

// scopeNodeIDs resolves the set of node IDs covered by (nodeID, scope).
func (fi *FlowIndex) scopeNodeIDs(nodeID string, scope Scope) map[string]bool {
	set := make(map[string]bool)
	switch scope {
	case ScopeSession:
		for id := range fi.nodes {
			set[id] = true
		}
	case ScopeSubtree:
		fi.collectSubtree(nodeID, set)
	default: // ScopeNode
		if _, ok := fi.nodes[nodeID]; ok {
			set[nodeID] = true
		}
	}
	return set
}

func (fi *FlowIndex) collectSubtree(id string, set map[string]bool) {
	n, ok := fi.nodes[id]
	if !ok || set[id] {
		return
	}
	set[id] = true
	for _, c := range n.Children {
		fi.collectSubtree(c, set)
	}
}

// Artifacts returns artifact occurrences owned by nodes in scope, in build
// (chronological-per-transcript) order. Empty kind matches all kinds. Every
// occurrence is returned; use DedupeArtifactsByKey for presentation dedupe.
func (fi *FlowIndex) Artifacts(nodeID string, kind ArtifactKind, scope Scope) []Artifact {
	set := fi.scopeNodeIDs(nodeID, scope)
	var out []Artifact
	for i := range fi.artifacts {
		a := &fi.artifacts[i]
		if !set[a.NodeID] {
			continue
		}
		if kind != "" && a.Kind != kind {
			continue
		}
		out = append(out, *a)
	}
	return out
}

// Facets returns the facet summary for (nodeID, scope). ScopeNode returns the
// node's DirectFacets; ScopeSubtree its precomputed SubtreeFacets; ScopeSession
// the root's SubtreeFacets.
func (fi *FlowIndex) Facets(nodeID string, scope Scope) FacetSummary {
	switch scope {
	case ScopeSession:
		nodeID = fi.RootID
		scope = ScopeSubtree
	}
	n, ok := fi.nodes[nodeID]
	if !ok {
		return FacetSummary{}
	}
	if scope == ScopeSubtree {
		return n.SubtreeFacets
	}
	return n.DirectFacets
}

// Stats aggregates transcript statistics over (nodeID, scope). Transcript-
// backed nodes contribute exact ScanSessionStats numbers; summary-only
// workflow agents contribute their workflow-summary metrics via the Estimated*
// fields. An agent with both a transcript and a summary entry is counted once,
// from its transcript. Turn/phase/shell nodes carry no transcript and
// contribute nothing directly.
func (fi *FlowIndex) Stats(nodeID string, scope Scope) FlowStats {
	set := fi.scopeNodeIDs(nodeID, scope)
	var out FlowStats
	for _, id := range fi.order {
		if !set[id] {
			continue
		}
		n := fi.nodes[id]
		if n.Transcript != nil {
			st, ok := fi.transcriptStats(n.Transcript.Path)
			if ok {
				mergeSessionStats(&out.SessionStats, st)
			}
			continue
		}
		if n.Kind == FlowNodeAgent && n.Estimated {
			if wa, ok := fi.wfAgentByID[agentIDFromNodeID(n.ID)]; ok {
				out.Estimated = true
				out.EstimatedTokens += wa.Tokens
				out.EstimatedToolCalls += wa.ToolCalls
				out.EstimatedDurationMS += wa.DurationMS
			}
		}
	}
	return out
}

func (fi *FlowIndex) transcriptStats(path string) (SessionStats, bool) {
	if st, ok := fi.statsCache[path]; ok {
		return st, true
	}
	st, err := ScanSessionStats(path)
	if err != nil {
		return SessionStats{}, false
	}
	fi.statsCache[path] = st
	return st, true
}

func agentIDFromNodeID(nodeID string) string {
	const p = "agent:"
	if len(nodeID) > len(p) && nodeID[:len(p)] == p {
		return nodeID[len(p):]
	}
	return ""
}

// mergeSessionStats accumulates src into dst: sums counters, merges maps,
// widens the timestamp window, and appends series.
func mergeSessionStats(dst *SessionStats, src SessionStats) {
	dst.TotalInputTokens += src.TotalInputTokens
	dst.TotalOutputTokens += src.TotalOutputTokens
	dst.TotalCacheReadTokens += src.TotalCacheReadTokens
	dst.TotalCacheCreationTokens += src.TotalCacheCreationTokens
	dst.OutputTokenSeries = append(dst.OutputTokenSeries, src.OutputTokenSeries...)

	mergeIntMap(&dst.ToolCounts, src.ToolCounts)
	mergeIntMap(&dst.MCPToolCounts, src.MCPToolCounts)
	mergeIntMap(&dst.CommandCounts, src.CommandCounts)
	mergeIntMap(&dst.SkillCounts, src.SkillCounts)
	mergeIntMap(&dst.AgentCounts, src.AgentCounts)
	mergeIntMap(&dst.ToolErrors, src.ToolErrors)
	mergeIntMap(&dst.SkillErrors, src.SkillErrors)
	mergeIntMap(&dst.CommandErrors, src.CommandErrors)
	mergeIntMap(&dst.Models, src.Models)
	mergeIntMap(&dst.HookCounts, src.HookCounts)
	mergeIntMap(&dst.HookEventCounts, src.HookEventCounts)

	dst.WriteCount += src.WriteCount
	dst.EditCount += src.EditCount
	dst.ReadCount += src.ReadCount
	dst.BashCount += src.BashCount
	if src.FilesTouched != nil {
		if dst.FilesTouched == nil {
			dst.FilesTouched = make(map[string]bool)
		}
		for k := range src.FilesTouched {
			dst.FilesTouched[k] = true
		}
	}

	dst.ToolResultCount += src.ToolResultCount
	dst.ToolErrorCount += src.ToolErrorCount
	dst.ErrorTimestamps = append(dst.ErrorTimestamps, src.ErrorTimestamps...)

	dst.MessageCount += src.MessageCount
	dst.UserMsgCount += src.UserMsgCount
	dst.AsstMsgCount += src.AsstMsgCount
	dst.CompactionCount += src.CompactionCount
	dst.TurnsPerRequest = append(dst.TurnsPerRequest, src.TurnsPerRequest...)
	dst.ModelSwitches += src.ModelSwitches
	dst.MsgTimestamps = append(dst.MsgTimestamps, src.MsgTimestamps...)

	if src.ModelTokens != nil {
		if dst.ModelTokens == nil {
			dst.ModelTokens = make(map[string]*ModelUsage)
		}
		for m, u := range src.ModelTokens {
			du := dst.ModelTokens[m]
			if du == nil {
				du = &ModelUsage{}
				dst.ModelTokens[m] = du
			}
			du.InputTokens += u.InputTokens
			du.OutputTokens += u.OutputTokens
			du.CacheReadTokens += u.CacheReadTokens
			du.CacheCreationTokens += u.CacheCreationTokens
		}
	}

	if !src.FirstTimestamp.IsZero() && (dst.FirstTimestamp.IsZero() || src.FirstTimestamp.Before(dst.FirstTimestamp)) {
		dst.FirstTimestamp = src.FirstTimestamp
	}
	if src.LastTimestamp.After(dst.LastTimestamp) {
		dst.LastTimestamp = src.LastTimestamp
	}
}

func mergeIntMap(dst *map[string]int, src map[string]int) {
	if len(src) == 0 {
		return
	}
	if *dst == nil {
		*dst = make(map[string]int, len(src))
	}
	for k, v := range src {
		(*dst)[k] += v
	}
}

// ---- exact spawn edges ------------------------------------------------------

// BuildToolUseToAgentMap scans entries for tool_result entries that carry an
// AgentID (from toolUseResult.agentId) and maps tool_use_id → agent ID. This is
// the exact spawn edge between an Agent/Task tool call and its subagent.
func BuildToolUseToAgentMap(entries []Entry) map[string]string {
	m := make(map[string]string)
	for _, e := range entries {
		if e.AgentID == "" {
			continue
		}
		for _, b := range e.Content {
			if b.Type == "tool_result" && b.ID != "" {
				m[b.ID] = e.AgentID
			}
		}
	}
	return m
}

// AttachSpawnOrigins fills each subagent's exact spawn edge (SpawnToolUseID,
// OriginMessageUUID, OriginEntryIndex) from the parent transcript entries.
// Agents with no exact edge (legacy transcripts) are left untouched so
// timestamp placement remains the fallback.
func AttachSpawnOrigins(agents []Subagent, entries []Entry) {
	if len(agents) == 0 {
		return
	}
	toolUseToAgent := BuildToolUseToAgentMap(entries)
	if len(toolUseToAgent) == 0 {
		return
	}
	// Invert: agent ID → spawning tool_use ID.
	spawnByAgent := make(map[string]string, len(toolUseToAgent))
	for tuID, agID := range toolUseToAgent {
		spawnByAgent[agID] = tuID
	}
	for i := range agents {
		tuID, ok := spawnByAgent[agents[i].ID]
		if !ok {
			continue
		}
		agents[i].SpawnToolUseID = tuID
		agents[i].OriginEntryIndex = -1
		// Locate the tool_use block itself for the message UUID + entry index.
		if ei, _, e, found := findToolUseBlock(entries, tuID); found {
			agents[i].OriginMessageUUID = e.UUID
			agents[i].OriginEntryIndex = ei
		}
	}
}

// findToolUseBlock locates the entry/block holding the tool_use with the given ID.
func findToolUseBlock(entries []Entry, toolUseID string) (entryIdx, blockIdx int, entry Entry, ok bool) {
	if toolUseID == "" {
		return 0, 0, Entry{}, false
	}
	for i := range entries {
		for bi, b := range entries[i].Content {
			if b.Type == "tool_use" && b.ID == toolUseID {
				return i, bi, entries[i], true
			}
		}
	}
	return 0, 0, Entry{}, false
}

// FillWorkflowAgentMeta fills WorkflowLabel, WorkflowPhaseIndex, and
// WorkflowPhaseTitle on workflow-spawned subagents (in place) by joining
// against the run summaries' workflowProgress. Non-workflow agents are left
// untouched.
func FillWorkflowAgentMeta(runs []WorkflowRun, agents []Subagent) {
	byID := make(map[string]WorkflowAgent)
	for _, r := range runs {
		for _, wa := range r.Agents {
			if wa.AgentID != "" {
				byID[wa.AgentID] = wa
			}
		}
	}
	for i := range agents {
		if agents[i].WorkflowRunID == "" {
			continue
		}
		wa, ok := byID[agents[i].ID]
		if !ok {
			continue
		}
		if wa.Label != "" {
			agents[i].WorkflowLabel = wa.Label
		}
		agents[i].WorkflowPhaseIndex = wa.PhaseIndex
		agents[i].WorkflowPhaseTitle = wa.PhaseTitle
	}
}

// ---- builder ----------------------------------------------------------------

// maxAgentDepth caps recursive nested-subagent loading.
const maxAgentDepth = 4

// BuildSessionFlow loads the session's transcript and builds the FlowIndex:
// turn nodes for user/assistant entries, agent nodes wired to their exact spawn
// edges (recursively including nested subagents), workflow run/phase nodes,
// shell job nodes, artifact occurrences with provenance, and decision markers.
func BuildSessionFlow(sess *Session) (*FlowIndex, error) {
	entries, err := LoadMessages(sess.FilePath)
	if err != nil {
		return nil, err
	}
	fi := &FlowIndex{
		SessionID:   sess.ID,
		RootID:      FlowSessionNodeID(sess.ID),
		nodes:       make(map[string]*FlowNode),
		byNode:      make(map[string][]int),
		statsCache:  make(map[string]SessionStats),
		wfAgentByID: make(map[string]WorkflowAgent),
	}
	b := &flowBuilder{fi: fi, sess: sess}
	b.build(entries)
	return fi, nil
}

type flowBuilder struct {
	fi      *FlowIndex
	sess    *Session
	entries []Entry

	turnByUUID map[string]string // entry UUID → turn node ID
	turnByIdx  []string          // entry index → turn node ID
	visited    map[string]bool   // agent transcript paths (recursion guard)
}

func (b *flowBuilder) build(entries []Entry) {
	b.entries = entries
	b.visited = make(map[string]bool)

	root := &FlowNode{
		ID:   b.fi.RootID,
		Kind: FlowNodeSession,
		Transcript: &TranscriptRef{
			Path: b.sess.FilePath,
			ID:   b.sess.ID,
		},
	}
	b.addNode(root)

	b.buildTurns(entries)

	// Discover agents + workflows, wire exact edges.
	agents, _ := FindSubagents(b.sess.FilePath)
	AttachSpawnOrigins(agents, entries)
	runs, _ := FindWorkflows(b.sess.FilePath)
	FillWorkflowAgentMeta(runs, agents)
	b.fi.agents = agents
	b.fi.runs = runs
	for _, r := range runs {
		for _, wa := range r.Agents {
			if wa.AgentID != "" {
				b.fi.wfAgentByID[wa.AgentID] = wa
			}
		}
	}

	b.buildWorkflowNodes(runs)
	b.buildAgentNodes(agents)
	b.buildShellNodes(entries)

	// Artifacts: parent transcript owned by turn nodes; agent transcripts owned
	// by their agent node. b.fi.agents includes nested agents discovered while
	// building the agent subtrees; each transcript is emitted exactly once.
	b.emitTranscriptArtifacts(b.sess.FilePath, entries, "", "", 0)
	emitted := make(map[string]bool, len(b.fi.agents))
	for _, a := range b.fi.agents {
		if a.FilePath == "" || emitted[a.FilePath] {
			continue
		}
		emitted[a.FilePath] = true
		b.emitAgentArtifacts(a)
	}

	b.markFirstChangeDecisions()
	b.markSteeringDecisions(entries)

	b.indexArtifacts()
	b.computeFacets()
}

func (b *flowBuilder) addNode(n *FlowNode) {
	if _, exists := b.fi.nodes[n.ID]; exists {
		return
	}
	b.fi.nodes[n.ID] = n
	b.fi.order = append(b.fi.order, n.ID)
	if n.ParentID != "" {
		if p, ok := b.fi.nodes[n.ParentID]; ok {
			p.Children = append(p.Children, n.ID)
		}
	}
}

func (b *flowBuilder) buildTurns(entries []Entry) {
	b.turnByUUID = make(map[string]string, len(entries))
	b.turnByIdx = make([]string, len(entries))
	for i := range entries {
		e := &entries[i]
		if e.Role != "user" && e.Role != "assistant" {
			continue
		}
		id := FlowTurnNodeID(e.UUID)
		if e.UUID == "" {
			id = fmt.Sprintf("turn:idx:%d", i)
		}
		n := &FlowNode{
			ID:       id,
			Kind:     FlowNodeTurn,
			ParentID: b.fi.RootID,
			Origin: FlowOrigin{
				MessageUUID: e.UUID,
				EntryIndex:  i,
				Timestamp:   e.Timestamp,
			},
		}
		b.addNode(n)
		if e.UUID != "" {
			b.turnByUUID[e.UUID] = id
		}
		b.turnByIdx[i] = id
	}
}

// turnForEntry returns the turn node ID for a parent entry index, falling back
// to the session root.
func (b *flowBuilder) turnForEntry(i int) string {
	if i >= 0 && i < len(b.turnByIdx) && b.turnByIdx[i] != "" {
		return b.turnByIdx[i]
	}
	return b.fi.RootID
}

// buildWorkflowNodes creates wf:<run> and phase:<run>:<n> nodes. The run node
// attaches to the turn containing the Workflow tool_use whose result recorded
// the runId; if that edge can't be found it attaches to the session root.
func (b *flowBuilder) buildWorkflowNodes(runs []WorkflowRun) {
	for _, r := range runs {
		origin, parent := b.workflowOrigin(r.RunID)
		wfNode := &FlowNode{
			ID:       FlowWorkflowNodeID(r.RunID),
			Kind:     FlowNodeWorkflow,
			ParentID: parent,
			Origin:   origin,
			Label:    r.Name,
		}
		b.addNode(wfNode)
		for _, p := range r.Phases {
			b.addNode(&FlowNode{
				ID:       FlowPhaseNodeID(r.RunID, p.Index),
				Kind:     FlowNodePhase,
				ParentID: wfNode.ID,
				Origin:   origin,
				Label:    p.Title,
			})
		}
	}
}

// workflowOrigin locates the exact edge for a workflow run: the tool_result
// whose raw JSON recorded the runId (toolUseResult.runId). The spawn turn is
// the assistant entry holding the matching Workflow tool_use.
func (b *flowBuilder) workflowOrigin(runID string) (FlowOrigin, string) {
	if runID == "" {
		return FlowOrigin{}, b.fi.RootID
	}
	needle := `"` + runID + `"`
	for i := range b.entries {
		e := &b.entries[i]
		if !contains(e.RawJSON, needle) {
			continue
		}
		for bi, blk := range e.Content {
			if blk.Type != "tool_result" || blk.ID == "" {
				continue
			}
			origin := FlowOrigin{
				MessageUUID: e.UUID,
				EntryIndex:  i,
				BlockIndex:  bi,
				ToolUseID:   blk.ID,
				Timestamp:   e.Timestamp,
			}
			// Prefer the turn of the tool_use (spawn point) over the result turn.
			parent := b.turnForEntry(i)
			if ui, ubi, ue, ok := findToolUseBlock(b.entries, blk.ID); ok {
				origin.MessageUUID = ue.UUID
				origin.EntryIndex = ui
				origin.BlockIndex = ubi
				origin.Timestamp = ue.Timestamp
				parent = b.turnForEntry(ui)
			}
			return origin, parent
		}
	}
	return FlowOrigin{}, b.fi.RootID
}

// buildAgentNodes creates agent nodes for discovered subagents (recursively
// loading nested subagents) plus summary-only nodes for workflow agents whose
// transcripts are gone.
func (b *flowBuilder) buildAgentNodes(agents []Subagent) {
	transcribed := make(map[string]bool, len(agents))
	for _, a := range agents {
		transcribed[a.ID] = true
	}

	for _, a := range agents {
		parent := b.agentParent(a)
		b.addAgentSubtree(a, parent, 0)
	}

	// Summary-only workflow agents: present in the run summary but without a
	// transcript on disk. They still become nodes so the workflow shape stays
	// complete; metrics for them come from the summary (Estimated).
	for _, r := range b.fi.runs {
		for _, wa := range r.Agents {
			if wa.AgentID == "" || transcribed[wa.AgentID] {
				continue
			}
			parent := b.workflowAgentParent(r.RunID, wa.PhaseIndex)
			b.addNode(&FlowNode{
				ID:        FlowAgentNodeID(wa.AgentID),
				Kind:      FlowNodeAgent,
				ParentID:  parent,
				Label:     wa.Label,
				Estimated: true,
			})
		}
	}
}

// agentParent resolves where a top-level agent hangs on the spine: workflow
// phase/run for workflow agents; otherwise the exact origin turn; otherwise
// the timestamp-fallback turn.
func (b *flowBuilder) agentParent(a Subagent) string {
	if a.WorkflowRunID != "" {
		return b.workflowAgentParent(a.WorkflowRunID, a.WorkflowPhaseIndex)
	}
	if a.OriginMessageUUID != "" {
		if id, ok := b.turnByUUID[a.OriginMessageUUID]; ok {
			return id
		}
	}
	if a.SpawnToolUseID != "" && a.OriginEntryIndex >= 0 {
		return b.turnForEntry(a.OriginEntryIndex)
	}
	return b.timestampFallbackTurn(a.Timestamp)
}

func (b *flowBuilder) workflowAgentParent(runID string, phaseIndex int) string {
	if phaseIndex > 0 {
		if _, ok := b.fi.nodes[FlowPhaseNodeID(runID, phaseIndex)]; ok {
			return FlowPhaseNodeID(runID, phaseIndex)
		}
	}
	if _, ok := b.fi.nodes[FlowWorkflowNodeID(runID)]; ok {
		return FlowWorkflowNodeID(runID)
	}
	return b.fi.RootID
}

// timestampFallbackTurn is the legacy placement: the last assistant turn whose
// timestamp does not exceed the agent's. Used only when no exact edge exists.
func (b *flowBuilder) timestampFallbackTurn(ts time.Time) string {
	if ts.IsZero() {
		return b.fi.RootID
	}
	best := ""
	for i := range b.entries {
		e := &b.entries[i]
		if e.Role != "assistant" || e.Timestamp.IsZero() {
			continue
		}
		if !ts.Before(e.Timestamp) {
			best = b.turnForEntry(i)
		}
	}
	if best == "" {
		return b.fi.RootID
	}
	return best
}

// addAgentSubtree adds an agent node and recursively discovers its nested
// subagents (agents spawned by agents).
func (b *flowBuilder) addAgentSubtree(a Subagent, parent string, depth int) {
	if depth > maxAgentDepth || b.visited[a.FilePath] {
		return
	}
	b.visited[a.FilePath] = true

	label := a.AgentType
	if a.WorkflowLabel != "" {
		label = a.WorkflowLabel
	}
	node := &FlowNode{
		ID:       FlowAgentNodeID(a.ID),
		Kind:     FlowNodeAgent,
		ParentID: parent,
		Label:    label,
		Origin: FlowOrigin{
			MessageUUID: a.OriginMessageUUID,
			EntryIndex:  a.OriginEntryIndex,
			ToolUseID:   a.SpawnToolUseID,
			Timestamp:   a.Timestamp,
		},
		Transcript: &TranscriptRef{Path: a.FilePath, ID: a.ID},
	}
	b.addNode(node)

	nested, err := FindSubagents(a.FilePath)
	if err != nil || len(nested) == 0 {
		return
	}
	agentEntries, err := LoadMessages(a.FilePath)
	if err == nil {
		AttachSpawnOrigins(nested, agentEntries)
	}
	for _, n := range nested {
		b.fi.agents = append(b.fi.agents, n)
		b.addAgentSubtree(n, node.ID, depth+1)
	}
}

// buildShellNodes creates shell:<tool_use_id> nodes for the parent transcript's
// background Bash/Monitor jobs, attached to the turn holding the spawning
// tool_use block.
func (b *flowBuilder) buildShellNodes(entries []Entry) {
	jobs := LoadShellJobsFromEntries(entries)
	b.fi.shells = jobs
	for _, j := range jobs {
		if j.ID == "" {
			continue
		}
		origin := FlowOrigin{ToolUseID: j.ID, Timestamp: j.StartedAt}
		parent := b.fi.RootID
		if ei, bi, e, ok := findToolUseBlock(entries, j.ID); ok {
			origin.MessageUUID = e.UUID
			origin.EntryIndex = ei
			origin.BlockIndex = bi
			parent = b.turnForEntry(ei)
		}
		label := j.Description
		if label == "" {
			label = firstLine(j.Command)
		}
		b.addNode(&FlowNode{
			ID:       FlowShellNodeID(j.ID),
			Kind:     FlowNodeShell,
			ParentID: parent,
			Origin:   origin,
			Label:    label,
		})
	}
}

// emitAgentArtifacts loads an agent transcript and emits its artifacts owned by
// the agent node.
func (b *flowBuilder) emitAgentArtifacts(a Subagent) {
	entries, err := LoadMessages(a.FilePath)
	if err != nil {
		return
	}
	b.emitTranscriptArtifacts(a.FilePath, entries, a.ID, a.WorkflowRunID, a.WorkflowPhaseIndex)
}

// indexArtifacts builds the node → artifact-index lookup.
func (b *flowBuilder) indexArtifacts() {
	for i := range b.fi.artifacts {
		nid := b.fi.artifacts[i].NodeID
		b.fi.byNode[nid] = append(b.fi.byNode[nid], i)
	}
}

// computeFacets fills DirectFacets from owned artifacts and SubtreeFacets by
// bottom-up aggregation over children.
func (b *flowBuilder) computeFacets() {
	for id, idxs := range b.fi.byNode {
		n, ok := b.fi.nodes[id]
		if !ok {
			continue
		}
		for _, i := range idxs {
			a := &b.fi.artifacts[i]
			n.DirectFacets.add(a.Kind, 1)
			if a.Kind == ArtifactError {
				n.DirectFacets.Errors++
			}
		}
	}

	// Tokens: turns carry their own usage; agents carry transcript totals (or
	// summary totals when estimated).
	for _, id := range b.fi.order {
		n := b.fi.nodes[id]
		switch n.Kind {
		case FlowNodeTurn:
			i := n.Origin.EntryIndex
			if i >= 0 && i < len(b.entries) {
				if u := extractUsage([]byte(b.entries[i].RawJSON)); u != nil {
					n.DirectFacets.Tokens = u.OutputTokens
				}
			}
		case FlowNodeAgent:
			if n.Transcript != nil {
				if st, ok := b.fi.transcriptStats(n.Transcript.Path); ok {
					n.DirectFacets.Tokens = st.TotalOutputTokens
				}
			} else if n.Estimated {
				if wa, ok := b.fi.wfAgentByID[agentIDFromNodeID(n.ID)]; ok {
					n.DirectFacets.Tokens = wa.Tokens
				}
			}
		}
	}

	// Bottom-up subtree aggregation (children were always added after their
	// parent, so reverse build order visits children first).
	for i := len(b.fi.order) - 1; i >= 0; i-- {
		n := b.fi.nodes[b.fi.order[i]]
		n.SubtreeFacets.merge(n.DirectFacets)
		for _, cid := range n.Children {
			if c, ok := b.fi.nodes[cid]; ok {
				n.SubtreeFacets.merge(c.SubtreeFacets)
			}
		}
	}
}

// sortArtifactsByTime orders artifacts chronologically with a stable tiebreak
// on transcript + entry index. Used by decision ordering.
func sortArtifactsByTime(arts []Artifact) {
	sort.SliceStable(arts, func(i, j int) bool {
		ti, tj := arts[i].Origin.Timestamp, arts[j].Origin.Timestamp
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		if arts[i].Origin.Transcript != arts[j].Origin.Transcript {
			return arts[i].Origin.Transcript < arts[j].Origin.Transcript
		}
		return arts[i].Origin.EntryIndex < arts[j].Origin.EntryIndex
	})
}
