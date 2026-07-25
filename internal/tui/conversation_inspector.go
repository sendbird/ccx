package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/aymanbagabas/go-udiff"
	"github.com/sendbird/ccx/internal/session"
)

type inspectorTab int

const (
	inspectorOverview inspectorTab = iota
	inspectorConversation
	inspectorChanges
	inspectorFiles
	inspectorRefs
	inspectorImages
	inspectorStats
)

var inspectorTabOrder = []inspectorTab{
	inspectorOverview,
	inspectorConversation,
	inspectorChanges,
	inspectorFiles,
	inspectorRefs,
	inspectorImages,
	inspectorStats,
}

type conversationInspector struct {
	Tab            inspectorTab
	Scope          session.Scope
	Zoom           bool
	ZoomPrevFocus  bool
	NodeID         string
	Rendered       string
	ExplicitTab    inspectorTab
	ExplicitNodeID string
	Explicit       bool
	// ReturnToID is retained as a compatibility/debug mirror of the top history
	// frame. Navigation restoration uses History so nested jumps can unwind one
	// exact step at a time.
	ReturnToID string
	History    []inspectorNavFrame

	// MetaTargets is parallel to the synthetic Entry.Content blocks rendered for
	// a session-meta row (memory/tasks-plan/summary). Index i describes what
	// Enter/J does when block cursor i is selected. Empty for non-meta previews.
	MetaTargets []metaEntryTarget
	// MetaDrill is the memory note filename currently drilled into (file detail
	// mode); empty means the file-list mode. Only meaningful for the memory row.
	MetaDrill string
	// MemorySearch is the committed full-text query for the memory row; non-empty
	// switches the memory pane from the file list to a cross-file match list.
	// Starting a search clears MetaDrill — search is its own mode. Cleared on Esc
	// or when the memory row is left.
	MemorySearch string
	// MetaPlanDrill is the artifact key of the plan currently shown in detail.
	// Empty means the combined tasks/plans list is shown.
	MetaPlanDrill string
	// ChangesByFile flips the Changes tab from the default per-occurrence list
	// (one diff per Edit/Write) to a per-file view that merges every
	// occurrence of a file under one header, showing a cumulative net diff
	// when reconstruction is reliable and per-occurrence diffs otherwise.
	ChangesByFile bool
}

// metaEntryTarget describes the jump/drill action bound to one selectable block
// in a session-meta inspector preview (memory files, decisions, tasks, plans,
// crons). It is stored parallel to the synthetic Entry.Content blocks.
type metaEntryTarget struct {
	kind        metaTargetKind
	fileName    string // memory note filename (memory-file drill target)
	filePath    string // absolute backing file path for $EDITOR open (memory/plan/scratchpad)
	transcript  string // transcript owning the origin (root or subagent)
	messageUUID string // originating turn to jump to (empty = no jump)
	entryIndex  int    // origin entry index, local to transcript
	blockIdx    int    // block within the source entry to focus (-1 = none)
	taskID      string // task ID (task targets)
	planKey     string // plan artifact key (plan-detail target)
	url         string // ref/URL row → Enter opens in browser (metaTargetRef)
}

type metaTargetKind int

const (
	metaTargetNone       metaTargetKind = iota
	metaTargetMemoryFile                // file-list row → Enter drills into the file
	metaTargetDecision                  // flow-summary decision → jump to origin turn
	metaTargetTask                      // task row → open task view / definition turn
	metaTargetTodo                      // todo row → jump to latest TodoWrite occurrence
	metaTargetPlan                      // plan row → Enter opens data; J jumps to origin
	metaTargetCron                      // cron row (informational; no origin to jump to)
	metaTargetRef                       // PR/Jira ref row → Enter opens URL in browser
	metaTargetScratchpad                // scratchpad file row → Enter opens in $EDITOR
)

// jumpable reports whether a target kind can jump to a conversation turn. None,
// separators, and crons (which have no recorded origin) cannot, so Enter/J on
// those rows must not fall back to a bare entry-index of 0.
func (t metaTargetKind) jumpable() bool {
	switch t {
	case metaTargetMemoryFile, metaTargetDecision, metaTargetTask, metaTargetTodo, metaTargetPlan, metaTargetRef:
		return true
	default:
		return false
	}
}

func (t inspectorTab) String() string {
	switch t {
	case inspectorConversation:
		return "Conversation"
	case inspectorChanges:
		return "Changes"
	case inspectorFiles:
		return "Files"
	case inspectorRefs:
		return "Refs"
	case inspectorImages:
		return "Images"
	case inspectorStats:
		return "Stats"
	default:
		return "Overview"
	}
}

func inspectorScopeName(scope session.Scope) string {
	switch scope {
	case session.ScopeSubtree:
		return "Subtree"
	case session.ScopeSession:
		return "Session"
	default:
		return "Node"
	}
}

// inspectorScopesFor returns the scope selector entries meaningful for a node,
// in cycle order. Scope on a node is *relative* to that node: Node is the node
// itself, Subtree is the node plus its descendants. Whole-session aggregation is
// not a per-node concern — it lives in the pinned "Session Flow" row — so Session
// is not offered for an ordinary node.
//
// Subtree is offered only when it resolves to a different set than Node: the node
// must have descendants AND not be a phase (a phase is auto-expanded to its
// subtree even under ScopeNode, see FlowIndex.scopeNodeIDs, so Node≡Subtree). A
// node with no distinct subtree gets a single [Node] entry and the selector
// collapses to a non-interactive label.
//
// The session root is the one exception: it *is* the whole session, so it keeps
// Session (and only Session — Node/Subtree on the root would just re-derive it).
func (a *App) inspectorScopesFor(nodeID string) []session.Scope {
	if a.conv.flow != nil && nodeID != "" && nodeID == a.conv.flow.RootID {
		return []session.Scope{session.ScopeSession}
	}
	if a.conv.flow != nil && nodeID != "" && len(a.conv.flow.Children(nodeID)) > 0 {
		if node, ok := a.conv.flow.Node(nodeID); ok && node.Kind != session.FlowNodePhase {
			return []session.Scope{session.ScopeNode, session.ScopeSubtree}
		}
	}
	return []session.Scope{session.ScopeNode}
}

// normalizeInspectorScope collapses a scope the current node does not support
// (Subtree on a childless node) down to Node so the selector never highlights an
// entry it does not display.
func (a *App) normalizeInspectorScope(nodeID string) {
	for _, s := range a.inspectorScopesFor(nodeID) {
		if s == a.conv.inspector.Scope {
			return
		}
	}
	a.conv.inspector.Scope = session.ScopeNode
}

// convItemFlowNodeID maps every unified-flow row to the FlowIndex node it owns.
// Decision markers belong to the node that owns the artifact occurrence.
func convItemFlowNodeID(item convItem, flow *session.FlowIndex) string {
	if flow == nil {
		return ""
	}
	switch item.kind {
	case convSessionMeta:
		return flow.RootID
	case convMsg:
		if item.merged.entry.UUID != "" {
			id := session.FlowTurnNodeID(item.merged.entry.UUID)
			if _, ok := flow.Node(id); ok {
				return id
			}
		}
		for i := item.merged.startIdx; i <= item.merged.endIdx; i++ {
			id := fmt.Sprintf("turn:idx:%d", i)
			if _, ok := flow.Node(id); ok {
				return id
			}
		}
	case convAgent:
		id := item.agent.ID
		if id == "" {
			id = item.agent.ShortID
		}
		return session.FlowAgentNodeID(id)
	case convWorkflow:
		return session.FlowWorkflowNodeID(item.workflow.RunID)
	case convPhase:
		return session.FlowPhaseNodeID(item.workflow.RunID, item.phase.Index)
	case convShell:
		return session.FlowShellNodeID(item.shell.ID)
	case convDecision:
		return item.decision.NodeID
	}
	return ""
}

func inspectorHasConversation(item convItem, node session.FlowNode) bool {
	if item.kind == convMsg {
		return len(item.merged.entry.Content) > 0
	}
	if item.kind == convAgent {
		return node.Transcript != nil && item.agent.FilePath != ""
	}
	return false
}

func inspectorHasOverview(item convItem) bool {
	switch item.kind {
	case convAgent, convWorkflow, convPhase, convShell, convDecision, convSessionMeta:
		return true
	default:
		return false
	}
}

func availableInspectorTabs(item convItem, flow *session.FlowIndex, nodeID string, scope session.Scope) []inspectorTab {
	if flow == nil || nodeID == "" {
		if item.kind == convMsg {
			return []inspectorTab{inspectorConversation}
		}
		return []inspectorTab{inspectorOverview}
	}
	node, ok := flow.Node(nodeID)
	if !ok {
		return []inspectorTab{inspectorOverview}
	}
	facets := flow.Facets(nodeID, scope)
	tabs := make([]inspectorTab, 0, len(inspectorTabOrder))
	if inspectorHasOverview(item) {
		tabs = append(tabs, inspectorOverview)
	}
	if inspectorHasConversation(item, node) {
		tabs = append(tabs, inspectorConversation)
	}
	if facets.Counts[session.ArtifactChange] > 0 {
		tabs = append(tabs, inspectorChanges)
	}
	if facets.Counts[session.ArtifactFile] > 0 {
		tabs = append(tabs, inspectorFiles)
	}
	if facets.Counts[session.ArtifactRef]+facets.Counts[session.ArtifactURL] > 0 {
		tabs = append(tabs, inspectorRefs)
	}
	if facets.Counts[session.ArtifactImage] > 0 {
		tabs = append(tabs, inspectorImages)
	}
	stats := flow.Stats(nodeID, scope)
	if hasInspectorStats(stats, flow.Children(nodeID)) {
		tabs = append(tabs, inspectorStats)
	}
	if len(tabs) == 0 {
		tabs = append(tabs, inspectorOverview)
	}
	return tabs
}

func hasInspectorStats(stats session.FlowStats, children []session.FlowNode) bool {
	return stats.MessageCount > 0 || len(stats.ToolCounts) > 0 || stats.ToolErrorCount > 0 ||
		stats.TotalInputTokens+stats.TotalOutputTokens+stats.TotalCacheReadTokens+stats.TotalCacheCreationTokens > 0 ||
		stats.EstimatedTokens+stats.EstimatedToolCalls > 0 || len(children) > 0
}

func containsInspectorTab(tabs []inspectorTab, target inspectorTab) bool {
	for _, tab := range tabs {
		if tab == target {
			return true
		}
	}
	return false
}

func validInspectorTab(current inspectorTab, tabs []inspectorTab) inspectorTab {
	if containsInspectorTab(tabs, current) {
		return current
	}
	if containsInspectorTab(tabs, inspectorOverview) {
		return inspectorOverview
	}
	return tabs[0]
}

func cycleInspectorTab(current inspectorTab, tabs []inspectorTab, delta int) inspectorTab {
	if len(tabs) == 0 {
		return inspectorOverview
	}
	idx := 0
	for i, tab := range tabs {
		if tab == current {
			idx = i
			break
		}
	}
	idx = (idx + delta + len(tabs)) % len(tabs)
	return tabs[idx]
}

func (a *App) inspectorTabs(item convItem, nodeID string) []inspectorTab {
	tabs := availableInspectorTabs(item, a.conv.flow, nodeID, a.conv.inspector.Scope)
	if a.conv.inspector.Explicit && a.conv.inspector.ExplicitNodeID == nodeID &&
		!containsInspectorTab(tabs, a.conv.inspector.ExplicitTab) {
		tabs = append(tabs, a.conv.inspector.ExplicitTab)
	}
	return tabs
}

func (a *App) syncInspectorSelection(item convItem) (session.FlowNode, bool) {
	flow := a.conv.flow
	nodeID := convItemFlowNodeID(item, flow)
	if nodeID == "" || flow == nil {
		return session.FlowNode{}, false
	}
	node, ok := flow.Node(nodeID)
	if !ok {
		return session.FlowNode{}, false
	}
	if a.conv.inspector.Explicit && a.conv.inspector.ExplicitNodeID != nodeID {
		a.conv.inspector.Explicit = false
	}
	a.conv.inspector.NodeID = nodeID
	a.normalizeInspectorScope(nodeID)
	tabs := a.inspectorTabs(item, nodeID)
	a.conv.inspector.Tab = validInspectorTab(a.conv.inspector.Tab, tabs)
	return node, true
}

func (a *App) openInspector(tab inspectorTab, scope session.Scope, zoom bool) {
	sp := &a.conv.split
	a.conv.inspector.Scope = scope
	a.conv.inspector.Tab = tab
	if item, ok := a.selectedConversationItem(); ok {
		a.conv.inspector.Explicit = true
		a.conv.inspector.ExplicitTab = tab
		a.conv.inspector.ExplicitNodeID = convItemFlowNodeID(item, a.conv.flow)
	}
	sp.Show = true
	if zoom {
		a.conv.inspector.ZoomPrevFocus = sp.Focus
		sp.Focus = true
	}
	a.conv.inspector.Zoom = zoom
	sp.PreviewOnly = zoom
	sp.CacheKey = ""
	a.updateConvPreview()
}

func (a *App) setInspectorZoom(zoom bool) {
	sp := &a.conv.split
	if a.conv.inspector.Zoom == zoom && sp.PreviewOnly == zoom {
		return
	}
	oldOffset := sp.Preview.YOffset
	oldCursor := -1
	if sp.Folds != nil {
		oldCursor = sp.Folds.BlockCursor
	}
	if zoom {
		a.conv.inspector.ZoomPrevFocus = sp.Focus
		sp.Show = true
		sp.Focus = true
	} else {
		sp.Focus = a.conv.inspector.ZoomPrevFocus
	}
	a.conv.inspector.Zoom = zoom
	sp.PreviewOnly = zoom
	sp.CacheKey = ""
	a.updateConvPreview()
	if sp.Folds != nil && oldCursor >= 0 && oldCursor < len(sp.Folds.Entry.Content) {
		sp.Folds.BlockCursor = oldCursor
		sp.RefreshFoldCursor(a.width, a.splitRatio)
	}
	maxOffset := max(sp.Preview.TotalLineCount()-sp.Preview.Height, 0)
	sp.Preview.YOffset = min(oldOffset, maxOffset)
}

func (a *App) cycleInspectorTabBy(delta int) {
	item, ok := a.selectedConversationItem()
	if !ok || a.conv.flow == nil {
		return
	}
	nodeID := convItemFlowNodeID(item, a.conv.flow)
	if nodeID == "" {
		return
	}
	tabs := a.inspectorTabs(item, nodeID)
	a.conv.inspector.Explicit = false
	a.conv.inspector.Tab = cycleInspectorTab(a.conv.inspector.Tab, tabs, delta)
	a.conv.split.CacheKey = ""
	a.updateConvPreview()
}

func (a *App) cycleInspectorScope() {
	scopes := a.inspectorScopesFor(a.conv.inspector.NodeID)
	idx := 0
	for i, s := range scopes {
		if s == a.conv.inspector.Scope {
			idx = i
			break
		}
	}
	a.conv.inspector.Scope = scopes[(idx+1)%len(scopes)]
	a.conv.split.CacheKey = ""
	a.updateConvPreview()
}

func (a *App) inspectorHeader(item convItem, node session.FlowNode) string {
	tabs := a.inspectorTabs(item, node.ID)
	var tabLabels []string
	for _, tab := range tabs {
		label := tab.String()
		if tab == a.conv.inspector.Tab {
			label = "[" + label + "]"
		}
		tabLabels = append(tabLabels, label)
	}

	identity := node.ID
	if node.Label != "" {
		identity += " · " + node.Label
	}
	zoom := "INSPECTOR"
	if a.conv.inspector.Zoom {
		zoom = "INSPECTOR / ZOOM"
	}
	var scopeLabels []string
	for _, scope := range a.inspectorScopesFor(node.ID) {
		label := inspectorScopeName(scope)
		if scope == a.conv.inspector.Scope {
			label = "[" + label + "]"
		}
		scopeLabels = append(scopeLabels, label)
	}
	return fmt.Sprintf("%s\n%s\n%s\nScope: %s\n%s", zoom, identity, strings.Join(tabLabels, "  "), strings.Join(scopeLabels, "  "), strings.Repeat("─", 24))
}

func (a *App) renderInspector(item convItem, node session.FlowNode, content string) string {
	return a.inspectorHeader(item, node) + "\n\n" + content
}

func (a *App) renderInspectorTab(item convItem, node session.FlowNode) string {
	switch a.conv.inspector.Tab {
	case inspectorConversation:
		return ""
	case inspectorChanges:
		return a.renderInspectorChanges(node.ID)
	case inspectorFiles:
		return a.renderInspectorFiles(node.ID)
	case inspectorRefs:
		return a.renderInspectorRefs(node.ID)
	case inspectorImages:
		return a.renderInspectorImages(node.ID)
	case inspectorStats:
		return a.renderInspectorStats(node.ID)
	default:
		return a.renderInspectorOverview(item, node)
	}
}

func (a *App) renderInspectorOverview(item convItem, node session.FlowNode) string {
	var content string
	switch item.kind {
	case convAgent:
		facets := a.conv.flow.Facets(node.ID, a.conv.inspector.Scope)
		var b strings.Builder
		fmt.Fprintf(&b, "# Agent: %s\n\nStatus: %s\n", inspectorNodeLabel(node, item.agent.ShortID), item.agentStatus)
		if item.agent.AgentType != "" {
			fmt.Fprintf(&b, "Type: %s\n", item.agent.AgentType)
		}
		if item.agent.FirstPrompt != "" {
			fmt.Fprintf(&b, "\nPrompt: %s\n", item.agent.FirstPrompt)
		}
		fmt.Fprintf(&b, "Artifacts: %s\nChildren: %d\n", inspectorFacetSummary(facets), len(a.conv.flow.Children(node.ID)))
		writeInspectorOrigin(&b, node.Origin, node.Transcript)
		content = b.String()
	case convWorkflow:
		content = renderWorkflowInspector(item.workflow, a.conv.flow.Facets(node.ID, a.conv.inspector.Scope))
	case convPhase:
		content = renderPhaseInspector(item.workflow, item.phase, a.conv.flow, node.ID, a.conv.inspector.Scope)
	case convShell:
		content = renderShellInspector(item.shell)
	case convDecision:
		content = a.renderDecisionInspector(item.decision)
	case convSessionMeta:
		switch item.sessionMeta {
		case "summary":
			content = a.renderFlowSummary()
		case "memory":
			content = a.buildMemoryContent(a.conv.sess)
		case "refs":
			content = a.buildRefsListText()
		case "scratchpad":
			content = a.buildScratchpadContent(a.conv.sess)
		default:
			content = a.buildTasksPlanContent(a.conv.sess)
		}
	default:
		content = "No overview for this node.\n"
	}
	return content
}

func inspectorNodeLabel(node session.FlowNode, fallback string) string {
	if node.Label != "" {
		return node.Label
	}
	if fallback != "" {
		return fallback
	}
	return node.ID
}

func inspectorFacetSummary(f session.FacetSummary) string {
	parts := make([]string, 0, 4)
	if n := f.Counts[session.ArtifactChange]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d changes", n))
	}
	if n := f.Counts[session.ArtifactFile]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d files", n))
	}
	if n := f.Counts[session.ArtifactRef] + f.Counts[session.ArtifactURL]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d refs/URLs", n))
	}
	if n := f.Counts[session.ArtifactImage]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d images", n))
	}
	if f.Errors > 0 {
		parts = append(parts, fmt.Sprintf("%d errors", f.Errors))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, " · ")
}

func writeInspectorOrigin(b *strings.Builder, origin session.FlowOrigin, transcript *session.TranscriptRef) {
	if origin.MessageUUID != "" || origin.EntryIndex >= 0 || origin.ToolUseID != "" {
		fmt.Fprintf(b, "Origin: message %s · entry %d · block %d", origin.MessageUUID, origin.EntryIndex+1, origin.BlockIndex+1)
		if origin.ToolUseID != "" {
			fmt.Fprintf(b, " · tool %s", origin.ToolUseID)
		}
		b.WriteByte('\n')
	}
	if transcript != nil {
		fmt.Fprintf(b, "Transcript: %s\n", transcript.Path)
	}
}

func (a *App) inspectorArtifacts(nodeID string, kinds ...session.ArtifactKind) []session.Artifact {
	if a.conv.flow == nil {
		return nil
	}
	var out []session.Artifact
	for _, kind := range kinds {
		out = append(out, a.conv.flow.Artifacts(nodeID, kind, a.conv.inspector.Scope)...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Origin.Timestamp.Before(out[j].Origin.Timestamp)
	})
	return out
}

func (a *App) renderInspectorChanges(nodeID string) string {
	arts := a.inspectorArtifacts(nodeID, session.ArtifactChange)
	if len(arts) == 0 {
		return fmt.Sprintf("# Changes\n\nNo changes in this %s scope.\n", strings.ToLower(inspectorScopeName(a.conv.inspector.Scope)))
	}
	if a.conv.inspector.ChangesByFile {
		return a.renderInspectorChangesByFile(arts)
	}
	diffWidth := max(a.conv.split.PreviewWidth(a.width, a.splitRatio)-4, 20)
	var b strings.Builder
	b.WriteString("# Changes\n")
	lastOwner := ""
	for i, art := range arts {
		owner := art.NodeID + " · " + art.Origin.Transcript
		if owner != lastOwner {
			fmt.Fprintf(&b, "\n## %s\n", owner)
			lastOwner = owner
		}
		data, _ := art.Data.(session.ChangeData)
		summary := data.Summary
		if summary == "" {
			summary = changeInputSummary(data.ToolName, data.ToolInput)
		}
		fmt.Fprintf(&b, "\n%d. %s %s", i+1, data.ToolName, art.Key)
		if summary != "" {
			fmt.Fprintf(&b, " · %s", summary)
		}
		b.WriteByte('\n')
		fmt.Fprintf(&b, "   origin: %s\n", inspectorArtifactOrigin(art.Origin))
		if diff := changeDiff(data, diffWidth); diff != "" {
			b.WriteString("\n" + diff + "\n")
		}
	}
	return b.String()
}

// changeDiff renders the inline diff for one ChangeData occurrence by
// reconstructing the tool_use block toolDiffOutput expects.
func changeDiff(data session.ChangeData, width int) string {
	if data.ToolName == "" || data.ToolInput == "" {
		return ""
	}
	block := session.ContentBlock{Type: "tool_use", ToolName: data.ToolName, ToolInput: data.ToolInput}
	return strings.TrimRight(toolDiffOutput(block, width), "\n")
}

// renderInspectorChangesByFile groups change occurrences by file path (in
// first-seen order) and renders one section per file. When the file's history
// can be reliably reconstructed (the first occurrence is a Write establishing a
// baseline and every later Edit applies cleanly), the section shows a single
// cumulative net diff from that baseline to the final state. Otherwise it falls
// back to the per-occurrence diffs so the user still sees every change.
func (a *App) renderInspectorChangesByFile(arts []session.Artifact) string {
	diffWidth := max(a.conv.split.PreviewWidth(a.width, a.splitRatio)-4, 20)

	order := make([]string, 0, len(arts))
	byFile := make(map[string][]session.Artifact, len(arts))
	for _, art := range arts {
		fp := art.Key
		if fp == "" {
			fp = "(unknown)"
		}
		if _, seen := byFile[fp]; !seen {
			order = append(order, fp)
		}
		byFile[fp] = append(byFile[fp], art)
	}

	var b strings.Builder
	b.WriteString("# Changes (by file)\n")
	for _, fp := range order {
		occ := byFile[fp]
		fmt.Fprintf(&b, "\n## %s%s\n", session.ShortenPath(fp, homeDir()), dimStyle.Render(fmt.Sprintf(" · %d occurrence(s)", len(occ))))
		if initial, final, ok := reconstructFileChanges(occ); ok {
			if diff, err := renderCumulativeFileDiff(fp, initial, final, diffWidth); err == nil {
				if diff != "" {
					b.WriteString("\n" + diff + "\n")
				} else {
					b.WriteString(dimStyle.Render("  (no net change across occurrences)\n"))
				}
				continue
			}
		}
		b.WriteString(dimStyle.Render("  (cumulative diff unavailable — showing per-occurrence)\n"))
		for i, art := range occ {
			data, _ := art.Data.(session.ChangeData)
			summary := data.Summary
			if summary == "" {
				summary = changeInputSummary(data.ToolName, data.ToolInput)
			}
			fmt.Fprintf(&b, "\n%d. %s %s", i+1, data.ToolName, session.ShortenPath(art.Key, homeDir()))
			if summary != "" {
				fmt.Fprintf(&b, " · %s", summary)
			}
			b.WriteByte('\n')
			fmt.Fprintf(&b, "   origin: %s\n", inspectorArtifactOrigin(art.Origin))
			if diff := changeDiff(data, diffWidth); diff != "" {
				b.WriteString("\n" + diff + "\n")
			}
		}
	}
	return b.String()
}

// reconstructFileChanges replays the change occurrences (already filtered to
// one file path, in chronological order) to recover the file's initial and
// final content. ok is true only when reconstruction is reliable: the first
// occurrence must be a Write (establishing a baseline) and every subsequent
// Edit must apply cleanly. Unknown tools (MultiEdit/NotebookEdit) or a missing
// old_string make reconstruction bail so the caller can fall back.
func reconstructFileChanges(occ []session.Artifact) (initial, final string, ok bool) {
	if len(occ) == 0 {
		return "", "", false
	}
	sort.SliceStable(occ, func(i, j int) bool {
		return occ[i].Origin.Timestamp.Before(occ[j].Origin.Timestamp)
	})
	first, _ := occ[0].Data.(session.ChangeData)
	if !isWriteTool(first.ToolName) {
		return "", "", false
	}
	content, ho := writeContent(first.ToolInput)
	if !ho {
		return "", "", false
	}
	initial = content
	for _, op := range occ[1:] {
		d, _ := op.Data.(session.ChangeData)
		switch {
		case isWriteTool(d.ToolName):
			c, ok := writeContent(d.ToolInput)
			if !ok {
				return "", "", false
			}
			content = c
		case d.ToolName == "Edit":
			c, ok := applyEditToContent(content, d.ToolInput)
			if !ok {
				return "", "", false
			}
			content = c
		default:
			// MultiEdit/NotebookEdit/unknown — bail to per-occurrence fallback.
			return "", "", false
		}
	}
	return initial, content, true
}

// Caps for the cumulative diff view: bound the udiff input size (LCS cost is
// quadratic in the worst case) and the rendered output length so a huge Write
// plus a small Edit cannot stall the TUI or blow up the preview.
const (
	maxCumulativeDiffBytes = 200_000
	maxCumulativeDiffLines = 300
)

var errCumulativeDiffTooLarge = errors.New("cumulative diff too large")

// renderCumulativeFileDiff produces a colorized unified diff between initial
// and final file contents. Returns ("", nil) when the two are identical, or
// (..., err) when the diff cannot be computed safely (content too large or
// udiff reports an inconsistency) so the caller can fall back to per-occurrence
// diffs. Output is line-capped to keep the preview bounded.
func renderCumulativeFileDiff(filePath, initial, final string, width int) (string, error) {
	if len(initial)+len(final) > maxCumulativeDiffBytes {
		return "", errCumulativeDiffTooLarge
	}
	short := session.ShortenPath(filePath, homeDir())
	edits := udiff.Strings(initial, final)
	raw, err := udiff.ToUnified(short, short, initial, edits, udiff.DefaultContextLines)
	if err != nil {
		return "", err
	}
	raw = strings.TrimRight(raw, "\n")
	if raw == "" {
		return "", nil
	}
	lines := strings.Split(raw, "\n")
	var b strings.Builder
	shown := 0
	for _, line := range lines {
		if shown >= maxCumulativeDiffLines {
			remaining := len(lines) - shown
			b.WriteString(diffHeaderStyle.Render(fmt.Sprintf("  ... (%d more diff lines, truncated)", remaining)) + "\n")
			break
		}
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			b.WriteString(diffHeaderStyle.Render("  "+line) + "\n")
		case strings.HasPrefix(line, "@@"):
			b.WriteString(diffHunkStyle.Render("  "+line) + "\n")
		case strings.HasPrefix(line, "+"):
			b.WriteString(renderDiffLine("+", line[1:], width-4, diffAddStyle) + "\n")
		case strings.HasPrefix(line, "-"):
			b.WriteString(renderDiffLine("-", line[1:], width-4, diffDelStyle) + "\n")
		default:
			b.WriteString(diffCtxStyle.Render("  "+line) + "\n")
		}
		shown++
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func (a *App) renderInspectorFiles(nodeID string) string {
	arts := a.inspectorArtifacts(nodeID, session.ArtifactFile)
	if len(arts) == 0 {
		return fmt.Sprintf("# Files\n\nNo files in this %s scope.\n", strings.ToLower(inspectorScopeName(a.conv.inspector.Scope)))
	}
	counts := make(map[string]int)
	for _, art := range arts {
		counts[art.Key]++
	}
	var b strings.Builder
	b.WriteString("# Files\n\n")
	for i, art := range arts {
		toolName, _ := art.Data.(string)
		if toolName == "" {
			toolName = "Tool"
		}
		fmt.Fprintf(&b, "%d. [%s] %s · occurrence %d/%d\n", i+1, toolName, art.Key, occurrenceIndex(arts, i), counts[art.Key])
		fmt.Fprintf(&b, "   origin: %s\n", inspectorArtifactOrigin(art.Origin))
	}
	return b.String()
}

func changeInputSummary(toolName, input string) string {
	if input == "" {
		return ""
	}
	var fields map[string]any
	if err := jsonUnmarshalString(input, &fields); err != nil {
		return ""
	}
	oldValue, _ := fields["old_string"].(string)
	newValue, _ := fields["new_string"].(string)
	if oldValue != "" || newValue != "" {
		return fmt.Sprintf("-%d/+%d lines", lineCount(oldValue), lineCount(newValue))
	}
	if toolName == "Write" {
		if content, _ := fields["content"].(string); content != "" {
			return fmt.Sprintf("%d lines", lineCount(content))
		}
	}
	return ""
}

func lineCount(value string) int {
	if value == "" {
		return 0
	}
	return strings.Count(value, "\n") + 1
}

func (a *App) renderInspectorRefs(nodeID string) string {
	arts := a.inspectorArtifacts(nodeID, session.ArtifactRef, session.ArtifactURL)
	if len(arts) == 0 {
		return fmt.Sprintf("# References\n\nNo references or URLs in this %s scope.\n", strings.ToLower(inspectorScopeName(a.conv.inspector.Scope)))
	}
	counts := make(map[string]int)
	for _, art := range arts {
		counts[string(art.Kind)+"\x00"+art.Key]++
	}
	var b strings.Builder
	b.WriteString("# References & URLs\n\n")
	for i, art := range arts {
		kind := "URL"
		if art.Kind == session.ArtifactRef {
			kind = "Ref"
		}
		fmt.Fprintf(&b, "%d. [%s] %s · occurrence %d/%d\n", i+1, kind, art.Key, occurrenceIndex(arts, i), counts[string(art.Kind)+"\x00"+art.Key])
		fmt.Fprintf(&b, "   origin: %s\n", inspectorArtifactOrigin(art.Origin))
	}
	return b.String()
}

func occurrenceIndex(arts []session.Artifact, idx int) int {
	target := arts[idx]
	count := 0
	for i := 0; i <= idx; i++ {
		if arts[i].Kind == target.Kind && arts[i].Key == target.Key {
			count++
		}
	}
	return count
}

func (a *App) renderInspectorImages(nodeID string) string {
	arts := a.inspectorArtifacts(nodeID, session.ArtifactImage)
	if len(arts) == 0 {
		return fmt.Sprintf("# Images\n\nNo images in this %s scope.\n", strings.ToLower(inspectorScopeName(a.conv.inspector.Scope)))
	}
	var b strings.Builder
	b.WriteString("# Images\n\n")
	for i, art := range arts {
		pasteID, _ := art.Data.(int)
		fmt.Fprintf(&b, "%d. paste #%d\n", i+1, pasteID)
		fmt.Fprintf(&b, "   transcript: %s\n", art.Origin.Transcript)
		fmt.Fprintf(&b, "   owner: %s\n", art.NodeID)
		fmt.Fprintf(&b, "   origin: %s\n", inspectorArtifactOrigin(art.Origin))
	}
	return b.String()
}

func (a *App) renderInspectorStats(nodeID string) string {
	stats := a.conv.flow.Stats(nodeID, a.conv.inspector.Scope)
	facets := a.conv.flow.Facets(nodeID, a.conv.inspector.Scope)
	children := a.conv.flow.Children(nodeID)
	exactTokens := stats.TotalInputTokens + stats.TotalOutputTokens + stats.TotalCacheReadTokens + stats.TotalCacheCreationTokens
	toolCalls := 0
	for _, count := range stats.ToolCounts {
		toolCalls += count
	}
	var b strings.Builder
	b.WriteString("# Stats\n\n")
	fmt.Fprintf(&b, "Tokens: %d exact", exactTokens)
	if stats.EstimatedTokens > 0 {
		fmt.Fprintf(&b, " + ~%d estimated", stats.EstimatedTokens)
	}
	b.WriteByte('\n')
	fmt.Fprintf(&b, "Tool calls: %d exact", toolCalls)
	if stats.EstimatedToolCalls > 0 {
		fmt.Fprintf(&b, " + ~%d estimated", stats.EstimatedToolCalls)
	}
	b.WriteByte('\n')
	fmt.Fprintf(&b, "Errors: %d\n", max(stats.ToolErrorCount, facets.Errors))
	fmt.Fprintf(&b, "Messages: %d\nChildren: %d\n", stats.MessageCount, len(children))
	if len(children) > 0 {
		var counts = make(map[session.FlowNodeKind]int)
		for _, child := range children {
			counts[child.Kind]++
		}
		var kinds []string
		for kind, count := range counts {
			kinds = append(kinds, fmt.Sprintf("%s=%d", kind, count))
		}
		sort.Strings(kinds)
		fmt.Fprintf(&b, "Child kinds: %s\n", strings.Join(kinds, " · "))
	}
	return b.String()
}

func inspectorArtifactOrigin(origin session.ArtifactOrigin) string {
	parts := []string{origin.Transcript}
	if origin.AgentID != "" {
		parts = append(parts, "agent "+origin.AgentID)
	}
	if origin.MessageUUID != "" {
		parts = append(parts, "message "+origin.MessageUUID)
	}
	parts = append(parts, "entry "+strconv.Itoa(origin.EntryIndex+1), "block "+strconv.Itoa(origin.BlockIndex+1))
	if origin.ToolUseID != "" {
		parts = append(parts, "tool "+origin.ToolUseID)
	}
	return strings.Join(parts, " · ")
}

// Kept behind a tiny wrapper to keep the inspector's extraction helper focused
// and make malformed historical tool input degrade to an empty summary.
func jsonUnmarshalString(value string, target any) error {
	return json.Unmarshal([]byte(value), target)
}
