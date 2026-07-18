package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

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
		targets = append(targets, metaEntryTarget{blockIdx: -1})
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
		out = append(out, metaEntry{
			block:  session.ContentBlock{Type: "text", Text: memoryListRow(note, hist[note.FileName])},
			target: metaEntryTarget{kind: metaTargetMemoryFile, fileName: note.FileName},
		})
	}
	return out
}

// metaMemoryDrillEntries renders a single memory note in detail: a header/back
// row plus the note body. J on any row jumps to the note's last write turn.
func (a *App) metaMemoryDrillEntries(note session.MemoryNote) []metaEntry {
	hist := a.conv.flow.MemoryTouchHistory()[note.FileName]
	uuid, blockIdx := a.lastMemoryWriteOrigin(note.FileName)
	target := metaEntryTarget{kind: metaTargetMemoryFile, fileName: note.FileName, messageUUID: uuid, blockIdx: blockIdx}

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
	if uuid != "" {
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

// lastMemoryWriteOrigin returns the message UUID and block index of the most
// recent Edit/Write to the given memory note basename, for J-jump. Empty UUID
// when the flow index has no such write (e.g. file present but never written
// this session).
func (a *App) lastMemoryWriteOrigin(fileName string) (string, int) {
	if a.conv.flow == nil {
		return "", -1
	}
	uuid := ""
	blockIdx := -1
	var latest time.Time
	for _, art := range a.conv.flow.Artifacts(a.conv.flow.RootID, session.ArtifactChange, session.ScopeSession) {
		if filepath.Base(art.Key) != fileName {
			continue
		}
		if uuid == "" || !art.Origin.Timestamp.Before(latest) {
			uuid = art.Origin.MessageUUID
			blockIdx = art.Origin.BlockIndex
			latest = art.Origin.Timestamp
		}
	}
	return uuid, blockIdx
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

// textMeta wraps a rendered string as a non-selectable-target metaEntry.
func textMeta(text string) metaEntry {
	return metaEntry{block: session.ContentBlock{Type: "text", Text: text}, target: metaEntryTarget{blockIdx: -1}}
}

// metaSummaryEntries and metaTasksPlanEntries are filled in a later step; for
// now they render the existing text content as a single non-selectable block so
// only the memory row gains selectable behavior first.
func (a *App) metaSummaryEntries() []metaEntry {
	return []metaEntry{textMeta(strings.TrimRight(a.renderFlowSummary(), "\n"))}
}

func (a *App) metaTasksPlanEntries() []metaEntry {
	return []metaEntry{textMeta(strings.TrimRight(a.buildTasksPlanContent(a.conv.sess), "\n"))}
}
