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
	Transcript  string // transcript containing the spawning tool_use
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
		if node, ok := fi.nodes[nodeID]; ok {
			// A phase is a grouping node whose content is its assigned agent
			// subtrees. Treating it as an empty metadata row makes every phase
			// appear identical in the inspector.
			if node.Kind == FlowNodePhase {
				fi.collectSubtree(nodeID, set)
			} else {
				set[nodeID] = true
			}
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
	if scope == ScopeSubtree || n.Kind == FlowNodePhase {
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

// BuildToolUseToAgentMap maps a result's tool_use_id to the child agent ID.
// Entry.AgentID is the transcript owner and must never be used as the child.
func BuildToolUseToAgentMap(entries []Entry) map[string]string {
	m := make(map[string]string)
	for _, e := range entries {
		if e.ToolResultAgentID == "" {
			continue
		}
		for _, b := range e.Content {
			if b.Type == "tool_result" && b.ID != "" {
				m[b.ID] = e.ToolResultAgentID
			}
		}
	}
	return m
}

// AttachSpawnOrigins fills exact root-transcript spawn edges. It remains the
// public compatibility helper; BuildSessionFlow uses attachSessionSpawnOrigins
// to repeat the same operation across every agent transcript.
func AttachSpawnOrigins(agents []Subagent, entries []Entry) {
	attachSpawnOrigins(agents, entries, "", "")
}

func attachSpawnOrigins(agents []Subagent, entries []Entry, parentAgentID, transcript string) {
	if len(agents) == 0 {
		return
	}
	byID := make(map[string]*Subagent, len(agents))
	for i := range agents {
		byID[agents[i].ID] = &agents[i]
	}
	for toolUseID, childID := range BuildToolUseToAgentMap(entries) {
		child := byID[childID]
		if child == nil || child.SpawnToolUseID != "" {
			continue
		}
		child.ParentAgentID = parentAgentID
		child.SpawnToolUseID = toolUseID
		child.OriginEntryIndex = -1
		child.OriginBlockIndex = -1
		child.OriginTranscript = transcript
		if ei, bi, e, found := findToolUseBlock(entries, toolUseID); found {
			child.OriginMessageUUID = e.UUID
			child.OriginEntryIndex = ei
			child.OriginBlockIndex = bi
		}
	}
}

// attachSessionSpawnOrigins scans the root and every discovered agent
// transcript. Ordinary nested agents are sibling files, so the parent relation
// must come from these result edges rather than directory recursion.
func attachSessionSpawnOrigins(agents []Subagent, rootEntries []Entry, rootPath string) map[string][]Entry {
	entriesByAgent := make(map[string][]Entry, len(agents))
	attachSpawnOrigins(agents, rootEntries, "", rootPath)
	for i := range agents {
		entries, err := LoadMessages(agents[i].FilePath)
		if err != nil {
			continue
		}
		entriesByAgent[agents[i].ID] = entries
		attachSpawnOrigins(agents, entries, agents[i].ID, agents[i].FilePath)
	}
	return entriesByAgent
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

	turnByUUID      map[string]string // root entry UUID → turn node ID
	turnByIdx       []string          // root entry index → turn node ID
	agentByID       map[string]Subagent
	agentEntries    map[string][]Entry
	workflowOrigins map[string]FlowOrigin
	workflowParents map[string]string
}

func (b *flowBuilder) build(entries []Entry) {
	b.entries = entries

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

	// Discover every transcript once, then derive exact parent edges from the
	// root and all agent transcripts. Ordinary nested agents are sibling files.
	agents, _ := FindSubagents(b.sess.FilePath)
	b.agentEntries = attachSessionSpawnOrigins(agents, entries, b.sess.FilePath)
	b.agentByID = make(map[string]Subagent, len(agents))
	for _, agent := range agents {
		b.agentByID[agent.ID] = agent
	}

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
	b.indexWorkflowOrigins(entries)

	b.buildWorkflowNodes(runs)
	b.buildAgentNodes(agents)
	b.buildShellNodes(entries)
	b.rebuildChildren()

	// Artifacts: root transcript occurrences belong to turns; every agent
	// transcript belongs to its agent node and is emitted exactly once.
	b.emitTranscriptArtifacts(b.sess.FilePath, entries, "", "", 0)
	emitted := make(map[string]bool, len(b.fi.agents))
	for _, a := range b.fi.agents {
		if a.FilePath == "" || emitted[a.FilePath] {
			continue
		}
		emitted[a.FilePath] = true
		b.emitAgentArtifacts(a)
	}

	b.markChangeDecisions()
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

// indexWorkflowOrigins locates workflow result edges in the root and every
// agent transcript. A workflow started by an agent belongs beneath that agent;
// a root workflow belongs beneath its exact spawning turn.
func (b *flowBuilder) indexWorkflowOrigins(rootEntries []Entry) {
	b.workflowOrigins = make(map[string]FlowOrigin)
	b.workflowParents = make(map[string]string)
	index := func(entries []Entry, transcript, parentAgentID string) {
		for i := range entries {
			e := &entries[i]
			if e.ToolResultRunID == "" {
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
					Transcript:  transcript,
				}
				parent := b.fi.RootID
				if parentAgentID != "" {
					parent = FlowAgentNodeID(parentAgentID)
				} else {
					parent = b.turnForEntry(i)
				}
				if ui, ubi, ue, ok := findToolUseBlock(entries, blk.ID); ok {
					origin.MessageUUID = ue.UUID
					origin.EntryIndex = ui
					origin.BlockIndex = ubi
					origin.Timestamp = ue.Timestamp
					if parentAgentID == "" {
						parent = b.turnForEntry(ui)
					}
				}
				b.workflowOrigins[e.ToolResultRunID] = origin
				b.workflowParents[e.ToolResultRunID] = parent
			}
		}
	}
	index(rootEntries, b.sess.FilePath, "")
	for agentID, entries := range b.agentEntries {
		if agent, ok := b.agentByID[agentID]; ok {
			index(entries, agent.FilePath, agentID)
		}
	}
}

// buildWorkflowNodes creates run and phase grouping nodes. Only the run claims
// the exact launch origin; phases are scopes defined by their assigned agents.
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
				Label:    p.Title,
			})
		}
	}
}

func (b *flowBuilder) workflowOrigin(runID string) (FlowOrigin, string) {
	if origin, ok := b.workflowOrigins[runID]; ok {
		parent := b.workflowParents[runID]
		if parent == "" {
			parent = b.fi.RootID
		}
		return origin, parent
	}
	return FlowOrigin{}, b.fi.RootID
}

// buildAgentNodes creates all transcript-backed agents from the session-wide
// connection graph, plus summary-only workflow agents whose transcript is gone.
func (b *flowBuilder) buildAgentNodes(agents []Subagent) {
	transcribed := make(map[string]bool, len(agents))
	for _, a := range agents {
		transcribed[a.ID] = true
		label := a.AgentType
		if a.WorkflowLabel != "" {
			label = a.WorkflowLabel
		}
		b.addNode(&FlowNode{
			ID:       FlowAgentNodeID(a.ID),
			Kind:     FlowNodeAgent,
			ParentID: b.agentParent(a),
			Label:    label,
			Origin: FlowOrigin{
				MessageUUID: a.OriginMessageUUID,
				EntryIndex:  a.OriginEntryIndex,
				BlockIndex:  a.OriginBlockIndex,
				ToolUseID:   a.SpawnToolUseID,
				Timestamp:   a.Timestamp,
				Transcript:  a.OriginTranscript,
			},
			Transcript: &TranscriptRef{Path: a.FilePath, ID: a.ID},
		})
	}

	for _, r := range b.fi.runs {
		for _, wa := range r.Agents {
			if wa.AgentID == "" || transcribed[wa.AgentID] {
				continue
			}
			b.addNode(&FlowNode{
				ID:        FlowAgentNodeID(wa.AgentID),
				Kind:      FlowNodeAgent,
				ParentID:  b.workflowAgentParent(r.RunID, wa.PhaseIndex),
				Label:     wa.Label,
				Estimated: true,
			})
		}
	}
}

func (b *flowBuilder) agentParent(a Subagent) string {
	if a.ParentAgentID != "" && a.ParentAgentID != a.ID {
		if _, ok := b.agentByID[a.ParentAgentID]; ok && !b.agentParentCycle(a.ID, a.ParentAgentID) {
			return FlowAgentNodeID(a.ParentAgentID)
		}
	}
	if a.WorkflowRunID != "" {
		return b.workflowAgentParent(a.WorkflowRunID, a.WorkflowPhaseIndex)
	}
	if a.OriginTranscript == b.sess.FilePath {
		if a.OriginMessageUUID != "" {
			if id, ok := b.turnByUUID[a.OriginMessageUUID]; ok {
				return id
			}
		}
		if a.SpawnToolUseID != "" && a.OriginEntryIndex >= 0 {
			return b.turnForEntry(a.OriginEntryIndex)
		}
	}
	return b.timestampFallbackTurn(a.Timestamp)
}

func (b *flowBuilder) agentParentCycle(childID, parentID string) bool {
	seen := map[string]bool{childID: true}
	for parentID != "" {
		if seen[parentID] {
			return true
		}
		seen[parentID] = true
		parent, ok := b.agentByID[parentID]
		if !ok {
			return false
		}
		parentID = parent.ParentAgentID
	}
	return false
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

// rebuildChildren makes parent linkage independent of node insertion order.
// This is required when an agent launches a workflow that is indexed first.
func (b *flowBuilder) rebuildChildren() {
	for _, node := range b.fi.nodes {
		node.Children = nil
	}
	for _, id := range b.fi.order {
		if id == b.fi.RootID {
			continue
		}
		node := b.fi.nodes[id]
		parent, ok := b.fi.nodes[node.ParentID]
		if !ok || node.ParentID == id {
			node.ParentID = b.fi.RootID
			parent = b.fi.nodes[b.fi.RootID]
		}
		parent.Children = append(parent.Children, id)
	}
}

// timestampFallbackTurn is used only for legacy agents without an exact edge.
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

	// Aggregate by graph traversal rather than insertion order. Workflows may be
	// indexed before the agent that launched them, and malformed legacy data can
	// contain cycles; visiting guards keep both cases safe.
	visited := make(map[string]bool, len(b.fi.nodes))
	visiting := make(map[string]bool, len(b.fi.nodes))
	var aggregate func(string) FacetSummary
	aggregate = func(id string) FacetSummary {
		n, ok := b.fi.nodes[id]
		if !ok {
			return FacetSummary{}
		}
		if visited[id] {
			return n.SubtreeFacets
		}
		if visiting[id] {
			return FacetSummary{}
		}
		visiting[id] = true
		total := n.DirectFacets
		for _, childID := range n.Children {
			total.merge(aggregate(childID))
		}
		delete(visiting, id)
		visited[id] = true
		n.SubtreeFacets = total
		return total
	}
	for _, id := range b.fi.order {
		aggregate(id)
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
