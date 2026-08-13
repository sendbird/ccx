package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
)

// The day pane answers "what came out of this day?". Every row is an output
// with the session that produced it as an anchor — the digest tells you what,
// and Enter on a row is how you get to how.

// dayOutputRow is one line in the day preview: an output the day produced, plus
// the session that produced it. The session is the anchor — Enter on any row
// opens that conversation, which is the only path from "what came out" back to
// "how it happened".
type dayOutputRow struct {
	out     session.SessionOutput
	sessID  string
	shortID string
	project string
	// sessions counts how many of the day's sessions touched this same output.
	// A PR gets discussed across several sessions; the row is the PR, not each
	// mention, and this says how widely it spread.
	sessions int
}

// buildDayOutputRows collects the day's outputs from state the scan and the ref
// pipeline already loaded — no transcripts, so moving across dates stays
// instant. Refs and plan slugs are what a session records without being parsed;
// the per-session Outputs digest is what pays the parse cost, on demand.
//
// Identical outputs are collapsed: on a busy day the same PR is referenced from
// half a dozen sessions, and listing each mention buries the other results. The
// row keeps its FIRST session as the anchor — where the work actually happened,
// as opposed to wherever it was later quoted.
//
// Rows are chronological: a day reads as a journal, the opposite of the list
// rows (which lead with the most recent).
func buildDayOutputRows(di dayItem) []dayOutputRow {
	var rows []dayOutputRow
	byKey := map[string]int{} // identity → index into rows
	add := func(o session.SessionOutput, s session.Session) {
		key := string(o.Kind) + "\x00" + o.Title
		if i, ok := byKey[key]; ok {
			rows[i].sessions++
			// Keep the earliest occurrence's timestamp as the row's time. ONLY
			// the timestamp: the rest of the output — crucially MessageUUID —
			// must stay with the anchor session, since a uuid from a later
			// session does not exist in the anchor's transcript and the jump
			// would silently miss.
			if !o.Last.IsZero() && (rows[i].out.Last.IsZero() || o.Last.Before(rows[i].out.Last)) {
				rows[i].out.Last = o.Last
			}
			return
		}
		byKey[key] = len(rows)
		rows = append(rows, dayOutputRow{
			out: o, sessID: s.ID, shortID: s.ShortID, project: s.ProjectName, sessions: 1,
		})
	}
	for _, s := range chronological(di.sessions) {
		for _, r := range s.Refs {
			add(session.RefOutput(r), s)
		}
		for _, slug := range s.PlanSlugs {
			add(session.SessionOutput{
				Kind: session.OutputPlan, Title: slug, Last: s.ModTime, Count: 1,
			}, s)
		}
	}
	// Group by kind (results before working material), preserving the
	// chronological order established above within each kind.
	sort.SliceStable(rows, func(i, j int) bool {
		return outputKindRank(rows[i].out.Kind) < outputKindRank(rows[j].out.Kind)
	})
	return rows
}

// outputKindRank mirrors session.SortOutputs' kind ordering for row structs,
// which carry more than a SessionOutput and so cannot use it directly.
func outputKindRank(k session.OutputKind) int {
	switch k {
	case session.OutputPR:
		return 0
	case session.OutputJira:
		return 1
	case session.OutputArtifact:
		return 2
	case session.OutputPlan:
		return 3
	case session.OutputMemory:
		return 4
	case session.OutputChange:
		return 5
	}
	return 6
}

// updateDayPreview renders the day pane: what the day produced, one row per
// output, each anchored to the session that made it. Sessions themselves are
// deliberately NOT listed — they are one row away in the list pane, and a busy
// day really can hold 250+ of them, which would bury the outputs entirely.
func (a *App) updateDayPreview(di dayItem) {
	previewW := max(a.width-a.sessSplit.ListWidth(a.width, a.splitRatio)-1, 1)
	contentH := max(a.height-3, 1)

	// Reset the cursor when the day changes so it never points past a shorter
	// day's row list.
	if a.dayOutputsCacheID != di.dayKey {
		a.dayOutputsCursor = 0
		a.dayOutputsCacheID = di.dayKey
	}
	rows := buildDayOutputRows(di)
	if a.dayOutputsCursor >= len(rows) {
		a.dayOutputsCursor = 0
	}
	a.dayOutputRows = rows

	// Recreate the viewport only on a size change. Rebuilding it every call
	// would reset YOffset to 0, and since cursor movement re-renders, every
	// keypress would snap a long day (462 rows is a real measurement) back to
	// the top — making everything below the fold unreachable.
	if a.sessSplit.Preview.Width != previewW || a.sessSplit.Preview.Height != contentH {
		a.sessSplit.Preview = viewport.New(previewW, contentH)
	}
	title := dayLabel(di.day, time.Now())
	subtitle := di.day.Format("Mon, Jan 2 2006")
	summary := fmt.Sprintf("%s across %s · %d messages",
		plural(len(di.sessions), "session"), plural(di.projects, "project"), di.totalMsgs)
	a.sessSplit.Preview.SetContent(a.renderOutputsPane(title, subtitle, summary, rows, previewW))
}

// updateDayProjectPreview renders the middle tier of the daily tree: one day's
// work in one project. Same pane as the day view, scoped down — the project
// breakdown is dropped because at this level there is only one project.
func (a *App) updateDayProjectPreview(pi projectItem) {
	previewW := max(a.width-a.sessSplit.ListWidth(a.width, a.splitRatio)-1, 1)
	contentH := max(a.height-3, 1)

	cacheID := pi.dayKey + "|" + pi.basePath
	if a.dayOutputsCacheID != cacheID {
		a.dayOutputsCursor = 0
		a.dayOutputsCacheID = cacheID
	}
	rows := buildDayOutputRows(dayItem{sessions: pi.sessions})
	if a.dayOutputsCursor >= len(rows) {
		a.dayOutputsCursor = 0
	}
	a.dayOutputRows = rows

	if a.sessSplit.Preview.Width != previewW || a.sessSplit.Preview.Height != contentH {
		a.sessSplit.Preview = viewport.New(previewW, contentH)
	}
	subtitle := pi.basePath
	if pi.branch != "" {
		subtitle += "  (" + pi.branch + ")"
	}
	summary := fmt.Sprintf("%s · %d messages on this day",
		plural(len(pi.sessions), "session"), pi.totalMsgs)
	a.sessSplit.Preview.SetContent(a.renderOutputsPane(pi.displayName, subtitle, summary, rows, previewW))
}

// renderOutputsPane draws a "what this produced" pane for any scope in the
// daily tree: a whole day, or one project within it. Only the header text
// differs — the outputs list is the same shape at every level.
func (a *App) renderOutputsPane(title, subtitle, summary string, rows []dayOutputRow, width int) string {
	section := lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	var sb strings.Builder

	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render(title))
	if subtitle != "" {
		sb.WriteString(dimStyle.Render("  " + subtitle))
	}
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render(summary))
	sb.WriteString("\n\n")

	heading := fmt.Sprintf("Produced (%d)", len(rows))
	if len(rows) > 0 && a.sessSplit.Focus {
		heading += "  ↵:jump to first mention  o:open  y:copy  x:actions"
	}
	sb.WriteString(section.Render(heading) + "\n")

	if len(rows) == 0 {
		// Refs resolve lazily, so "nothing yet" is the honest phrasing: rows fill
		// in as the background extract lands rather than this being a verdict.
		sb.WriteString(dimStyle.Render("  no references or plans recorded yet") + "\n\n")
	} else {
		lastKind := session.OutputKind("")
		for i, r := range rows {
			if r.out.Kind != lastKind {
				if lastKind != "" {
					sb.WriteString("\n")
				}
				sb.WriteString(dimStyle.Bold(true).Render("  "+outputSection(r.out.Kind)) + "\n")
				lastKind = r.out.Kind
			}
			sb.WriteString(dayOutputLine(r, width, i == a.dayOutputsCursor && a.sessSplit.Focus) + "\n")
		}
		sb.WriteString("\n")
	}

	// No project breakdown here: the list itself now nests day → project →
	// session, so the projects are one row below and repeating them in the pane
	// would say the same thing twice.
	//
	// The footer describes whichever pane owns the keys. Unfocused, Enter still
	// belongs to the list (it folds the row); focused, the keys are this pane's,
	// and saying otherwise sent people to the wrong action.
	if a.sessSplit.Focus {
		sb.WriteString(dimStyle.Render("↵ jumps to where it first appeared  •  o opens it  •  y copies  •  x lists every action for the row  •  ↑↓ moves between outputs"))
	} else {
		sb.WriteString(dimStyle.Render("↵/o folds this row  •  tab focuses this pane"))
	}
	return sb.String()
}

// dayOutputLine renders one output row: cursor, kind glyph, title, then the
// producing session as a dimmed anchor.
func dayOutputLine(r dayOutputRow, width int, selected bool) string {
	cursor := "  "
	titleStyle := lipgloss.NewStyle().Bold(true)
	if selected {
		cursor = lipgloss.NewStyle().Foreground(colorBorderFocused).Bold(true).Render("> ")
		titleStyle = titleStyle.Foreground(colorBorderFocused)
	}
	head := cursor + outputGlyph(r.out) + " " + titleStyle.Render(r.out.Title)

	anchor := dimStyle.Render("  " + r.shortID)
	if r.sessions > 1 {
		anchor = dimStyle.Render(fmt.Sprintf("  %s +%d", r.shortID, r.sessions-1))
	}
	if r.project != "" {
		anchor += dimStyle.Render(" · " + r.project)
	}

	detail := r.out.Detail
	if detail == "" {
		return head + anchor
	}
	avail := width - lipgloss.Width(head) - lipgloss.Width(anchor) - 2
	if avail < 8 {
		return head + anchor
	}
	return head + "  " + dimStyle.Render(truncate(detail, avail)) + anchor
}

// handleDayPreviewKeys drives the day pane when the preview has focus: the
// cursor moves between outputs, Enter jumps into the conversation at the moment
// the output first appeared, and `o` opens the output itself (a PR/Jira URL) in
// the browser. The two are deliberately different questions — "how did this
// happen" vs "take me to the thing" — so they are no longer aliases.
func (a *App) handleDayPreviewKeys(sp *SplitPane, key string) (tea.Model, tea.Cmd, bool) {
	switch key {
	case "enter":
		return a.openSelectedDayOutput()
	case "o":
		return a.openSelectedDayOutputTarget()
	case a.keymap.Actions.CopyPath, "y":
		return a.copySelectedDayOutput()
	case "/":
		sp.Focus = false
		return a, startListSearch(&a.sessionList), true
	}
	switch HandleFlatCursorNav(&a.dayOutputsCursor, len(a.dayOutputRows), key) {
	case NavCursorMoved:
		a.sessSplit.CacheKey = "" // force the day pane to re-render with the new highlight
		// Re-render whichever scope owns the pane. A day-scoped PROJECT row owns
		// it too (selectedOwnsDayPane), and rendering only the day case left the
		// highlight frozen on those rows.
		if di, ok := a.selectedDay(); ok {
			a.updateDayPreview(di)
		} else if pi, ok := a.selectedProject(); ok && pi.dayKey != "" {
			a.updateDayProjectPreview(pi)
		}
		// Nudge the viewport so the cursor stays in view as it walks past the
		// fold (the tasks/agents preview does the same).
		switch key {
		case "up", "k":
			sp.Preview.LineUp(1)
		case "down", "j":
			sp.Preview.LineDown(1)
		}
		return a, nil, true
	case NavBoundaryDown, NavBoundaryUp:
		return a, nil, true
	}
	if scrollViewport(&sp.Preview, key) {
		return a, nil, true
	}
	return a, nil, false
}

func (a *App) selectedDayOutput() (dayOutputRow, bool) {
	if a.dayOutputsCursor < 0 || a.dayOutputsCursor >= len(a.dayOutputRows) {
		return dayOutputRow{}, false
	}
	return a.dayOutputRows[a.dayOutputsCursor], true
}

// openSelectedDayOutput jumps to the conversation entry where the output under
// the cursor FIRST appeared. The digest says what came out; this is how you get
// to how — and the interesting moment is the first mention, not the session as
// a whole. The uuid comes from the row's anchor session (buildDayOutputRows
// keeps the earliest occurrence), so it resolves inside the transcript we open.
// Outputs with no recorded entry (plan slugs carried over from a parent
// session) still open the conversation.
func (a *App) openSelectedDayOutput() (tea.Model, tea.Cmd, bool) {
	r, ok := a.selectedDayOutput()
	if !ok {
		return a, nil, true
	}
	if r.out.MessageUUID != "" {
		m, cmd := a.jumpToSessionEntry(r.sessID, r.out.MessageUUID)
		return m, cmd, true
	}
	sess, ok := a.sessionByIDFromStore(r.sessID)
	if !ok {
		return a, nil, true
	}
	a.currentSess = sess
	return a, a.openConversation(sess), true
}

// openSelectedDayOutputTarget opens the output itself: a PR/Jira/artifact URL
// goes to the browser, a file-backed output surfaces its path, and anything
// with neither falls back to the producing conversation.
func (a *App) openSelectedDayOutputTarget() (tea.Model, tea.Cmd, bool) {
	r, ok := a.selectedDayOutput()
	if !ok {
		return a, nil, true
	}
	if r.out.URL != "" {
		if err := a.openInBrowser(r.out.URL); err != nil {
			a.copiedMsg = "Open failed: " + err.Error()
		} else {
			a.copiedMsg = "Opened " + r.out.Title
		}
		return a, nil, true
	}
	if r.out.Path != "" {
		a.copiedMsg = r.out.Path
		return a, nil, true
	}
	return a.openSelectedDayOutput()
}

// copySelectedDayOutput copies the output's URL, or its path when it is a file.
func (a *App) copySelectedDayOutput() (tea.Model, tea.Cmd, bool) {
	r, ok := a.selectedDayOutput()
	if !ok {
		return a, nil, true
	}
	target := r.out.URL
	if target == "" {
		target = r.out.Path
	}
	if target == "" {
		a.copiedMsg = "No URL for this output"
		return a, nil, true
	}
	copyToClipboard(target)
	a.copiedMsg = "Copied " + r.out.Title
	return a, nil, true
}

// chronological returns the day's sessions oldest-first — how a journal reads,
// the opposite of the list rows (which lead with the most recent).
func chronological(sessions []session.Session) []session.Session {
	out := make([]session.Session, len(sessions))
	copy(out, sessions)
	sort.SliceStable(out, func(i, j int) bool { return out[i].ModTime.Before(out[j].ModTime) })
	return out
}
