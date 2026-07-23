package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
)

// metaEntry pairs one synthetic content block (a selectable inspector row) with
// the jump/drill target bound to it. buildMetaEntry* helpers return a slice of
// these; the caller flattens them into a session.Entry plus a parallel
// MetaTargets slice.
type metaEntry struct {
	block  session.ContentBlock
	target metaEntryTarget
}

// buildSessionMetaEntry assembles the selectable synthetic Entry for a
// session-meta inspector row (summary/memory/tasksplan), returning the entry
// and the parallel MetaTargets slice. The zeroth block is always a
// non-selectable header so the block cursor lands on real items first.
func (a *App) buildSessionMetaEntry(item convItem) (session.Entry, []metaEntryTarget) {
	var entries []metaEntry
	switch item.sessionMeta {
	case "memory":
		entries = a.metaMemoryEntries()
	case "tasksplan":
		entries = a.metaTasksPlanEntries()
	case "refs":
		entries = a.metaRefsEntries()
	default:
		entries = a.metaSummaryEntries()
	}

	blocks := make([]session.ContentBlock, 0, len(entries))
	targets := make([]metaEntryTarget, 0, len(entries))
	for _, e := range entries {
		blocks = append(blocks, e.block)
		targets = append(targets, e.target)
	}
	if len(blocks) == 0 {
		blocks = append(blocks, session.ContentBlock{Type: "text", Text: dimStyle.Render("(nothing to show)")})
		targets = append(targets, metaEntryTarget{entryIndex: -1, blockIdx: -1})
	}
	return session.Entry{Content: blocks}, targets
}

// metaMemoryEntries builds the memory rows. In list mode (MetaDrill empty) each
// memory note is one selectable row showing name, type, description, and its
// first→last write history. In drill mode it renders the single note's body.
func (a *App) metaMemoryEntries() []metaEntry {
	sess := a.conv.sess

	var out []metaEntry
	// Todos are independent selectable resources. When the transcript contains
	// TodoWrite occurrences, each row points back to its latest exact origin.
	out = append(out, a.metaTodoEntries(sess.Todos)...)

	if sess.ProjectPath == "" {
		if len(out) == 0 {
			out = append(out, textMeta(dimStyle.Render("(no project path)")))
		}
		return out
	}
	notes := session.LoadMemoryNotes(sess.ProjectPath, homeDir())

	// Drill mode: render just the selected note's body with a back hint.
	if a.conv.inspector.MetaDrill != "" {
		for _, note := range notes {
			if note.FileName == a.conv.inspector.MetaDrill {
				return a.metaMemoryDrillEntries(note)
			}
		}
		// Drilled file vanished (deleted between renders) — fall back to list.
		a.conv.inspector.MetaDrill = ""
	}

	hist := a.conv.flow.MemoryTouchHistory()

	if len(notes) == 0 {
		if len(out) == 0 {
			out = append(out, textMeta(dimStyle.Render("No memory notes.")))
		}
		return out
	}

	header := dimStyle.Render(fmt.Sprintf("══ Memory · %d file(s) · ↵ open ══", len(notes)))
	out = append(out, textMeta(header))
	for _, note := range notes {
		// Enter drills into the file; J jumps to its last write turn, so bind the
		// write origin here too (entryIndex defaults to -1 when never written).
		target := metaEntryTarget{kind: metaTargetMemoryFile, fileName: note.FileName, entryIndex: -1, blockIdx: -1}
		if origin, ok := a.lastMemoryWriteOrigin(note.FileName); ok {
			t := a.originTarget(metaTargetMemoryFile, origin)
			t.fileName = note.FileName
			target = t
		}
		out = append(out, metaEntry{
			block:  session.ContentBlock{Type: "text", Text: memoryListRow(note, hist[note.FileName])},
			target: target,
		})
	}
	return out
}

// metaMemoryDrillEntries renders a single memory note in detail: a header/back
// row plus the note body. J on any row jumps to the note's last write turn.
func (a *App) metaMemoryDrillEntries(note session.MemoryNote) []metaEntry {
	hist := a.conv.flow.MemoryTouchHistory()[note.FileName]
	origin, hasOrigin := a.lastMemoryWriteOrigin(note.FileName)
	target := metaEntryTarget{kind: metaTargetMemoryFile, fileName: note.FileName, entryIndex: -1, blockIdx: -1}
	if hasOrigin {
		t := a.originTarget(metaTargetMemoryFile, origin)
		t.fileName = note.FileName
		target = t
	}

	var head strings.Builder
	title := note.Name
	if note.IsIndex {
		title = "MEMORY.md (index)"
	}
	head.WriteString(dimStyle.Render("← esc: back") + "  " + memTypeStyle(note.Type).Render(title))
	if note.Type != "" && !note.IsIndex {
		head.WriteString("  " + memTypeStyle(note.Type).Render("["+note.Type+"]"))
	}
	head.WriteString("\n")
	head.WriteString(dimStyle.Render(memoryHistoryLine(hist)))
	if hasOrigin {
		head.WriteString(dimStyle.Render("  · J: jump to last write"))
	}

	previewW := max(a.conv.split.PreviewWidth(a.width, a.splitRatio)-4, 20)
	body := renderMarkdownText(note.Body, previewW)

	return []metaEntry{
		{block: session.ContentBlock{Type: "text", Text: head.String()}, target: target},
		{block: session.ContentBlock{Type: "text", Text: strings.TrimRight(body, "\n")}, target: target},
	}
}

// memoryListRow formats one memory note as a single selectable row: name, type
// tag, one-line description, and its write-history window.
func memoryListRow(note session.MemoryNote, hist session.TouchHistory) string {
	var b strings.Builder
	title := note.Name
	if note.IsIndex {
		title = "MEMORY.md"
	}
	b.WriteString(memTypeStyle(note.Type).Render(title))
	if note.Type != "" && !note.IsIndex {
		b.WriteString("  " + memTypeStyle(note.Type).Render("["+note.Type+"]"))
	}
	if h := memoryHistoryLine(hist); h != "" {
		b.WriteString(dimStyle.Render("  " + h))
	}
	if note.Description != "" {
		b.WriteString("\n" + dimStyle.Render("  "+note.Description))
	}
	return b.String()
}

// memoryHistoryLine renders "first MM-DD HH:MM → last MM-DD HH:MM (×N)" from a
// touch history, or "" when there is no recorded write (e.g. imported file).
func memoryHistoryLine(h session.TouchHistory) string {
	if h.Count == 0 || h.First.IsZero() {
		return ""
	}
	const layout = "01-02 15:04"
	if h.First.Equal(h.Last) {
		return h.First.Format(layout)
	}
	return fmt.Sprintf("%s → %s (×%d)", h.First.Format(layout), h.Last.Format(layout), h.Count)
}

// lastMemoryWriteOrigin returns the origin of the most recent Edit/Write to the
// given memory note basename, for J-jump. ok=false when the flow index has no
// such write (e.g. file present but never written this session).
func (a *App) lastMemoryWriteOrigin(fileName string) (session.ArtifactOrigin, bool) {
	if a.conv.flow == nil {
		return session.ArtifactOrigin{}, false
	}
	var best session.ArtifactOrigin
	found := false
	for _, art := range a.conv.flow.Artifacts(a.conv.flow.RootID, session.ArtifactChange, session.ScopeSession) {
		if filepath.Base(art.Key) != fileName || !a.originVisibleInExecutionContext(art.Origin) {
			continue
		}
		if !found || !art.Origin.Timestamp.Before(best.Timestamp) {
			best = art.Origin
			found = true
		}
	}
	return best, found
}

// metaTodoEntries renders each todo as an independently selectable resource.
// The latest matching TodoWrite occurrence supplies its cross-context origin.
func (a *App) metaTodoEntries(todos []session.TodoItem) []metaEntry {
	if len(todos) == 0 {
		return nil
	}
	completed := 0
	for _, todo := range todos {
		if todo.Status == "completed" {
			completed++
		}
	}
	out := []metaEntry{textMeta(dimStyle.Render(fmt.Sprintf("── Todos [%d/%d] · ↵/J jump ──", completed, len(todos))))}
	origins := a.latestTodoOrigins()
	for _, todo := range todos {
		target := metaEntryTarget{kind: metaTargetTodo, entryIndex: -1, blockIdx: -1}
		if origin, ok := origins[todo.Content]; ok {
			target = a.originTarget(metaTargetTodo, origin)
		}
		out = append(out, metaEntry{
			block:  session.ContentBlock{Type: "text", Text: todoRow(todo)},
			target: target,
		})
	}
	return out
}

func (a *App) latestTodoOrigins() map[string]session.ArtifactOrigin {
	out := make(map[string]session.ArtifactOrigin)
	if a.conv.flow == nil {
		return out
	}
	for _, artifact := range a.conv.flow.Artifacts(a.conv.flow.RootID, session.ArtifactTodo, session.ScopeSession) {
		if !a.originVisibleInExecutionContext(artifact.Origin) {
			continue
		}
		current, exists := out[artifact.Key]
		if !exists || !artifact.Origin.Timestamp.Before(current.Timestamp) {
			out[artifact.Key] = artifact.Origin
		}
	}
	return out
}

// originVisibility loads and filters a transcript once per conversation refresh.
// Resource previews call provenance checks for every selectable row, so reading
// the same JSONL file here on each cursor move makes pinned navigation unusable.
func (a *App) originVisibility(context executionContext) (executionOriginVisibility, bool) {
	key := context.Key
	if cached, ok := a.conv.execution.OriginVisibility[key]; ok {
		return cached, true
	}
	raw, err := session.LoadMessages(key)
	if err != nil {
		return executionOriginVisibility{}, false
	}
	visible := raw
	if context.Agent.ID != "" {
		visible = filterAgentContextEntries(visible)
		if context.Agent.AgentType == "aside_question" {
			visible = filterSideQuestionContext(visible)
		}
	}
	visibleUUIDs := make(map[string]bool, len(visible))
	for _, entry := range visible {
		if entry.UUID != "" {
			visibleUUIDs[entry.UUID] = true
		}
	}
	cached := executionOriginVisibility{Raw: raw, Visible: visible, VisibleUUIDs: visibleUUIDs}
	if a.conv.execution.OriginVisibility == nil {
		a.conv.execution.OriginVisibility = make(map[string]executionOriginVisibility)
	}
	a.conv.execution.OriginVisibility[key] = cached
	return cached, true
}

// originVisibleInExecutionContext rejects artifacts from inherited transcript
// history that the corresponding execution context intentionally hides.
func (a *App) originVisibleInExecutionContext(origin session.ArtifactOrigin) bool {
	context, ok := a.executionContextForTranscript(origin.Transcript)
	if !ok {
		return false
	}
	visibility, ok := a.originVisibility(context)
	if !ok {
		return false
	}

	var source session.Entry
	foundSource := false
	if origin.EntryIndex >= 0 && origin.EntryIndex < len(visibility.Raw) {
		source = visibility.Raw[origin.EntryIndex]
		foundSource = true
	} else if origin.MessageUUID != "" {
		for _, entry := range visibility.Raw {
			if entry.UUID == origin.MessageUUID {
				source = entry
				foundSource = true
				break
			}
		}
	}
	if !foundSource {
		return false
	}
	if source.UUID != "" {
		return visibility.VisibleUUIDs[source.UUID]
	}
	for _, entry := range visibility.Visible {
		if entry.Role == source.Role && entry.Timestamp.Equal(source.Timestamp) {
			if origin.BlockIndex < 0 {
				return true
			}
			if origin.BlockIndex < len(source.Content) && origin.BlockIndex < len(entry.Content) &&
				sameConversationBlock(entry.Content[origin.BlockIndex], source.Content[origin.BlockIndex]) {
				return true
			}
		}
	}
	return false
}

func todoRow(todo session.TodoItem) string {
	icon := iconIdle
	style := dimStyle
	switch todo.Status {
	case "completed":
		icon = iconDone
		style = lipgloss.NewStyle().Foreground(colorAccent)
	case "in_progress":
		icon = iconActive
		style = lipgloss.NewStyle().Foreground(colorAssistant)
	}
	return style.Render(fmt.Sprintf("%s %s", icon, todo.Content))
}

// currentMetaTarget returns the jump/drill target bound to the focused block of
// a session-meta preview, or (zero, false) when there is none (non-meta preview,
// header row, or out-of-range cursor).
func (a *App) currentMetaTarget() (metaEntryTarget, bool) {
	sp := &a.conv.split
	if sp.Folds == nil {
		return metaEntryTarget{}, false
	}
	targets := a.conv.inspector.MetaTargets
	bc := sp.Folds.BlockCursor
	if bc < 0 || bc >= len(targets) {
		return metaEntryTarget{}, false
	}
	return targets[bc], true
}

// handleMetaEntryEnter acts on Enter over a focused session-meta block: memory
// rows drill into the file, other targets jump to the originating turn. Returns
// handled=false when the block has no actionable target so the caller falls
// back to the default zoom behavior.
func (a *App) handleMetaEntryEnter() (bool, tea.Model, tea.Cmd) {
	target, ok := a.currentMetaTarget()
	if !ok {
		return false, a, nil
	}
	switch target.kind {
	case metaTargetMemoryFile:
		if a.conv.inspector.MetaDrill == "" && target.fileName != "" {
			// List → detail: drill into the selected memory note.
			a.enterMemoryDrill(target.fileName)
			return true, a, nil
		}
		// Already in detail (or no file): fall through to a jump if we have one.
		if m, cmd, ok := a.jumpToMetaTarget(target); ok {
			return true, m, cmd
		}
		a.copiedMsg = "No origin turn for this memory"
		return true, a, nil
	case metaTargetTodo:
		if m, cmd, ok := a.jumpToMetaTarget(target); ok {
			return true, m, cmd
		}
		a.copiedMsg = "No origin turn for this todo"
		return true, a, nil
	case metaTargetTask:
		// Prefer opening the task's own view (matches decision-task Enter); fall
		// back to jumping to its definition turn.
		if task, ok := a.taskByID(target.taskID); ok {
			if _, _, visible := a.taskConversationData(task); visible {
				m, cmd := a.drillIntoTaskConversation(task)
				return true, m, cmd
			}
		}
		if m, cmd, ok := a.jumpToMetaTarget(target); ok {
			return true, m, cmd
		}
		a.copiedMsg = "No conversation or origin turn for this task"
		return true, a, nil
	case metaTargetPlan:
		if a.conv.inspector.MetaPlanDrill == "" && target.planKey != "" {
			a.enterPlanDrill(target.planKey)
			return true, a, nil
		}
		return true, a, nil
	case metaTargetRef:
		if target.url == "" {
			a.copiedMsg = "No URL for this reference"
			return true, a, nil
		}
		if err := a.openInBrowser(target.url); err != nil {
			a.copiedMsg = "Open failed: " + err.Error()
			return true, a, nil
		}
		a.copiedMsg = "Opened " + target.url
		return true, a, nil
	default:
		if m, cmd, ok := a.jumpToMetaTarget(target); ok {
			return true, m, cmd
		}
	}
	return false, a, nil
}

// taskByID finds a session task by its ID.
func (a *App) taskByID(id string) (session.TaskItem, bool) {
	if id == "" {
		return session.TaskItem{}, false
	}
	for _, t := range a.conv.sess.Tasks {
		if t.ID == id {
			return t, true
		}
	}
	return session.TaskItem{}, false
}

// enterMemoryDrill switches the memory row from list mode to single-file detail
// for fileName and re-renders, keeping the inspector zoomed.
func (a *App) enterMemoryDrill(fileName string) {
	a.conv.inspector.MetaDrill = fileName
	a.conv.split.CacheKey = ""
	a.updateConvPreview()
	a.focusFirstActionableMetaTarget()
}

// exitMemoryDrill returns from single-file detail to the memory file list.
// Returns true when it consumed the key (was in drill mode).
func (a *App) exitMemoryDrill() bool {
	if a.conv.inspector.MetaDrill == "" {
		return false
	}
	a.conv.inspector.MetaDrill = ""
	a.conv.split.CacheKey = ""
	if a.conv.split.Folds != nil {
		a.conv.split.Folds.BlockCursor = 0
	}
	a.updateConvPreview()
	return true
}

func (a *App) enterPlanDrill(planKey string) {
	a.conv.inspector.MetaPlanDrill = planKey
	a.conv.split.CacheKey = ""
	a.updateConvPreview()
	a.focusFirstActionableMetaTarget()
}

func (a *App) focusFirstActionableMetaTarget() {
	sp := &a.conv.split
	if sp.Folds == nil {
		return
	}
	for i, target := range a.conv.inspector.MetaTargets {
		if target.kind == metaTargetNone {
			continue
		}
		sp.Folds.BlockCursor = i
		sp.RefreshFoldCursor(a.width, a.splitRatio)
		sp.ScrollToBlock()
		return
	}
}

func (a *App) exitPlanDrill() bool {
	if a.conv.inspector.MetaPlanDrill == "" {
		return false
	}
	a.conv.inspector.MetaPlanDrill = ""
	a.conv.split.CacheKey = ""
	if a.conv.split.Folds != nil {
		a.conv.split.Folds.BlockCursor = 0
	}
	a.updateConvPreview()
	return true
}

// jumpToMetaTarget opens the exact transcript turn and source block that
// produced a resource. Cross-context jumps switch the execution rail first;
// the history frame retains the source context so Esc returns to the resource.
func (a *App) jumpToMetaTarget(target metaEntryTarget) (tea.Model, tea.Cmd, bool) {
	if !target.kind.jumpable() {
		return a, nil, false
	}
	if target.messageUUID == "" && target.entryIndex < 0 {
		return a, nil, false
	}

	returnFrame := a.captureInspectorNavFrame()
	transcript := target.transcript
	if transcript == "" {
		transcript = a.conv.sess.FilePath
	}
	switchedContext := executionContextKey(transcript) != a.conv.execution.ActiveKey
	if switchedContext {
		context, ok := a.executionContextForTranscript(transcript)
		if !ok || !a.activateExecutionContext(context.Key, true) {
			a.copiedMsg = "origin transcript unavailable"
			return a, nil, true
		}
	}
	rollback := func(message string) (tea.Model, tea.Cmd, bool) {
		if switchedContext {
			a.restoreInspectorFrame(returnFrame)
		}
		a.copiedMsg = message
		return a, nil, true
	}

	sourceEntry, sourceUUID := a.resolveVisibleOrigin(transcript, target)
	idx := mergedIndexForOrigin(a.conv.merged, sourceUUID, sourceEntry)
	if idx < 0 {
		return rollback("origin turn not found")
	}
	visible := false
	for _, raw := range a.convList.VisibleItems() {
		item, ok := raw.(convItem)
		if ok && item.kind == convMsg && item.merged.startIdx == a.conv.merged[idx].startIdx {
			visible = true
			break
		}
	}
	if !visible {
		return rollback("origin turn is hidden by filter")
	}

	blockIdx := -1
	if sourceEntry >= a.conv.merged[idx].startIdx && sourceEntry <= a.conv.merged[idx].endIdx && target.blockIdx >= 0 {
		blockIdx = target.blockIdx
		for i := a.conv.merged[idx].startIdx; i < sourceEntry && i < len(a.conv.messages); i++ {
			blockIdx += len(a.conv.messages[i].Content)
		}
		if blockIdx >= len(a.conv.merged[idx].entry.Content) {
			blockIdx = -1
		}
	}

	a.conv.inspector.History = append(a.conv.inspector.History, returnFrame)
	a.conv.inspector.ReturnToID = returnFrame.location.ItemID
	model, cmd := a.openConversationInspectorForEntryWithHistory(a.conv.merged[idx], blockIdx, false)
	return model, cmd, true
}

// resolveVisibleOrigin maps transcript-local raw coordinates onto the entries
// currently visible after agent context filtering. UUID is preferred; legacy
// UUID-less entries fall back to timestamp, role, and the exact source block.
func (a *App) resolveVisibleOrigin(transcript string, target metaEntryTarget) (int, string) {
	findVisible := func(entry session.Entry, uuid string) int {
		for i, visible := range a.conv.messages {
			if uuid != "" && visible.UUID == uuid {
				return i
			}
			if uuid == "" && visible.Role == entry.Role && visible.Timestamp.Equal(entry.Timestamp) {
				if target.blockIdx < 0 {
					return i
				}
				if target.blockIdx < len(entry.Content) && target.blockIdx < len(visible.Content) &&
					sameConversationBlock(visible.Content[target.blockIdx], entry.Content[target.blockIdx]) {
					return i
				}
			}
		}
		return -1
	}

	if target.messageUUID != "" {
		if i := findVisible(session.Entry{}, target.messageUUID); i >= 0 {
			return i, target.messageUUID
		}
	}
	raw, err := session.LoadMessages(transcript)
	if err == nil && target.entryIndex >= 0 && target.entryIndex < len(raw) {
		entry := raw[target.entryIndex]
		// The target UUID may be absent or stale in legacy provenance. The raw
		// entry's UUID is authoritative for mapping into the filtered view.
		if i := findVisible(entry, entry.UUID); i >= 0 {
			return i, entry.UUID
		}
		return -1, entry.UUID
	}
	return -1, target.messageUUID
}

// mergedByUUID finds the merged conversation turn whose head entry UUID matches.
// Prefer jumpToMetaTarget for origin jumps (it also handles the entry-index
// fallback); this remains for callers that only have a head UUID.
func (a *App) mergedByUUID(uuid string) (mergedMsg, bool) {
	if uuid == "" {
		return mergedMsg{}, false
	}
	if idx := mergedIndexForOrigin(a.conv.merged, uuid, -1); idx >= 0 {
		return a.conv.merged[idx], true
	}
	return mergedMsg{}, false
}

// metaSummaryEntries renders the Session Flow summary as a header block plus one
// selectable row per decision marker. Each decision row jumps (Enter/J) to the
// turn that produced it.
func (a *App) metaSummaryEntries() []metaEntry {
	flow := a.conv.flow
	if flow == nil {
		return []metaEntry{textMeta(strings.TrimRight(a.renderFlowSummary(), "\n"))}
	}

	out := []metaEntry{textMeta(strings.TrimRight(a.flowSummaryHeader(), "\n"))}

	decisions := flow.Decisions(session.ScopeSession)
	visibleDecisions := decisions[:0]
	for _, decision := range decisions {
		if a.originVisibleInExecutionContext(decision.Origin) {
			visibleDecisions = append(visibleDecisions, decision)
		}
	}
	decisions = visibleDecisions
	if len(decisions) == 0 {
		out = append(out, textMeta(dimStyle.Render("No decisions recorded.")))
		return out
	}
	out = append(out, textMeta(dimStyle.Render(fmt.Sprintf("── Decisions [%d] · ↵/J jump ──", len(decisions)))))
	for _, d := range decisions {
		dd, _ := d.Data.(session.DecisionData)
		out = append(out, metaEntry{
			block:  session.ContentBlock{Type: "text", Text: decisionRow(dd)},
			target: a.originTarget(metaTargetDecision, d.Origin),
		})
	}
	return out
}

// originTarget preserves transcript-local coordinates so resource jumps can
// cross between main and subagent execution contexts without losing precision.
func (a *App) originTarget(kind metaTargetKind, origin session.ArtifactOrigin) metaEntryTarget {
	target := metaEntryTarget{kind: kind, entryIndex: -1, blockIdx: -1}
	if !a.originVisibleInExecutionContext(origin) {
		return target
	}
	target.transcript = origin.Transcript
	target.messageUUID = origin.MessageUUID
	target.entryIndex = origin.EntryIndex
	target.blockIdx = origin.BlockIndex
	return target
}

// flowSummaryHeader is the non-decision preamble of the flow summary (counts,
// tokens, artifact badges) — the decisions list is rendered as selectable rows
// separately so it is intentionally omitted here.
func (a *App) flowSummaryHeader() string {
	full := a.renderFlowSummary()
	if idx := strings.Index(full, "\n## Decisions"); idx >= 0 {
		return full[:idx]
	}
	return full
}

// decisionRow formats one decision marker as a selectable row.
func decisionRow(dd session.DecisionData) string {
	label := dd.Label
	if label == "" {
		label = string(dd.Kind)
	}
	return dimStyle.Render("▣ ") + label
}

// metaTasksPlanEntries renders tasks, cron jobs, and plans as selectable rows.
// Plan detail is an inspector-local drill view; J still jumps to its latest
// ExitPlanMode turn.
func (a *App) metaTasksPlanEntries() []metaEntry {
	if a.conv.inspector.MetaPlanDrill != "" {
		if art, ok := a.latestPlanArtifact(a.conv.inspector.MetaPlanDrill); ok {
			return a.metaPlanDrillEntries(art)
		}
		a.conv.inspector.MetaPlanDrill = ""
	}

	sess := a.conv.sess
	var out []metaEntry

	taskOrigins := a.taskOriginByID()
	if len(sess.Tasks) > 0 {
		completed := 0
		for _, t := range sess.Tasks {
			if t.Status == "completed" {
				completed++
			}
		}
		out = append(out, textMeta(dimStyle.Render(fmt.Sprintf("── Tasks [%d/%d] · ↵/J jump ──", completed, len(sess.Tasks)))))
		for _, task := range sess.Tasks {
			target := metaEntryTarget{kind: metaTargetTask, taskID: task.ID, entryIndex: -1, blockIdx: -1}
			if origin, ok := taskOrigins[task.ID]; ok {
				target = a.originTarget(metaTargetTask, origin)
				target.taskID = task.ID
			}
			out = append(out, metaEntry{
				block:  session.ContentBlock{Type: "text", Text: taskRow(task)},
				target: target,
			})
		}
	}

	if len(sess.Crons) > 0 {
		out = append(out, textMeta(dimStyle.Render(fmt.Sprintf("── Cron Jobs [%d] ──", len(sess.Crons)))))
		for _, cron := range sess.Crons {
			out = append(out, metaEntry{
				block:  session.ContentBlock{Type: "text", Text: cronRow(cron)},
				target: metaEntryTarget{kind: metaTargetCron, entryIndex: -1, blockIdx: -1},
			})
		}
	}

	if plans := a.planEntries(); len(plans) > 0 {
		out = append(out, textMeta(dimStyle.Render(fmt.Sprintf("── Plans [%d] · ↵ open · J jump ──", len(plans)))))
		out = append(out, plans...)
	}

	if len(out) == 0 {
		out = append(out, textMeta(dimStyle.Render("No tasks, cron jobs, or plans.")))
	}
	return out
}

// planEntries builds one selectable row per plan file. The first occurrence
// establishes list order; the latest occurrence supplies its data and J target.
func (a *App) planEntries() []metaEntry {
	if a.conv.flow == nil {
		return nil
	}
	arts := a.conv.flow.Artifacts(a.conv.flow.RootID, session.ArtifactPlan, session.ScopeSession)
	hist := a.conv.flow.PlanTouchHistory()
	order := make([]string, 0, len(arts))
	latest := make(map[string]session.Artifact, len(arts))
	for _, art := range arts {
		if !a.originVisibleInExecutionContext(art.Origin) {
			continue
		}
		if _, seen := latest[art.Key]; !seen {
			order = append(order, art.Key)
		}
		latest[art.Key] = art
	}
	out := make([]metaEntry, 0, len(order))
	for _, key := range order {
		art := latest[key]
		data, _ := art.Data.(session.PlanData)
		target := a.originTarget(metaTargetPlan, art.Origin)
		target.planKey = key
		out = append(out, metaEntry{
			block:  session.ContentBlock{Type: "text", Text: planRow(key, data, hist[key])},
			target: target,
		})
	}
	return out
}

func (a *App) latestPlanArtifact(key string) (session.Artifact, bool) {
	if a.conv.flow == nil || key == "" {
		return session.Artifact{}, false
	}
	arts := a.conv.flow.Artifacts(a.conv.flow.RootID, session.ArtifactPlan, session.ScopeSession)
	for i := len(arts) - 1; i >= 0; i-- {
		if arts[i].Key == key && a.originVisibleInExecutionContext(arts[i].Origin) {
			return arts[i], true
		}
	}
	return session.Artifact{}, false
}

func (a *App) metaPlanDrillEntries(art session.Artifact) []metaEntry {
	data, _ := art.Data.(session.PlanData)
	target := a.originTarget(metaTargetPlan, art.Origin)
	target.planKey = art.Key

	name := filepath.Base(art.Key)
	if data.PlanFilePath != "" {
		name = filepath.Base(data.PlanFilePath)
	}
	var head strings.Builder
	head.WriteString(dimStyle.Render("← esc: back") + "  " + planBadge.Render("▤ "+name))
	if h := memoryHistoryLine(a.conv.flow.PlanTouchHistory()[art.Key]); h != "" {
		head.WriteString(dimStyle.Render("  " + h))
	}
	if target.messageUUID != "" || target.entryIndex >= 0 {
		head.WriteString(dimStyle.Render("  · J: jump to latest write"))
	}
	if data.PlanFilePath != "" {
		head.WriteString("\n" + dimStyle.Render(data.PlanFilePath))
	}

	body := strings.TrimSpace(data.Plan)
	if body == "" {
		body = dimStyle.Render("(plan data unavailable)")
	} else {
		previewW := max(a.conv.split.PreviewWidth(a.width, a.splitRatio)-4, 20)
		body = strings.TrimRight(renderMarkdownText(body, previewW), "\n")
	}
	return []metaEntry{
		{block: session.ContentBlock{Type: "text", Text: head.String()}, target: target},
		{block: session.ContentBlock{Type: "text", Text: body}, target: target},
	}
}

// taskOriginByID maps each task ID to the origin of its most recent
// TaskCreate/TaskUpdate occurrence, so a task row can jump to its turn.
func (a *App) taskOriginByID() map[string]session.ArtifactOrigin {
	out := make(map[string]session.ArtifactOrigin)
	if a.conv.flow == nil {
		return out
	}
	for _, art := range a.conv.flow.Artifacts(a.conv.flow.RootID, session.ArtifactTask, session.ScopeSession) {
		data, ok := art.Data.(session.TaskEventData)
		if !ok || data.TaskID == "" || !a.originVisibleInExecutionContext(art.Origin) {
			continue
		}
		prev, seen := out[data.TaskID]
		if !seen || !art.Origin.Timestamp.Before(prev.Timestamp) {
			out[data.TaskID] = art.Origin
		}
	}
	return out
}

func taskRow(t session.TaskItem) string {
	icon := iconIdle
	style := dimStyle
	switch t.Status {
	case "completed":
		icon = iconDone
		style = lipgloss.NewStyle().Foreground(colorAccent)
	case "in_progress":
		icon = iconActive
		style = lipgloss.NewStyle().Foreground(colorAssistant)
	}
	subject := t.Subject
	if subject == "" {
		subject = "(untitled task)"
	}
	return style.Render(icon+" ") + subject
}

func cronRow(c session.CronItem) string {
	icon := iconActive
	style := lipgloss.NewStyle().Foreground(colorAssistant)
	suffix := ""
	if c.Status == "deleted" {
		icon = iconStopped
		style = dimStyle
		if !c.DeletedAt.IsZero() {
			suffix = dimStyle.Render("  deleted " + c.DeletedAt.Format("01-02 15:04"))
		}
	}
	headline := strings.TrimSpace(strings.Join([]string{c.ID, c.Cron}, "  "))
	if headline == "" {
		headline = "(cron)"
	}
	return style.Render(icon+" ") + headline + suffix
}

func planRow(key string, data session.PlanData, hist session.TouchHistory) string {
	name := key
	if data.PlanFilePath != "" {
		name = filepath.Base(data.PlanFilePath)
	} else {
		name = filepath.Base(key)
	}
	row := planBadge.Render("▤ ") + name
	if h := memoryHistoryLine(hist); h != "" {
		row += dimStyle.Render("  " + h)
	}
	return row
}

// textMeta wraps a rendered string as a non-selectable-target metaEntry.
func textMeta(text string) metaEntry {
	return metaEntry{block: session.ContentBlock{Type: "text", Text: text}, target: metaEntryTarget{entryIndex: -1, blockIdx: -1}}
}

// metaRefsEntries builds one selectable row per PR/Jira reference found in the
// session flow, deduped by URL. Enter on a row opens the URL in the browser
// (metaTargetRef); J jumps to the ref's first-seen turn when available.
func (a *App) metaRefsEntries() []metaEntry {
	if a.conv.flow == nil {
		return nil
	}
	arts := session.DedupeArtifactsByKey(
		a.conv.flow.Artifacts(a.conv.flow.RootID, session.ArtifactRef, session.ScopeSession))
	if len(arts) == 0 {
		return nil
	}
	out := []metaEntry{textMeta(dimStyle.Render("── Refs & URLs · ↵ open · J jump ──"))}
	for _, art := range arts {
		ref, ok := art.Data.(session.SessionRef)
		if !ok || ref.URL == "" {
			continue
		}
		// Decorate with already-resolved status from the process-wide cache so
		// PR/Jira refs show their state in the flow row without blocking. The
		// cache is populated by the refs preview's async resolve and by
		// openConversation's background resolve; if not cached yet, the row
		// renders the link only and fills in on a later tick once refStatusMsg
		// re-renders.
		if cached, ok := session.CachedRefStatus(ref.URL); ok {
			ref.State = cached.State
			ref.ReviewDecision = cached.ReviewDecision
			ref.ChecksState = cached.ChecksState
			ref.IsDraft = cached.IsDraft
			ref.JiraStatus = cached.JiraStatus
			ref.JiraStatusDone = cached.JiraStatusDone
			ref.Resolved = cached.Resolved
		}
		target := metaEntryTarget{kind: metaTargetRef, url: ref.URL, entryIndex: -1, blockIdx: -1}
		if art.Origin.MessageUUID != "" || art.Origin.EntryIndex >= 0 {
			target.transcript = art.Origin.Transcript
			target.messageUUID = art.Origin.MessageUUID
			target.entryIndex = art.Origin.EntryIndex
			target.blockIdx = art.Origin.BlockIndex
		}
		out = append(out, metaEntry{
			block:  session.ContentBlock{Type: "text", Text: refRow(ref)},
			target: target,
		})
	}
	return out
}

// buildRefsListText is a flat (non-selectable) fallback render of the session's
// refs, used when the inspector routes the refs meta row through the facet
// render path instead of the synthetic selectable-entry path.
func (a *App) buildRefsListText() string {
	if a.conv.flow == nil {
		return "# Refs & URLs\n\nNo flow index available.\n"
	}
	arts := session.DedupeArtifactsByKey(
		a.conv.flow.Artifacts(a.conv.flow.RootID, session.ArtifactRef, session.ScopeSession))
	if len(arts) == 0 {
		return "# Refs & URLs\n\nNo PR or Jira references in this session.\n"
	}
	var b strings.Builder
	b.WriteString("# Refs & URLs\n\n")
	for i, art := range arts {
		ref, ok := art.Data.(session.SessionRef)
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "%d. %s\n   %s\n", i+1, refRow(ref), ref.URL)
	}
	return b.String()
}

// refRow renders one PR/Jira/artifact reference as a single-line row: a kind
// tag, the label (or artifact title), and (when known) the resolved state.
func refRow(ref session.SessionRef) string {
	tag := "URL"
	style := dimStyle
	switch ref.Kind {
	case session.RefPR:
		tag = "PR"
		style = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	case session.RefJira:
		tag = "Jira"
		style = lipgloss.NewStyle().Foreground(colorUser).Bold(true)
	case session.RefArtifact:
		tag = "Artifact"
		style = lipgloss.NewStyle().Foreground(colorAssistant).Bold(true)
	}
	name := ref.Label
	if ref.Title != "" {
		name = ref.Title
	}
	row := style.Render("["+tag+"]") + " " + name
	if ref.State != "" {
		row += " " + dimStyle.Render("("+string(ref.State)+")")
	} else if ref.Kind == session.RefArtifact {
		row += " " + dimStyle.Render("(published)")
	}
	return row
}
