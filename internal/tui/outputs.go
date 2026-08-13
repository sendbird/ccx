package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
)

// The Outputs digest answers "what came out of this session?" — the durable
// results (PRs, Jira issues, published artifacts, plans, memory notes) followed
// by the working material (edited files, scratchpad). It is deliberately not a
// second conversation view: every row is a thing that outlived the session, and
// Enter takes you either to the thing itself (a URL) or back to the moment in
// the transcript that produced it.

// updateSessionOutputsPreview renders the Outputs digest for a session. Refs
// come from the session store (the async extract/resolve pipeline owns their
// status) and everything else is collected from the transcript plus disk.
//
// Collecting is off the UI thread: scanning a large transcript takes hundreds
// of milliseconds (a 100MB session measured ~790ms), and doing that inline
// would freeze navigation on every arrow key. The first visit renders a
// placeholder and dispatches outputsCollectedMsg; subsequent visits hit the
// per-session memo. Refs that have not been extracted yet get the same offline
// extract the References preview uses.
func (a *App) updateSessionOutputsPreview(sess session.Session) tea.Cmd {
	return a.buildOutputsPreview(sess, true)
}

// refreshOutputsPreviewLayout re-renders the digest at the current pane size
// WITHOUT dispatching any work. The render path (View) cannot dispatch a
// tea.Cmd, so calling the dispatching variant there would arm an in-flight
// latch whose command is then dropped — stranding the pane on "scanning
// transcript…" with nothing left to clear the latch. Resize only needs the
// layout recomputed from already-collected rows.
func (a *App) refreshOutputsPreviewLayout(sess session.Session) {
	_ = a.buildOutputsPreview(sess, false)
}

// buildOutputsPreview renders the digest. When dispatch is false it never arms
// a latch and never returns work; see refreshOutputsPreviewLayout.
func (a *App) buildOutputsPreview(sess session.Session, dispatch bool) tea.Cmd {
	// The list widget's copy is a snapshot from the last rebuild; the store is
	// the source of truth for lazily-resolved refs (see updateSessionRefsPreview).
	if fresh, ok := a.sessionByIDFromStore(sess.ID); ok {
		sess = fresh
	}

	previewW := max(a.width-a.sessSplit.ListWidth(a.width, a.splitRatio)-1, 1)
	contentH := max(a.height-3, 1)

	if a.sessOutputsCacheID != sess.ID {
		a.sessOutputsCursor = 0
		a.sessOutputsCacheID = sess.ID
		a.sessOutputs = nil
		a.sessOutputsCollected = ""
	}

	var cmds []tea.Cmd

	// Live sessions grow after the scan set HasRefs, so treat them as
	// possibly-having-refs and extract off the file (the refs-preview rationale).
	mayHaveRefs := sess.HasRefs || sess.IsLive
	if dispatch && len(sess.Refs) == 0 && mayHaveRefs && !sess.RefsResolved && !a.refsInFlight[sess.ID] {
		if cmd := a.extractSessionRefsCmd(sess.ID, sess.FilePath); cmd != nil {
			a.refsInFlight[sess.ID] = true
			cmds = append(cmds, cmd)
		}
	}

	// A transcript that grew invalidates the collection; the ref count is not
	// part of the key because refs are merged in at render time, not collected.
	dataKey := fmt.Sprintf("%s:%d", sess.ID, sess.ModTime.UnixNano())
	if dispatch && a.sessOutputsCollected != dataKey && !a.outputsInFlight[sess.ID] {
		a.outputsInFlight[sess.ID] = true
		cmds = append(cmds, collectOutputsCmd(sess, dataKey))
	}

	collecting := a.sessOutputsCollected != dataKey
	rows := a.mergeRefOutputs(a.sessOutputs, sess.Refs)
	if a.sessOutputsCursor >= len(rows) {
		a.sessOutputsCursor = 0
	}
	a.sessOutputsRows = rows

	renderKey := fmt.Sprintf("%s:%d:%d:%d:%t:%t", dataKey, len(rows), previewW, a.sessOutputsCursor, a.sessSplit.Focus, collecting)
	if a.sessOutputsCacheKey != renderKey {
		a.sessOutputsCache = a.renderOutputs(rows, previewW, collecting, mayHaveRefs && !sess.RefsResolved)
		a.sessOutputsCacheKey = renderKey
	}

	if a.sessSplit.Preview.Width != previewW || a.sessSplit.Preview.Height != contentH {
		a.sessSplit.Preview = viewport.New(previewW, contentH)
	}
	a.sessSplit.Preview.SetContent(a.sessOutputsCache)
	return tea.Batch(cmds...)
}

// outputsCollectedMsg carries the transcript/disk-derived outputs for a session
// (refs excluded — those arrive through the ref pipeline and are merged in at
// render time). dataKey stamps the transcript state the collection reflects, so
// a result for a since-modified session is recognized as stale.
type outputsCollectedMsg struct {
	id      string
	dataKey string
	outputs []session.SessionOutput
}

// collectOutputsCmd scans a session's transcript and filesystem state off the
// UI thread.
func collectOutputsCmd(sess session.Session, dataKey string) tea.Cmd {
	return func() tea.Msg {
		home, _ := os.UserHomeDir()
		outs := session.CollectSessionOutputs(sess, home)
		outs = append(outs, session.PlanFileOutputs(sess, home, outs)...)
		return outputsCollectedMsg{id: sess.ID, dataKey: dataKey, outputs: outs}
	}
}

// mergeRefOutputs returns the collected outputs plus the session's references
// as rows, in display order. Refs are merged here rather than in the collector
// so a ref whose status resolves later re-renders without a rescan.
func (a *App) mergeRefOutputs(collected []session.SessionOutput, refs []session.SessionRef) []session.SessionOutput {
	rows := make([]session.SessionOutput, 0, len(collected)+len(refs))
	rows = append(rows, collected...)
	for _, r := range refs {
		rows = append(rows, session.RefOutput(r))
	}
	session.SortOutputs(rows)
	return rows
}

// outputSection maps an output kind to its section heading. Kinds are emitted
// in SortOutputs order, so a heading is written whenever the kind changes.
func outputSection(k session.OutputKind) string {
	switch k {
	case session.OutputPR:
		return "Pull Requests"
	case session.OutputJira:
		return "Jira Issues"
	case session.OutputArtifact:
		return "Artifacts"
	case session.OutputPlan:
		return "Plans"
	case session.OutputMemory:
		return "Memory"
	case session.OutputChange:
		return "Files Changed"
	case session.OutputScratchpad:
		return "Scratchpad"
	}
	return ""
}

// outputGlyph returns the colored marker for an output row. Refs reuse their
// lifecycle coloring so an open PR still reads as open here.
func outputGlyph(o session.SessionOutput) string {
	switch o.Kind {
	case session.OutputPR, session.OutputJira, session.OutputArtifact:
		if o.Ref != nil {
			dot, _ := refStateBadge(*o.Ref)
			return dot
		}
		return dimStyle.Render("○")
	case session.OutputPlan:
		return planBadge.Render("◆")
	case session.OutputMemory:
		return memoryBadge.Render("◆")
	case session.OutputChange:
		return doneBadgeStyle.Render("▸")
	case session.OutputScratchpad:
		return dimStyle.Render("▸")
	}
	return dimStyle.Render("○")
}

// renderOutputs draws the digest. collecting is true while the transcript scan
// is still running and resolvingRefs while ref status is in flight, so an
// incomplete list never reads as a final answer.
func (a *App) renderOutputs(outs []session.SessionOutput, width int, collecting, resolvingRefs bool) string {
	var sb strings.Builder
	title := "── Outputs ──"
	if len(outs) > 0 && a.sessSplit.Focus {
		title = "── Outputs  ↵:open  y:copy ──"
	}
	sb.WriteString(statTitleStyle.Render(title) + "\n")
	if collecting {
		sb.WriteString(dimStyle.Render("scanning transcript…") + "\n")
	}
	if resolvingRefs {
		sb.WriteString(dimStyle.Render("resolving PR/Jira status…") + "\n")
	}
	sb.WriteString("\n")

	if len(outs) == 0 {
		if collecting {
			return sb.String()
		}
		sb.WriteString(dimStyle.Render("This session produced no plans, memory, files, or references."))
		return sb.String()
	}

	lastKind := session.OutputKind("")
	for i, o := range outs {
		if o.Kind != lastKind {
			if lastKind != "" {
				sb.WriteString("\n")
			}
			sb.WriteString(dimStyle.Bold(true).Render(outputSection(o.Kind)) + "\n")
			lastKind = o.Kind
		}
		selected := i == a.sessOutputsCursor && a.sessSplit.Focus
		sb.WriteString(outputLine(o, width, selected) + "\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// outputLine renders one digest row: cursor, kind glyph, title, then a dimmed
// detail and relative time. The detail is truncated to whatever the pane has
// left so a long path never wraps the row into two lines.
func outputLine(o session.SessionOutput, width int, selected bool) string {
	cursor := "  "
	titleStyle := lipgloss.NewStyle().Bold(true)
	if selected {
		cursor = lipgloss.NewStyle().Foreground(colorBorderFocused).Bold(true).Render("> ")
		titleStyle = titleStyle.Foreground(colorBorderFocused)
	}
	head := cursor + outputGlyph(o) + " " + titleStyle.Render(o.Title)

	suffix := ""
	if o.Count > 1 {
		suffix += dimStyle.Render(fmt.Sprintf("  ×%d", o.Count))
	}
	if !o.Last.IsZero() {
		suffix += dimStyle.Render("  · " + timeAgo(o.Last))
	}

	if o.Detail == "" {
		return head + suffix
	}
	// Reserve the head and suffix, then fit the detail into what's left; below
	// a usable minimum the detail is dropped rather than shown as an ellipsis.
	avail := width - lipgloss.Width(head) - lipgloss.Width(suffix) - 2
	if avail < 8 {
		return head + suffix
	}
	return head + "  " + dimStyle.Render(truncate(o.Detail, avail)) + suffix
}

// handleOutputsPreviewKeys drives the Outputs digest when the preview pane has
// focus: cursor movement plus Enter/o to open and y to copy the row's target.
func (a *App) handleOutputsPreviewKeys(sp *SplitPane, key string) (tea.Model, tea.Cmd, bool) {
	switch key {
	case "enter", "o":
		return a.openSelectedOutput()
	case a.keymap.Actions.CopyPath, "y":
		return a.copySelectedOutput()
	case "/":
		sp.Focus = false
		return a, startListSearch(&a.sessionList), true
	}
	switch HandleFlatCursorNav(&a.sessOutputsCursor, len(a.sessOutputsRows), key) {
	case NavCursorMoved:
		a.sessOutputsCacheKey = "" // re-render so the cursor highlight moves
		if sess, ok := a.selectedSession(); ok {
			return a, a.updateSessionOutputsPreview(sess), true
		}
		return a, nil, true
	case NavBoundaryDown, NavBoundaryUp:
		// Boundary crossing is disabled in the sessions preview (refs/agents do
		// the same) — the list pane keeps ownership of leaving the pane.
		return a, nil, true
	}
	if scrollViewport(&sp.Preview, key) {
		return a, nil, true
	}
	return a, nil, false
}

// selectedOutput returns the digest row under the cursor.
func (a *App) selectedOutput() (session.SessionOutput, bool) {
	if a.sessOutputsCursor < 0 || a.sessOutputsCursor >= len(a.sessOutputsRows) {
		return session.SessionOutput{}, false
	}
	return a.sessOutputsRows[a.sessOutputsCursor], true
}

// openSelectedOutput opens the output under the cursor: external refs go to the
// browser, and everything else jumps back to the conversation entry that
// produced it — the "why does this file exist" question the digest raises.
func (a *App) openSelectedOutput() (tea.Model, tea.Cmd, bool) {
	o, ok := a.selectedOutput()
	if !ok {
		return a, nil, true
	}
	if o.URL != "" {
		if err := a.openInBrowser(o.URL); err != nil {
			a.copiedMsg = "Open failed: " + err.Error()
		} else {
			a.copiedMsg = "Opened " + o.Title
		}
		return a, nil, true
	}
	if o.MessageUUID == "" {
		// Discovered on disk (scratchpad, a memory note with no recorded write):
		// there is no transcript entry to jump to, so surface the path instead
		// of silently doing nothing.
		a.copiedMsg = o.Path
		return a, nil, true
	}
	// Jump against the session the digest is tracking, not whatever the list
	// cursor resolves to — on a day row those differ.
	m, cmd := a.jumpToSessionEntry(a.sessOutputsCacheID, o.MessageUUID)
	return m, cmd, true
}

// copySelectedOutput copies the output's URL, or its path when it is a file.
func (a *App) copySelectedOutput() (tea.Model, tea.Cmd, bool) {
	o, ok := a.selectedOutput()
	if !ok {
		return a, nil, true
	}
	target := o.URL
	if target == "" {
		target = o.Path
	}
	if target == "" {
		return a, nil, true
	}
	copyToClipboard(target)
	a.copiedMsg = "Copied " + o.Title
	return a, nil, true
}
