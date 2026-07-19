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
	// Todos come first as read-only rows (no drill target). They are session
	// state independent of the memory directory, so render them even when no
	// project path / memory dir is available.
	if len(sess.Todos) > 0 {
		out = append(out, textMeta(a.renderTodosBlock(sess.Todos)))
	}

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
		if filepath.Base(art.Key) != fileName {
			continue
		}
		if !found || !art.Origin.Timestamp.Before(best.Timestamp) {
			best = art.Origin
			found = true
		}
	}
	return best, found
}

// renderTodosBlock renders the session todo list as one text block.
func (a *App) renderTodosBlock(todos []session.TodoItem) string {
	completed := 0
	for _, t := range todos {
		if t.Status == "completed" {
			completed++
		}
	}
	var b strings.Builder
	b.WriteString(dimStyle.Render(fmt.Sprintf("── Todos [%d/%d] ──", completed, len(todos))))
	for _, t := range todos {
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
		b.WriteString("\n" + style.Render(fmt.Sprintf("  %s %s", icon, t.Content)))
	}
	return b.String()
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
	if a.conv.split.Folds != nil {
		a.conv.split.Folds.BlockCursor = 0
	}
	a.updateConvPreview()
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

// jumpToMetaTarget jumps to the conversation turn that produced a meta entry.
// Origins recorded on flow artifacts point at a specific transcript entry, which
// mergeConversationTurns may have folded into a multi-entry turn — so we resolve
// the turn by UUID first and fall back to the entry-index range (matching
// mergedIndexForOrigin), not a bare UUID equality on the merged turn head.
func (a *App) jumpToMetaTarget(target metaEntryTarget) (tea.Model, tea.Cmd, bool) {
	// Only rows bound to a real origin jump. Header/todo/separator rows carry
	// metaTargetNone and must NOT fall through to a bare entry-index of 0, which
	// would jump to the first turn.
	if !target.kind.jumpable() {
		return a, nil, false
	}
	if target.messageUUID == "" && target.entryIndex < 0 {
		return a, nil, false
	}
	idx := mergedIndexForOrigin(a.conv.merged, target.messageUUID, target.entryIndex)
	if idx < 0 {
		a.copiedMsg = "origin turn not found"
		return a, nil, true
	}
	// Jump at turn granularity: origin.BlockIndex is relative to the source
	// entry, which does not line up with the merged turn's concatenated blocks.
	model, cmd := a.openConversationInspectorForEntry(a.conv.merged[idx], -1)
	return model, cmd, true
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

// originTarget builds a jump target from an artifact origin. The entry-index
// fallback is only meaningful for origins in the root transcript (entry indices
// are transcript-local), so it is dropped for agent-owned origins — those still
// jump by UUID when the turn is visible.
func (a *App) originTarget(kind metaTargetKind, origin session.ArtifactOrigin) metaEntryTarget {
	entryIndex := -1
	if origin.Transcript == "" || origin.Transcript == a.conv.sess.FilePath {
		entryIndex = origin.EntryIndex
	}
	return metaEntryTarget{
		kind:        kind,
		messageUUID: origin.MessageUUID,
		entryIndex:  entryIndex,
		blockIdx:    origin.BlockIndex,
	}
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
// Tasks jump to their definition turn (when known), plans to the ExitPlanMode
// turn, crons to their create turn.
func (a *App) metaTasksPlanEntries() []metaEntry {
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
		out = append(out, textMeta(dimStyle.Render(fmt.Sprintf("── Plans [%d] · ↵/J jump ──", len(plans)))))
		out = append(out, plans...)
	}

	if len(out) == 0 {
		out = append(out, textMeta(dimStyle.Render("No tasks, cron jobs, or plans.")))
	}
	return out
}

// planEntries builds one selectable row per ExitPlanMode plan occurrence, in
// chronological order, jumping to the turn that wrote it.
func (a *App) planEntries() []metaEntry {
	if a.conv.flow == nil {
		return nil
	}
	arts := a.conv.flow.Artifacts(a.conv.flow.RootID, session.ArtifactPlan, session.ScopeSession)
	hist := a.conv.flow.PlanTouchHistory()
	seen := make(map[string]bool)
	var out []metaEntry
	for _, art := range arts {
		if seen[art.Key] {
			continue
		}
		seen[art.Key] = true
		data, _ := art.Data.(session.PlanData)
		out = append(out, metaEntry{
			block:  session.ContentBlock{Type: "text", Text: planRow(art.Key, data, hist[art.Key])},
			target: a.originTarget(metaTargetPlan, art.Origin),
		})
	}
	return out
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
		if !ok || data.TaskID == "" {
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
