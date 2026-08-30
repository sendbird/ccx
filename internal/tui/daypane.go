package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
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
	// when is the moment the output FIRST appeared — the day's timeline is built
	// on it and the time-sorted layout prints it. Refs extracted before
	// FirstSeen was recorded (and plan slugs, which have no entry at all) fall
	// back to the anchor session's ModTime: sinking them to the bottom of the
	// day with a zero time would read as "produced last", which is worse than
	// approximating with the session they came from.
	when time.Time
	// whenApprox marks a `when` that came from that fallback, so the timeline
	// can say "about here" (`~`) instead of stating a minute it does not know.
	whenApprox bool
}

// dayOutputTab is one kind filter over what a scope produced. The pane is a
// tabbed surface rather than one grouped list: a busy day produces 570 outputs,
// and with kinds stacked as sections the later groups sit hundreds of lines
// below the fold — reachable only by scrolling past everything else. A tab
// makes each kind one keypress away instead.
type dayOutputTab struct {
	label string
	// kind is the OutputKind this tab shows; empty means the timeline of
	// everything, which is the tab the pane opens on.
	kind session.OutputKind
}

// dayOutputTabAll is the timeline: every output the scope produced, in the order
// it first appeared, kinds interleaved. A day is lived in time, and "what
// happened after the PR went up" is what the pane is usually asked.
var dayOutputTabAll = dayOutputTab{label: "All"}

// dayOutputTabOrder lists the kind tabs in the order they are offered, matching
// outputKindRank (results before working material).
var dayOutputTabOrder = []dayOutputTab{
	{label: "PRs", kind: session.OutputPR},
	{label: "Jira", kind: session.OutputJira},
	{label: "Artifacts", kind: session.OutputArtifact},
	{label: "Plans", kind: session.OutputPlan},
}

// dayOutputTabsFor returns All plus a tab for every kind the scope actually
// produced. Offering a tab that is always empty would make the bar say more
// than the day does (the inspector's availableInspectorTabs does the same).
//
// active is kept in the bar even when this scope produced none of it — the tab
// is sticky as you walk dates, and dropping it would silently snap back to All
// and break the date-to-date comparison that stickiness exists for (the
// inspector keeps an explicitly-chosen tab the same way).
func dayOutputTabsFor(rows []dayOutputRow, active session.OutputKind) []dayOutputTab {
	present := make(map[session.OutputKind]bool, len(rows))
	for _, r := range rows {
		present[r.out.Kind] = true
	}
	tabs := []dayOutputTab{dayOutputTabAll}
	for _, t := range dayOutputTabOrder {
		if present[t.kind] || t.kind == active {
			tabs = append(tabs, t)
		}
	}
	return tabs
}

// dayOutputRowMatches reports whether a row matches every term in the query.
// Terms are AND-ed and matched case-insensitively against everything visible on
// the row plus its target, so "cplat argocd" narrows the way a reader expects
// without needing to know which field holds which part.
func dayOutputRowMatches(r dayOutputRow, terms []string) bool {
	if len(terms) == 0 {
		return true
	}
	hay := strings.ToLower(strings.Join([]string{
		r.out.Title, r.out.Detail, r.out.Path, r.out.URL,
		string(r.out.Kind), r.project, r.shortID,
	}, "\x00"))
	for _, t := range terms {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

// filterDayOutputRows narrows rows to one tab's kind and the pane's own search
// query. The result is what the pane both renders AND indexes with
// dayOutputsCursor — filtering at render time only would leave Enter/o/y/x
// acting on a different output than the highlighted one.
//
// The query is the day pane's own, independent of the session list's filter:
// the two panes answer different questions ("which sessions" vs "which
// outputs"), and a day with 682 outputs needs narrowing even when the session
// list does not.
func filterDayOutputRows(rows []dayOutputRow, tab dayOutputTab, query string) []dayOutputRow {
	terms := strings.Fields(strings.ToLower(strings.TrimSpace(query)))
	if tab.kind == "" && len(terms) == 0 {
		return rows
	}
	out := make([]dayOutputRow, 0, len(rows))
	for _, r := range rows {
		if tab.kind != "" && r.out.Kind != tab.kind {
			continue
		}
		if !dayOutputRowMatches(r, terms) {
			continue
		}
		out = append(out, r)
	}
	return out
}

// dayOutputTabIndex locates the active tab in the bar. dayOutputTabsFor always
// includes the active kind, so the All fallback here only fires for a kind that
// is not a tab at all (a config or ordering mismatch), never for an empty day.
func dayOutputTabIndex(tabs []dayOutputTab, active session.OutputKind) int {
	for i, t := range tabs {
		if t.kind == active {
			return i
		}
	}
	return 0
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
// Rows come out as a timeline of first appearances. Chronological is NOT the
// natural insertion order — a session's Refs are sorted first-seen DESCENDING
// (session.SortRefs), so the timeline only exists because of the explicit sort
// below. Kind filtering is a separate step (filterDayOutputRows) so every tab
// keeps this one order.
func buildDayOutputRows(di dayItem) []dayOutputRow {
	var rows []dayOutputRow
	byKey := map[string]int{} // identity → index into rows
	add := func(o session.SessionOutput, s session.Session, ts time.Time, approx bool) {
		key := string(o.Kind) + "\x00" + o.Title
		if i, ok := byKey[key]; ok {
			rows[i].sessions++
			// Keep the earliest occurrence's timestamp as the row's time. ONLY
			// the timestamp: the rest of the output — crucially MessageUUID —
			// must stay with the anchor session, since a uuid from a later
			// session does not exist in the anchor's transcript and the jump
			// would silently miss.
			if !ts.IsZero() && (rows[i].when.IsZero() || ts.Before(rows[i].when)) {
				rows[i].when, rows[i].whenApprox = ts, approx
			}
			return
		}
		byKey[key] = len(rows)
		rows = append(rows, dayOutputRow{
			out: o, sessID: s.ID, shortID: s.ShortID, project: s.ProjectName, sessions: 1,
			when: ts, whenApprox: approx,
		})
	}
	for _, s := range chronological(di.sessions) {
		for _, r := range s.Refs {
			o := session.RefOutput(r)
			ts, approx := outputWhen(o.First, s)
			add(o, s, ts, approx)
		}
		for _, slug := range s.PlanSlugs {
			// A plan slug records no entry at all, so its time is always the
			// session's.
			add(session.SessionOutput{
				Kind: session.OutputPlan, Title: slug, Last: s.ModTime, Count: 1,
			}, s, s.ModTime, true)
		}
	}
	sortDayOutputRowsByTime(rows)
	return rows
}

// outputWhen resolves the moment an output first appeared, falling back to the
// producing session's last-activity time when the ref carries no timestamp
// (extracted by an older build). A zero time would sort to one end of the day
// and read as a claim about when the work happened; the session's own time is a
// bounded approximation instead. The bool says which of the two it returned, so
// the timeline can mark the approximation rather than assert a minute.
func outputWhen(first time.Time, s session.Session) (time.Time, bool) {
	if !first.IsZero() {
		return first, false
	}
	return s.ModTime, true
}

// sortDayOutputRowsByTime orders rows oldest-first — a day reads as a journal,
// the opposite of the list rows (which lead with the most recent). Ties break
// on title so the order is stable across rebuilds rather than depending on map
// iteration.
func sortDayOutputRowsByTime(rows []dayOutputRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i].when, rows[j].when
		if a.IsZero() != b.IsZero() {
			return b.IsZero() // rows with no time at all go last
		}
		if !a.Equal(b) {
			return a.Before(b)
		}
		return rows[i].out.Title < rows[j].out.Title
	})
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
	// day's row list. The TAB is deliberately kept — walking dates under one
	// kind filter is what makes it useful.
	if a.dayOutputsCacheID != di.dayKey {
		a.dayOutputsCursor = 0
		a.dayOutputsCacheID = di.dayKey
		// The query is dropped on a scope change, unlike the TAB. A kind filter
		// is a lens you carry across dates; a text query is about one day's
		// specific rows, and carrying it would silently hide the new day's
		// outputs behind a filter the user is no longer thinking about.
		a.dayOutputQuery = ""
	}
	all := buildDayOutputRows(di)

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
	a.sessSplit.Preview.SetContent(a.renderOutputsPane(title, subtitle, summary, di.day, all, previewW))
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
		a.dayOutputQuery = "" // see updateDayPreview: queries do not travel
	}
	all := buildDayOutputRows(dayItem{sessions: pi.sessions})

	if a.sessSplit.Preview.Width != previewW || a.sessSplit.Preview.Height != contentH {
		a.sessSplit.Preview = viewport.New(previewW, contentH)
	}
	subtitle := pi.basePath
	if pi.branch != "" {
		subtitle += "  (" + pi.branch + ")"
	}
	summary := fmt.Sprintf("%s · %d messages on this day",
		plural(len(pi.sessions), "session"), pi.totalMsgs)
	a.sessSplit.Preview.SetContent(a.renderOutputsPane(pi.displayName, subtitle, summary, dayKeyTime(pi.dayKey), all, previewW))
}

// dayKeyTime parses a "2006-01-02" fold key back into a local date. A zero time
// on failure is harmless: it only makes the timeline print full dates instead
// of bare times.
func dayKeyTime(key string) time.Time {
	t, err := time.ParseInLocation("2006-01-02", key, time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}

// renderOutputsPane draws a "what this produced" pane for any scope in the
// daily tree: a whole day, or one project within it. Only the header text
// differs — the outputs list is the same shape at every level. day is the
// calendar date the scope covers, which the timeline uses to decide whether a
// row's time needs its date spelled out.
//
// all is every row the scope produced; the active tab narrows it. The narrowed
// slice is stored on the App because that is what dayOutputsCursor indexes —
// Enter/o/y/x all resolve through it, so the rendered list and the actionable
// list must be the same slice.
func (a *App) renderOutputsPane(title, subtitle, summary string, day time.Time, all []dayOutputRow, width int) string {
	section := lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	var sb strings.Builder

	tabs := dayOutputTabsFor(all, a.dayOutputTabKind)
	active := tabs[dayOutputTabIndex(tabs, a.dayOutputTabKind)]
	rows := filterDayOutputRows(all, active, a.dayOutputQuery)
	if a.dayOutputsCursor >= len(rows) {
		a.dayOutputsCursor = 0
	}
	a.dayOutputRows = rows

	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(colorPrimary).Render(title))
	if subtitle != "" {
		sb.WriteString(dimStyle.Render("  " + subtitle))
	}
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render(summary))
	sb.WriteString("\n\n")

	sb.WriteString(a.renderDayOutputTabs(tabs, active, all) + "\n")

	heading := fmt.Sprintf("Produced (%d)", len(rows))
	if a.dayOutputQuery != "" {
		// Say the count is filtered and by what. Without this a narrowed list
		// reads as "this day produced 3 things", which is a different claim.
		heading += fmt.Sprintf(" of %d  /%s", len(all), a.dayOutputQuery)
	}
	if len(rows) > 0 && a.sessSplit.Focus {
		heading += "  ↵:jump to first mention  o:open  y:copy  x:actions  /:search"
	}
	sb.WriteString(section.Render(heading) + "\n")

	switch {
	case len(rows) == 0 && a.dayOutputQuery != "":
		sb.WriteString(dimStyle.Render(fmt.Sprintf("  nothing matching %q", a.dayOutputQuery)) + "\n\n")
	case len(rows) == 0 && active.kind != "":
		// The tab is sticky across dates on purpose, so an empty day under a
		// kind filter is a real answer ("this day produced no PRs"), not a
		// reason to silently fall back to All.
		sb.WriteString(dimStyle.Render("  nothing of this kind on this day") + "\n\n")
	case len(rows) == 0:
		// Refs resolve lazily, so "nothing yet" is the honest phrasing: rows fill
		// in as the background extract lands rather than this being a verdict.
		sb.WriteString(dimStyle.Render("  no references or plans recorded yet") + "\n\n")
	default:
		// One run of rows in first-appearance order, each stamped with when it
		// appeared. Kind headings are deliberately absent at every tab: under a
		// kind tab they would repeat one word, and in All — with kinds
		// interleaved — they would fire on nearly every row. The glyph already
		// says what a row is.
		for i, r := range rows {
			sb.WriteString(dayOutputLine(r, day, width, i == a.dayOutputsCursor && a.sessSplit.Focus) + "\n")
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
		sb.WriteString(dimStyle.Render("↵ jumps to where it first appeared  •  o opens it  •  y copies  •  x lists every action for the row  •  1-9/tab switch kind  •  ↑↓ moves between outputs"))
	} else {
		sb.WriteString(dimStyle.Render("↵/o folds this row  •  1-9/tab switch kind  •  → focuses this pane"))
	}
	return sb.String()
}

// renderDayOutputTabs draws the kind tab bar, each tab carrying its own count so
// the bar doubles as the day's rollup — you can see there were 424 PRs without
// opening that tab.
func (a *App) renderDayOutputTabs(tabs []dayOutputTab, active dayOutputTab, all []dayOutputRow) string {
	counts := make(map[session.OutputKind]int, len(tabs))
	for _, r := range all {
		counts[r.out.Kind]++
	}
	hl := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	parts := make([]string, 0, len(tabs))
	for _, t := range tabs {
		n := len(all)
		if t.kind != "" {
			n = counts[t.kind]
		}
		label := fmt.Sprintf("%s %d", t.label, n)
		if t == active {
			parts = append(parts, hl.Render("["+label+"]"))
			continue
		}
		parts = append(parts, dimStyle.Render(" "+label+" "))
	}
	return "  " + strings.Join(parts, " ")
}

// dayOutputTime formats a row's first-appearance stamp for the timeline. Within
// the scope's own date a bare time is enough; an output whose first mention
// lands on another day (a long-lived session, or a ref carried in from an
// earlier one) spells the date out rather than showing a time that silently
// belongs elsewhere. approx times — taken from the producing session because
// the output records no entry of its own — are marked `~`, since printing them
// bare would state a minute the row does not actually know.
func dayOutputTime(when, day time.Time, approx bool) string {
	if when.IsZero() {
		return "  --  "
	}
	lead := " "
	if approx {
		lead = "~"
	}
	if !day.IsZero() && (when.Year() != day.Year() || when.YearDay() != day.YearDay()) {
		return lead + when.Format("Jan 2 15:04")
	}
	return lead + when.Format("15:04") + " "
}

// dayOutputLine renders one output row: cursor, first-appearance stamp, kind
// glyph, title, then the producing session as a dimmed anchor. The stamp leads
// because it is the column the eye scans down — every tab is a timeline.
func dayOutputLine(r dayOutputRow, day time.Time, width int, selected bool) string {
	cursor := "  "
	titleStyle := lipgloss.NewStyle().Bold(true)
	if selected {
		cursor = lipgloss.NewStyle().Foreground(colorBorderFocused).Bold(true).Render("> ")
		titleStyle = titleStyle.Foreground(colorBorderFocused)
	}
	stamp := dimStyle.Render(dayOutputTime(r.when, day, r.whenApprox)) + " "
	head := cursor + stamp + outputGlyph(r.out) + " " + titleStyle.Render(r.out.Title)

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
//
// tab/shift+tab are NOT handled here: km.Session.Preview consumes them long
// before the focused-preview handlers run, so kind switching lives in that case
// instead (see handleSessionKeys).
func (a *App) handleDayPreviewKeys(sp *SplitPane, key string) (tea.Model, tea.Cmd, bool) {
	switch key {
	case "enter":
		return a.openSelectedDayOutput()
	case "o":
		return a.openSelectedDayOutputTarget()
	case a.keymap.Actions.CopyPath, "y":
		return a.copySelectedDayOutput()
	case "/":
		// Search the pane you are in. Focus stays here: the day pane has its own
		// query because "which outputs" and "which sessions" are different
		// questions, and a day with hundreds of rows needs narrowing on its own
		// terms.
		a.startDayOutputSearch()
		return a, nil, true
	}
	switch HandleFlatCursorNav(&a.dayOutputsCursor, len(a.dayOutputRows), key) {
	case NavCursorMoved:
		a.sessSplit.CacheKey = "" // force the day pane to re-render with the new highlight
		a.renderOwningDayScope()
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

// cycleDayOutputTab moves the day pane's kind filter by delta, wrapping. Only
// the tabs the scope actually has are in the ring, so the cycle never lands on
// an always-empty kind.
//
// The cursor goes back to the top: every row action (Enter, o, y, x) resolves
// through dayOutputsCursor into the FILTERED slice, so keeping an index across
// a tab switch would point at a different output than the highlighted one.
func (a *App) cycleDayOutputTab(delta int) {
	tabs := dayOutputTabsFor(a.currentDayOutputRows(), a.dayOutputTabKind)
	if len(tabs) <= 1 {
		return
	}
	idx := dayOutputTabIndex(tabs, a.dayOutputTabKind)
	a.setDayOutputTabKind(tabs[(idx+delta+len(tabs))%len(tabs)].kind)
}

// selectDayOutputTab jumps straight to the n-th tab in the bar, 1-based and
// POSITIONAL: "1" is whatever sits leftmost (always All), "2" the next one, and
// so on, exactly as the bar reads on screen. The tabs are built per scope
// (dayOutputTabsFor drops kinds the day produced none of), so a fixed
// digit→kind table would point the digits at labels that are not there.
//
// Reports whether n addressed a tab; out of range is the caller's to swallow.
func (a *App) selectDayOutputTab(n int) bool {
	tabs := dayOutputTabsFor(a.currentDayOutputRows(), a.dayOutputTabKind)
	if n < 1 || n > len(tabs) {
		return false
	}
	a.setDayOutputTabKind(tabs[n-1].kind)
	return true
}

// setDayOutputTabKind applies a tab switch: repaint the pane under the new
// filter and put the cursor back on its first row. Re-selecting the active tab
// keeps the cursor where it is — nothing about the list changed, so moving it
// would be a switch the user did not ask for.
func (a *App) setDayOutputTabKind(kind session.OutputKind) {
	if kind == a.dayOutputTabKind {
		return
	}
	a.dayOutputTabKind = kind
	a.dayOutputsCursor = 0
	a.sessSplit.CacheKey = ""
	a.renderOwningDayScope()
	a.sessSplit.Preview.GotoTop()
}

// dayOutputTabHint lists the digit → tab bindings for the help overlay, in the
// bar's own order, so the hint matches what the pane is showing rather than the
// preview modes the digits carry on a session row.
func (a *App) dayOutputTabHint() string {
	tabs := dayOutputTabsFor(a.currentDayOutputRows(), a.dayOutputTabKind)
	parts := make([]string, 0, len(tabs))
	for i, t := range tabs {
		if i >= 9 {
			break
		}
		parts = append(parts, fmt.Sprintf("%d:%s", i+1, strings.ToLower(t.label)))
	}
	return strings.Join(parts, " ")
}

// currentDayOutputRows rebuilds the UNFILTERED rows for whichever scope owns the
// pane. The tab bar needs every kind the scope produced, which the filtered
// a.dayOutputRows no longer knows.
func (a *App) currentDayOutputRows() []dayOutputRow {
	if di, ok := a.selectedDay(); ok {
		return buildDayOutputRows(di)
	}
	if pi, ok := a.selectedProject(); ok && pi.dayKey != "" {
		return buildDayOutputRows(dayItem{sessions: pi.sessions})
	}
	return nil
}

// renderOwningDayScope re-renders whichever scope owns the day pane. A
// day-scoped PROJECT row owns it too (selectedOwnsDayPane), and rendering only
// the day case left the pane frozen on those rows.
func (a *App) renderOwningDayScope() {
	if di, ok := a.selectedDay(); ok {
		a.updateDayPreview(di)
		return
	}
	if pi, ok := a.selectedProject(); ok && pi.dayKey != "" {
		a.updateDayProjectPreview(pi)
	}
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

// startDayOutputSearch opens the day pane's own search input, pre-filled with
// the applied query so refining is editing rather than retyping.
func (a *App) startDayOutputSearch() {
	a.dayOutputSearching = true
	// Remember what was applied when the input opened. Typing applies live, so
	// by the time Esc arrives dayOutputQuery already holds the edited value and
	// is no longer what "cancel" should restore.
	a.dayOutputQueryBefore = a.dayOutputQuery
	ti := textinput.New()
	ti.Prompt = "Search outputs: "
	ti.SetValue(a.dayOutputQuery)
	ti.CursorEnd()
	ti.Focus()
	a.dayOutputSearchTI = ti
}

// handleDayOutputSearch processes keys while the day pane's search is active.
// The query applies as you type so the row count reacts immediately; Esc
// restores whatever was applied when the input opened.
func (a *App) handleDayOutputSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		a.dayOutputSearching = false
		a.applyDayOutputQuery(a.dayOutputSearchTI.Value())
		return a, nil
	case "esc":
		a.dayOutputSearching = false
		// Esc cancels the edit, not the filter: it restores what was applied when
		// the input opened, so an abandoned edit does not silently become the
		// filter and an accidental keypress does not lose the narrowing.
		a.applyDayOutputQuery(a.dayOutputQueryBefore)
		return a, nil
	}
	var cmd tea.Cmd
	a.dayOutputSearchTI, cmd = a.dayOutputSearchTI.Update(msg)
	a.applyDayOutputQuery(a.dayOutputSearchTI.Value())
	return a, cmd
}

// applyDayOutputQuery sets the pane's query and re-renders. The cursor goes back
// to the top for the same reason a tab switch resets it: every row action
// resolves through dayOutputsCursor into the FILTERED slice, so an index kept
// across a filter change would point at a different output than the highlighted
// one.
func (a *App) applyDayOutputQuery(q string) {
	a.dayOutputQuery = q
	a.dayOutputsCursor = 0
	a.sessSplit.CacheKey = ""
	a.renderOwningDayScope()
}

// clearDayOutputSearch drops the pane's query entirely.
func (a *App) clearDayOutputSearch() {
	a.dayOutputSearching = false
	a.applyDayOutputQuery("")
}
