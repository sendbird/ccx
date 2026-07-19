package tui

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

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
	// ReturnToID is the item selected when a zoom entry jumped the list
	// selection elsewhere (e.g. a marker opening its parent turn); leaving
	// zoom restores it so "back" lands where the user entered from.
	ReturnToID string

	// MetaTargets is parallel to the synthetic Entry.Content blocks rendered for
	// a session-meta row (memory/tasks-plan/summary). Index i describes what
	// Enter/J does when block cursor i is selected. Empty for non-meta previews.
	MetaTargets []metaEntryTarget
	// MetaDrill is the memory note filename currently drilled into (file detail
	// mode); empty means the file-list mode. Only meaningful for the memory row.
	MetaDrill string
}

// metaEntryTarget describes the jump/drill action bound to one selectable block
// in a session-meta inspector preview (memory files, decisions, tasks, plans,
// crons). It is stored parallel to the synthetic Entry.Content blocks.
type metaEntryTarget struct {
	kind        metaTargetKind
	fileName    string // memory note filename (memory-file drill target)
	messageUUID string // originating turn to jump to (empty = no jump)
	entryIndex  int    // origin entry index in the parent transcript (fallback locator)
	blockIdx    int    // block within that turn to focus (-1 = none)
	taskID      string // task ID (task targets)
}

type metaTargetKind int

const (
	metaTargetNone      metaTargetKind = iota
	metaTargetMemoryFile                // file-list row → Enter drills into the file
	metaTargetDecision                  // flow-summary decision → jump to origin turn
	metaTargetTask                      // task row → open task view / definition turn
	metaTargetPlan                      // plan row → jump to ExitPlanMode turn
	metaTargetCron                      // cron row (informational; jump to definition turn)
)

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

func cycleInspectorScope(scope session.Scope) session.Scope {
	switch scope {
	case session.ScopeNode:
		return session.ScopeSubtree
	case session.ScopeSubtree:
		return session.ScopeSession
	default:
		return session.ScopeNode
	}
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
	tabs := a.inspectorTabs(item, nodeID)
	a.conv.inspector.Tab = validInspectorTab(a.conv.inspector.Tab, tabs)
	return node, true
}

func (a *App) openInspector(tab inspectorTab, scope session.Scope, zoom bool) {
	sp := &a.conv.split
	a.conv.inspector.Scope = scope
	a.conv.inspector.Tab = tab
	a.conv.inspector.ReturnToID = ""
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
	if !zoom {
		if id := a.conv.inspector.ReturnToID; id != "" {
			a.conv.inspector.ReturnToID = ""
			if id != a.selectedConversationItemID() && a.restoreConvSelection(id) {
				sp.CacheKey = ""
				a.updateConvPreview()
			}
		}
	}
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
	a.conv.inspector.Scope = cycleInspectorScope(a.conv.inspector.Scope)
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
	for _, scope := range []session.Scope{session.ScopeNode, session.ScopeSubtree, session.ScopeSession} {
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
		fmt.Fprintf(&b, "%d. %s %s", i+1, data.ToolName, art.Key)
		if summary != "" {
			fmt.Fprintf(&b, " · %s", summary)
		}
		b.WriteByte('\n')
		fmt.Fprintf(&b, "   origin: %s\n", inspectorArtifactOrigin(art.Origin))
	}
	return b.String()
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
