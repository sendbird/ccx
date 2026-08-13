package tui

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
)

// dayItem is a date row in the daily view: a folder-like parent whose children
// are the sessions active on that day. Like projectItem it is not a session —
// Enter toggles its fold state rather than opening anything.
type dayItem struct {
	dayKey   string // "2006-01-02", the stable fold/identity key
	day      time.Time
	sessions []session.Session // that day's sessions, most-recent-first
	expanded bool

	totalMsgs int
	projects  int // distinct project roots touched that day
	liveCount int
	openPRs   int
	// Output rollup — what the day actually produced.
	prs       int
	jiras     int
	artifacts int
	plans     int
}

func (d dayItem) FilterValue() string {
	// Mirror projectItem: a date row stays visible whenever any session under
	// it matches, so filtering inside the daily view keeps its date headers.
	parts := make([]string, 0, len(d.sessions)+3)
	parts = append(parts, d.dayKey, d.day.Format("Mon Jan 2 2006"), "is:day")
	for _, s := range d.sessions {
		parts = append(parts, session.FilterValueFor(s, nil))
	}
	return strings.Join(parts, " ")
}

// dayFoldKey is the sessFolded key for a date row.
func dayFoldKey(dayKey string) string { return "day:" + dayKey }

// buildDailyItems groups sessions by the calendar day of their last activity
// (ModTime, local time), newest day first. A session that spanned several days
// appears once, under the day it was last active — duplicating a row across
// days would break multi-select (keyed by session ID) and fold bookkeeping.
func buildDailyItems(sessions []session.Session, folded map[string]bool) []list.Item {
	groups := make(map[string]*dayItem)
	for i := range sessions {
		s := sessions[i]
		y, m, d := s.ModTime.Date()
		day := time.Date(y, m, d, 0, 0, 0, 0, s.ModTime.Location())
		key := day.Format("2006-01-02")
		g, ok := groups[key]
		if !ok {
			g = &dayItem{dayKey: key, day: day}
			groups[key] = g
		}
		g.sessions = append(g.sessions, s)
	}

	days := make([]*dayItem, 0, len(groups))
	for _, g := range groups {
		sort.Slice(g.sessions, func(i, j int) bool {
			return g.sessions[i].ModTime.After(g.sessions[j].ModTime)
		})
		projects := make(map[string]bool, len(g.sessions))
		for _, s := range g.sessions {
			g.totalMsgs += s.MsgCount
			projects[s.ProjectPath] = true
			if s.IsLive {
				g.liveCount++
			}
			openPRs, _ := s.OpenRefCounts()
			g.openPRs += openPRs
			for _, r := range s.Refs {
				switch r.Kind {
				case session.RefPR:
					g.prs++
				case session.RefJira:
					g.jiras++
				case session.RefArtifact:
					g.artifacts++
				}
			}
			g.plans += len(s.PlanSlugs)
		}
		g.projects = len(projects)
		g.expanded = !folded[dayFoldKey(g.dayKey)]
		days = append(days, g)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].day.After(days[j].day) })

	items := make([]list.Item, 0, len(days)*4)
	for _, g := range days {
		items = append(items, *g)
		if !g.expanded {
			continue
		}
		items = append(items, dayProjectItems(g, folded)...)
	}
	return items
}

// dayProjectRows groups one day's sessions by project, most-active-first. The
// middle tier of the day → project → session tree: a heavy day spans 30
// projects, and reading it as one flat run of sessions loses the thing you
// actually want to know — which work areas the day went into.
func dayProjectRows(g *dayItem) []projectItem {
	byPath := map[string]*projectItem{}
	var order []string
	for _, s := range g.sessions {
		key := s.ProjectPath
		p, ok := byPath[key]
		if !ok {
			name := s.ProjectName
			if name == "" {
				name = filepathBase(s.ProjectPath)
			}
			p = &projectItem{basePath: key, displayName: name, branch: s.GitBranch}
			byPath[key] = p
			order = append(order, key)
		}
		p.sessions = append(p.sessions, s)
	}

	rows := make([]projectItem, 0, len(order))
	for _, key := range order {
		p := byPath[key]
		sort.SliceStable(p.sessions, func(i, j int) bool {
			return p.sessions[i].ModTime.After(p.sessions[j].ModTime)
		})
		// Aggregate exactly what a project row needs to stand on its own: the
		// lifecycle counts its badges render, plus the day-scoped output rollup.
		for _, s := range p.sessions {
			p.totalMsgs += s.MsgCount
			if s.ModTime.After(p.bestTime) {
				p.bestTime = s.ModTime
			}
			if s.IsWorktree {
				p.worktrees++
			}
			if s.IsLive {
				p.liveSessions++
			}
			if s.IsCurrentWindow {
				p.hereCount++
			}
			if s.IsLive && s.ActiveMonitorCount() > 0 {
				p.monSessions++
			}
			if s.IsLive && s.AwaitingInput {
				p.inputSessions++
			}
			if openPRs, _ := s.OpenRefCounts(); openPRs > 0 {
				p.openPRs += openPRs
			}
			switch s.Lifecycle() {
			case session.LifecycleBusy:
				p.busyCount++
			case session.LifecycleBG:
				p.bgSessions++
			case session.LifecycleStuck:
				p.stuckCount++
			case session.LifecycleWait:
				p.waitCount++
			case session.LifecycleDone:
				p.doneCount++
			}
		}
		switch {
		case p.busyCount > 0:
			p.lifecycle = session.LifecycleBusy
		case p.bgSessions > 0:
			p.lifecycle = session.LifecycleBG
		case p.stuckCount > 0:
			p.lifecycle = session.LifecycleStuck
		case p.waitCount > 0:
			p.lifecycle = session.LifecycleWait
		case p.doneCount > 0:
			p.lifecycle = session.LifecycleDone
		default:
			p.lifecycle = session.LifecycleNone
		}
		rows = append(rows, *p)
	}
	// Most-recent project first, matching how the days themselves are ordered.
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].bestTime.After(rows[j].bestTime) })
	return rows
}

// dayProjectItems renders one day's project tier plus the sessions under each
// expanded project.
func dayProjectItems(g *dayItem, folded map[string]bool) []list.Item {
	projects := dayProjectRows(g)
	items := make([]list.Item, 0, len(projects)*2)
	for pi, p := range projects {
		// Fold keys are day-scoped: the same project appears under many days,
		// and one shared key would fold it everywhere at once.
		key := dayProjectFoldKey(g.dayKey, p.basePath)
		p.expanded = !folded[key]
		p.dayKey = g.dayKey
		p.treeDepth = 1
		p.treeLast = pi == len(projects)-1
		items = append(items, p)
		if !p.expanded {
			continue
		}
		for ci, ch := range p.sessions {
			items = append(items, sessionItem{
				sess:      ch,
				treeDepth: 2,
				treeLast:  ci == len(p.sessions)-1,
			})
		}
	}
	return items
}

// dayProjectFoldKey is the sessFolded key for a project row inside a day.
func dayProjectFoldKey(dayKey, basePath string) string {
	return "day:" + dayKey + "|repo:" + basePath
}

// dayLabel renders a date row's headline: "Today", "Yesterday", or a weekday +
// date. Relative labels are computed against now so the top of the list reads
// as a journal rather than as a table of ISO strings.
func dayLabel(day, now time.Time) string {
	y, m, d := now.Date()
	today := time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	switch {
	case day.Equal(today):
		return "Today"
	case day.Equal(today.AddDate(0, 0, -1)):
		return "Yesterday"
	default:
		return day.Format("Mon Jan 2")
	}
}

// dayCacheKey builds the row-cache key for a date row. The rendered relative
// label ("Today"/"Yesterday") is part of the key, not just the date: a session
// left open across midnight would otherwise keep serving a cached "Today" for
// what is now yesterday.
func (d sessionDelegate) dayCacheKey(m list.Model, index int, di dayItem, selected bool) string {
	return fmt.Sprintf("d|%d|%d|%t|%s|%t|%s|%s|%d|%d|%d|%d|%d|%d|%d|%d",
		m.Width(), index, selected, listFilterTerm(m), di.expanded, di.dayKey,
		dayLabel(di.day, time.Now()),
		len(di.sessions), di.totalMsgs, di.projects, di.liveCount,
		di.prs, di.jiras, di.artifacts, di.plans)
}

// renderDay draws a date row: calendar-style folder + relative day label +
// what that day produced. Always 2 rows tall so cursor math stays consistent
// with sessionItem and projectItem rendering.
func (d sessionDelegate) renderDay(w io.Writer, m list.Model, index int, di dayItem) {
	selected := index == m.Index()
	cacheKey := d.dayCacheKey(m, index, di, selected)
	if cached, ok := d.rowCache.Get(cacheKey); ok {
		fmt.Fprint(w, cached)
		return
	}
	width := m.Width()
	clamp := lipgloss.NewStyle().MaxWidth(width)

	cursor := "  "
	if selected {
		cursor = "> "
	}
	folderIcon := iconFolder
	if di.expanded {
		folderIcon = iconFolderOpen
	}

	nameStyle := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	metaStyle := dimStyle
	if selected {
		nameStyle = nameStyle.Foreground(colorPurple)
		metaStyle = lipgloss.NewStyle().Foreground(colorHelp)
	}

	dot := "  "
	if di.liveCount > 0 {
		dot = liveDotStyle.Render(iconStatusDot) + " "
	}

	label := nameStyle.Render(dayLabel(di.day, time.Now()))
	// The ISO date disambiguates "Today"/"Yesterday" and dates older than a
	// week that render as "Mon Jan 2" without a year. It is the first thing to
	// go under width pressure — the label already names the day.
	head := fmt.Sprintf("%s%s%s %s", cursor, dot, dimStyle.Render(folderIcon), label)
	if iso := metaStyle.Render(" " + di.dayKey); lipgloss.Width(head)+lipgloss.Width(iso) <= width-12 {
		head += iso
	}
	budget := width - lipgloss.Width(head) - 2

	badges := ""
	badgesW := 0
	// Output rollup badges — the point of the daily view is what came out of
	// the day, so PR/Jira/artifact/plan counts lead over session mechanics.
	// They are appended most-important-first and dropped wholesale once the row
	// runs out of width: a half-rendered badge reads as data corruption, and a
	// mid-word truncation of "[PLAN×2]" is worse than showing nothing.
	appendIfFits := func(style lipgloss.Style, text string) {
		if badgesW+lipgloss.Width(text)+1 > budget {
			return
		}
		badges = appendBadge(badges, &badgesW, style, text)
	}
	if di.prs > 0 && !d.hiddenBadges["PR"] {
		appendIfFits(prBadgeStyle, badgeLabel(iconBadgePR, fmt.Sprintf("PR×%d", di.prs)))
	}
	if di.jiras > 0 {
		appendIfFits(jiraBadgeStyle, fmt.Sprintf("[JIRA×%d]", di.jiras))
	}
	if di.artifacts > 0 {
		appendIfFits(artifactBadgeStyle, fmt.Sprintf("[ART×%d]", di.artifacts))
	}
	if di.plans > 0 {
		appendIfFits(planBadge, fmt.Sprintf("[PLAN×%d]", di.plans))
	}

	line1 := head + "  " + badges

	summary := " " + plural(len(di.sessions), "session")
	if di.projects > 0 {
		summary += ", " + plural(di.projects, "project")
	}
	if di.totalMsgs > 0 {
		summary += fmt.Sprintf(", %dm", di.totalMsgs)
	}
	line2 := "        " + metaStyle.Render(summary)

	if selected {
		line1 = padSelectedRow(line1, width)
		line2 = padSelectedRow(line2, width)
	}
	_ = badgesW // date rows use loose columns, same as project rows

	rendered := fmt.Sprintf("%s\n%s", clamp.Render(line1), clamp.Render(line2))
	d.rowCache.Set(cacheKey, rendered)
	fmt.Fprint(w, rendered)
}

// padSelectedRow pads a row to the full list width and applies the selected-row
// background so the highlight spans the pane.
func padSelectedRow(line string, width int) string {
	if bare := lipgloss.Width(line); bare < width {
		line += strings.Repeat(" ", width-bare)
	}
	return selectedRowStyle.Render(line)
}

// plural renders "1 session" / "2 sessions" for the day summary's counts.
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// toggleDailyView flips the browser between the daily view and whatever
// grouping the user was in before. Being able to ask "what came out today?"
// mid-read — and get back to where you were — is the point; routing that
// through the command palette would make it a mode switch instead of a glance.
//
// The two views keep separate preview modes: the daily view is about results,
// the project browser about sessions, and forcing one preview choice on both
// means every swap lands on the wrong pane. What IS shared is the cursor —
// rebuildSessionList re-anchors it on the same session, so flipping the axis
// re-sorts what is on screen instead of jumping somewhere else.
func (a *App) toggleDailyView() tea.Cmd {
	// Captured before the mode flips: the anchor is read off the row the cursor
	// is on right now, in the grouping it currently lives in.
	anchor := a.sessionListAnchor()
	if a.sessGroupMode == groupDaily {
		a.dailyPreviewMode = a.sessPreviewMode
		a.sessGroupMode = a.preDailyGroupMode
		a.sessPreviewMode = a.browserPreviewMode
		a.copiedMsg = "Grouping: " + groupModeString(a.sessGroupMode)
	} else {
		a.browserPreviewMode = a.sessPreviewMode
		a.preDailyGroupMode = a.sessGroupMode
		a.sessGroupMode = groupDaily
		a.sessPreviewMode = a.dailyPreviewMode
		a.copiedMsg = "Daily view"
	}
	a.revealAnchor(anchor, a.sessGroupMode)
	a.closePaneProxy()
	a.rebuildSessionList()
	a.sessSplit.CacheKey = ""
	return a.updateSessionPreview()
}

// cursorAnchor is what the cursor was sitting on, expressed in terms that
// survive a regrouping: an aggregate row's own identity (date, project path)
// plus the session under the cursor as a fallback.
//
// Aggregate rows are anchored on their identity rather than on their
// representative session because the rep is the group's newest session and the
// live tick keeps minting newer ones — matching on it would lose the row every
// few seconds on an active day.
type cursorAnchor struct {
	sessionID string
	// isParentRow: the cursor was on a day/project row, not a session. The
	// restore prefers landing on the same *kind* of row: dropping a project-head
	// cursor onto one of that project's children reads as "it lost my place"
	// even when the session matches.
	isParentRow bool
	isDayRow    bool
	projectPath string
	dayKey      string
}

func (c cursorAnchor) empty() bool { return c.sessionID == "" && !c.isParentRow }

// sessionListAnchor captures the cursor's row identity from the live list.
func (a *App) sessionListAnchor() cursorAnchor {
	var c cursorAnchor
	if sess, ok := a.selectedSession(); ok {
		c.sessionID = sess.ID
	}
	switch v := a.sessionList.SelectedItem().(type) {
	case projectItem:
		c.isParentRow, c.projectPath, c.dayKey = true, v.basePath, v.dayKey
	case dayItem:
		c.isParentRow, c.isDayRow, c.dayKey = true, true, v.dayKey
	}
	return c
}

// findIn returns the index of the anchor's row in items, or -1. Both the
// post-rebuild restore and the pre-rebuild auto-expand run through this so the
// row we unfold for and the row the cursor lands on cannot drift apart.
func (c cursorAnchor) findIn(items []list.Item) int {
	if c.isParentRow {
		for i, item := range items {
			switch v := item.(type) {
			case dayItem:
				if c.isDayRow && v.dayKey == c.dayKey {
					return i
				}
			case projectItem:
				// Same project, whichever grouping it now lives in. Matching on
				// the path (not the representative session) keeps the cursor on
				// the project even when the two views pick different reps —
				// the browser's rep spans all days, the daily one just today's.
				// A blank dayKey on either side means one of the two rows is a
				// flat browser row, where a project appears exactly once and the
				// path is the whole identity.
				if !c.isDayRow && v.basePath == c.projectPath &&
					(c.dayKey == "" || v.dayKey == "" || v.dayKey == c.dayKey) {
					return i
				}
			}
		}
	}
	if c.sessionID == "" {
		return -1
	}
	fallback := -1
	for i, item := range items {
		switch v := item.(type) {
		case projectItem:
			if c.isParentRow && len(v.sessions) > 0 && v.sessions[0].ID == c.sessionID {
				return i
			}
		case dayItem:
			if c.isParentRow && len(v.sessions) > 0 && v.sessions[0].ID == c.sessionID {
				return i
			}
		case sessionItem:
			if v.sess.ID == c.sessionID {
				if !c.isParentRow {
					return i
				}
				if fallback < 0 {
					fallback = i
				}
			}
		}
	}
	return fallback
}

// rowDepth is a row's nesting level in the built item list: 0 for a top-level
// row, 1 for its children, 2 for a session under a day's project. -1 marks a
// row that is not part of the tree (a section header).
func rowDepth(item list.Item) int {
	switch v := item.(type) {
	case dayItem:
		return 0
	case projectItem:
		return v.treeDepth
	case sessionItem:
		return v.treeDepth
	}
	return -1
}

// rowFoldKey is the sessFolded key that hides a row's children, or "" when the
// row folds nothing (a session with no group, a project row in a flat mode).
func rowFoldKey(item list.Item) string {
	switch v := item.(type) {
	case dayItem:
		return dayFoldKey(v.dayKey)
	case projectItem:
		return projectFoldKey(v)
	case sessionItem:
		return v.groupKey
	}
	return ""
}

// revealAnchor unfolds exactly the ancestors that would hide the anchor's row
// in destMode, so a regrouping cannot strand the cursor.
//
// Each grouping nests differently and keys its folds differently (day: →
// day:|repo: in the daily view, repo: in the project browser, proj:/team:/
// chain:/fork: elsewhere), so rather than re-deriving those key shapes by hand
// per mode, build the destination once fully expanded, locate the target row,
// and walk back up its actual parents. Whatever the builders nest, the walk
// follows — a new grouping mode gets this for free.
//
// Only the anchor's own ancestor chain is cleared. Expanding everything would
// be the easy fix and the wrong one: folding is how a 250-session day stays
// readable, and the fold map is persisted (capturePreferences →
// folded_groups), so a blanket expand would follow the user into the next
// launch. Clearing the ancestors is likewise persisted, but that matches what
// is on screen after the swap — re-folding them behind the cursor would hide
// the row we just went to the trouble of revealing.
//
// Deliberately called from the view swap rather than from rebuildSessionList:
// the live tick rebuilds every few seconds, and `f` (fold all) rebuilds right
// after collapsing everything — auto-expanding there would undo the user's
// fold the instant they pressed the key.
func (a *App) revealAnchor(anchor cursorAnchor, destMode int) {
	if len(a.sessFolded) == 0 || anchor.empty() {
		return
	}
	// nil fold map: the destination as it would look with nothing collapsed, so
	// rows hidden by the user's folds are still there to be found.
	expanded := buildGroupedItems(a.sessions, destMode, nil, a.config.WorktreeDir)
	idx := anchor.findIn(expanded)
	if idx < 0 {
		return
	}
	depth := rowDepth(expanded[idx])
	for i := idx - 1; i >= 0 && depth > 0; i-- {
		d := rowDepth(expanded[i])
		if d < 0 {
			// A section header — every ancestor of this row lies below it.
			break
		}
		if d >= depth {
			continue
		}
		depth = d
		if key := rowFoldKey(expanded[i]); key != "" {
			delete(a.sessFolded, key)
		}
	}
}

// selectedOwnsDayPane reports whether the row under the cursor renders the
// day-outputs pane: a date row, or a project row nested inside one. Both
// aggregate a scope rather than being a session, so both drive that pane.
func (a *App) selectedOwnsDayPane() bool {
	switch v := a.sessionList.SelectedItem().(type) {
	case dayItem:
		return true
	case projectItem:
		return v.dayKey != ""
	}
	return false
}
