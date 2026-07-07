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
	ansi "github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
	"github.com/sendbird/ccx/internal/session"
)

// Group mode constants
const (
	groupFlat           = 0
	groupProject        = 1
	groupTree           = 2
	groupChain          = 3
	groupFork           = 4
	groupBaseProject    = 5
	groupProjectCentric = 6 // project rows are first-class, sessions are children
	numGroupModes       = 7
)

// buildGroupedItems returns list items for the given group mode.
//
// folded contains group keys whose children should be hidden. Builders set
// sessionItem.groupKey/groupChildren on parent rows so the renderer can show
// a fold chevron and so handlers can find which key to toggle.
func buildGroupedItems(sessions []session.Session, groupMode int, folded map[string]bool, worktreeDir ...string) []list.Item {
	currentSessions, rest := splitCurrentWindow(sessions)

	buildForMode := func(ss []session.Session) []list.Item {
		switch groupMode {
		case groupProject:
			return buildProjectGroupItems(ss, folded)
		case groupTree:
			return buildTreeItems(ss, folded)
		case groupChain:
			return buildChainGroupItems(ss, folded)
		case groupFork:
			return buildForkGroupItems(ss, folded)
		case groupBaseProject:
			return buildBaseProjectGroupItems(ss, folded, worktreeDir...)
		case groupProjectCentric:
			return buildProjectCentricItems(ss, folded, worktreeDir...)
		default:
			items := make([]list.Item, len(ss))
			for i, s := range ss {
				items[i] = sessionItem{sess: s}
			}
			return items
		}
	}

	currentItems := buildForMode(currentSessions)
	restItems := buildForMode(rest)

	if len(currentItems) == 0 {
		return restItems
	}

	items := make([]list.Item, 0, len(currentItems)+len(restItems)+2)
	currentLabel := "Current Window"
	restLabel := "Sessions"
	if groupMode == groupProjectCentric {
		currentLabel = "Current Window Projects"
		restLabel = "Projects"
	}
	items = append(items, headerItem{label: currentLabel})
	items = append(items, currentItems...)
	if len(restItems) > 0 {
		items = append(items, headerItem{label: restLabel})
		items = append(items, restItems...)
	}
	return items
}

// splitCurrentWindow partitions sessions into those in the current tmux window
// (preserving most-recent-first order) and the rest.
func splitCurrentWindow(sessions []session.Session) (current, rest []session.Session) {
	for _, s := range sessions {
		if s.IsCurrentWindow {
			current = append(current, s)
		} else {
			rest = append(rest, s)
		}
	}
	sort.Slice(current, func(i, j int) bool {
		return current[i].ModTime.After(current[j].ModTime)
	})
	return current, rest
}

// substringFilter matches items whose FilterValue contains the search term as a substring.
// Supports space-separated multi-term AND matching (e.g., "role=user bash").
func substringFilter(term string, targets []string) []list.Rank {
	terms := strings.Fields(strings.ToLower(term))
	if len(terms) == 0 {
		return nil
	}
	var ranks []list.Rank
	for i, t := range targets {
		lower := strings.ToLower(t)
		allMatch := true
		for _, tt := range terms {
			if !strings.Contains(lower, tt) {
				allMatch = false
				break
			}
		}
		if !allMatch {
			continue
		}
		firstIdx := strings.Index(lower, terms[0])
		matched := make([]int, len(terms[0]))
		for j := range len(terms[0]) {
			matched[j] = firstIdx + j
		}
		ranks = append(ranks, list.Rank{Index: i, MatchedIndexes: matched})
	}
	return ranks
}

type sessionItem struct {
	sess          session.Session
	treeDepth     int    // 0=root, 1=teammate child
	treeLast      bool   // last child in group (└─ vs ├─)
	groupKey      string // stable identity of the group this row heads (depth=0 only when group has children)
	groupChildren int    // number of children in this group (0 when not a group head)
	groupFolded   bool   // current fold state at build time, used by the renderer for chevron + count
}

func (s sessionItem) FilterValue() string {
	return session.FilterValueFor(s.sess, nil)
}

// projectItem represents a project (or base repo) row in the
// project-centric view. It is not a session — it is a folder-like row that
// can be expanded to reveal its child sessions. Selecting it does not open
// a conversation; pressing Enter/Open toggles its fold state instead.
type projectItem struct {
	basePath     string            // canonical key (resolved base repo path)
	displayName  string            // shown name (project name or base repo basename)
	branch       string            // main repo's git branch (if any)
	sessions     []session.Session // all sessions under this project (main + worktrees), sorted recent-first
	worktrees    int               // count of worktree-only sessions
	totalMsgs    int               // sum of MsgCount across sessions
	liveSessions int               // number of live sessions
	bgSessions   int               // sessions whose lifecycle is BG
	monSessions  int               // sessions with active monitor jobs
	inputSessions int              // sessions awaiting user answer (AskUserQuestion)
	openPRs       int              // sessions with at least one open PR (summed open-PR count)
	stuckCount   int               // STUCK lifecycle sessions
	waitCount    int               // WAIT lifecycle sessions
	doneCount    int               // DONE lifecycle sessions
	busyCount    int               // BUSY lifecycle sessions
	hereCount    int               // sessions in the current tmux window
	bestTime     time.Time         // most-recent ModTime in this project
	expanded     bool              // current fold state at build time
	lifecycle    session.LifecycleState
}

func (p projectItem) FilterValue() string {
	// Concatenate every child session's filter value so the project row
	// becomes visible whenever any session beneath it matches the filter.
	parts := make([]string, 0, len(p.sessions)+3)
	parts = append(parts, p.displayName, p.basePath, p.branch, "is:project")
	for _, s := range p.sessions {
		parts = append(parts, session.FilterValueFor(s, nil))
	}
	return strings.Join(parts, " ")
}

// headerSentinel is returned by headerItem.FilterValue so headers never match a
// user filter via substring search. The filter wrapper still injects them back
// in to keep section titles visible.
const headerSentinel = "\x00ccx-header\x00"

// headerItem is a non-selectable list item that renders a section divider.
type headerItem struct {
	label string
}

func (h headerItem) FilterValue() string { return headerSentinel }

// isSeparator reports whether item is a non-session decorative row.
func isSeparator(item list.Item) bool {
	_, ok := item.(headerItem)
	return ok
}

type sessionDelegate struct {
	timeW        int              // max width of time-ago column
	msgW         int              // max width of message count column
	selectedSet  map[string]bool  // shared reference to App.selectedSet
	hiddenBadges map[string]bool  // shared reference to App.hiddenBadges
	rowCache     *sessionRowCache // shared render cache for visible project/session rows
}

func (d sessionDelegate) Height() int                             { return 2 }
func (d sessionDelegate) Spacing() int                            { return 0 }
func (d sessionDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (d sessionDelegate) hiddenBadgeKey() string {
	if len(d.hiddenBadges) == 0 {
		return ""
	}
	keys := []string{"BG", "MON", "INPUT", "WAIT", "DONE", "STUCK", "PR"}
	var b strings.Builder
	for _, k := range keys {
		if d.hiddenBadges[k] {
			b.WriteString(k)
			b.WriteByte(',')
		}
	}
	return b.String()
}

func (d sessionDelegate) sessionCacheKey(m list.Model, index int, si sessionItem, selected bool) string {
	filterTerm := listFilterTerm(m)
	multi := d.selectedSet != nil && d.selectedSet[si.sess.ID]
	openPRs, openJira := si.sess.OpenRefCounts()
	return fmt.Sprintf("s|%d|%d|%t|%t|%s|%s|%s|%d|%t|%d|%d|%d|%t|%d|%d|%d|%s",
		m.Width(), index, selected, multi, filterTerm, d.hiddenBadgeKey(), si.sess.ID,
		si.groupChildren, si.groupFolded, int(si.sess.Lifecycle()), si.sess.MsgCount,
		si.sess.ActiveMonitorCount(), si.sess.AwaitingInput, openPRs, openJira, si.sess.ModTime.Unix(), si.sess.FirstPrompt)
}

func (d sessionDelegate) projectCacheKey(m list.Model, index int, pi projectItem, selected bool) string {
	return fmt.Sprintf("p|%d|%d|%t|%s|%s|%t|%d|%d|%d|%d|%d|%d|%d|%d|%d|%d|%s",
		m.Width(), index, selected, listFilterTerm(m), d.hiddenBadgeKey(), pi.expanded,
		len(pi.sessions), pi.totalMsgs, pi.liveSessions, pi.bgSessions, pi.monSessions,
		pi.inputSessions, pi.stuckCount, pi.waitCount, pi.doneCount, pi.openPRs, pi.basePath)
}

// renderProject draws a folder-style row for a project: chevron + name +
// branch + session count + lifecycle badges. Always 2 rows tall to match
// sessionItem rendering. When selected, the row is highlighted full-width.
func (d sessionDelegate) renderProject(w io.Writer, m list.Model, index int, pi projectItem) {
	selected := index == m.Index()
	cacheKey := d.projectCacheKey(m, index, pi, selected)
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
	// The folder glyph itself conveys fold state (open when expanded), so no
	// separate chevron is drawn — that pairing was visually redundant.
	folderIcon := iconFolder
	if pi.expanded {
		folderIcon = iconFolderOpen
	}

	nameStyle := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	branchStyle := dimStyle
	countStyle := lipgloss.NewStyle().Foreground(colorAccent)
	timeStyle := dimStyle
	if selected {
		nameStyle = nameStyle.Foreground(lipgloss.Color("#A78BFA"))
		branchStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))
		countStyle = countStyle.Bold(true)
		timeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))
	}

	// Badges roll up project-wide lifecycle counts. Live/busy are shown as a
	// status dot before the folder (green=live, amber=busy) rather than text
	// badges; HERE is dropped entirely.
	hide := d.hiddenBadges
	badges := ""
	badgesW := 0
	if pi.bgSessions > 0 && !hide["BG"] {
		badges = appendBadge(badges, &badgesW, bgBadgeStyle, badgeLabel(iconBadgeBg, fmt.Sprintf("BG×%d", pi.bgSessions)))
	}
	if pi.monSessions > 0 && !hide["MON"] {
		badges = appendBadge(badges, &badgesW, monBadgeStyle, badgeLabel(iconBadgeMon, fmt.Sprintf("MON×%d", pi.monSessions)))
	}
	if pi.inputSessions > 0 && !hide["INPUT"] {
		badges = appendBadge(badges, &badgesW, inputBadgeStyle, badgeLabel(iconBadgeInput, fmt.Sprintf("INPUT×%d", pi.inputSessions)))
	}
	if pi.stuckCount > 0 && !hide["STUCK"] {
		badges = appendBadge(badges, &badgesW, stuckBadgeStyle, badgeLabel(iconBadgeStuck, fmt.Sprintf("STUCK×%d", pi.stuckCount)))
	}
	if pi.waitCount > 0 && !hide["WAIT"] {
		badges = appendBadge(badges, &badgesW, waitBadgeStyle, badgeLabel(iconBadgeWait, fmt.Sprintf("WAIT×%d", pi.waitCount)))
	}
	if pi.doneCount > 0 && !hide["DONE"] {
		badges = appendBadge(badges, &badgesW, doneBadgeStyle, badgeLabel(iconBadgeDone, fmt.Sprintf("DONE×%d", pi.doneCount)))
	}
	if pi.openPRs > 0 && !hide["PR"] {
		label := "PR"
		if pi.openPRs > 1 {
			label = fmt.Sprintf("PR×%d", pi.openPRs)
		}
		badges = appendBadge(badges, &badgesW, prBadgeStyle, badgeLabel(iconBadgePR, label))
	}

	// Live/busy status dot before the folder icon (2-cell reserved column).
	projDot := "  "
	if pi.busyCount > 0 {
		projDot = busyDotStyle.Render(iconStatusDot) + " "
	} else if pi.liveSessions > 0 {
		projDot = liveDotStyle.Render(iconStatusDot) + " "
	}

	// Header text: line-art folder, name, branch, time, and badges.
	branch := ""
	if pi.branch != "" {
		branch = " (" + pi.branch + ")"
	}
	folder := dimStyle.Render(folderIcon)
	name := nameStyle.Render(pi.displayName)
	br := branchStyle.Render(branch)
	timeStr := timeStyle.Render(timeAgo(pi.bestTime))

	summary := fmt.Sprintf(" %d session", len(pi.sessions))
	if len(pi.sessions) != 1 {
		summary += "s"
	}
	if pi.worktrees > 0 {
		summary += fmt.Sprintf(", %d wt", pi.worktrees)
	}
	if pi.totalMsgs > 0 {
		summary += fmt.Sprintf(", %dm", pi.totalMsgs)
	}
	summaryStyled := branchStyle.Render(summary)

	filterTerm := listFilterTerm(m)
	if filterTerm != "" {
		highlighted := highlightSnippet(pi.displayName, filterTerm, max(width/2, 10), nameStyle)
		name = highlighted
	}

	line1 := fmt.Sprintf("%s%s%s %s%s  %s%s", cursor, projDot, folder, name, br, timeStr, badges)
	// Pad/clamp.
	if selected {
		bare := lipgloss.Width(line1)
		if bare < width {
			line1 += strings.Repeat(" ", width-bare)
		}
		line1 = selectedRowStyle.Render(line1)
	}

	line2 := "        " + summaryStyled
	if selected {
		bare := lipgloss.Width(line2)
		if bare < width {
			line2 += strings.Repeat(" ", width-bare)
		}
		line2 = selectedRowStyle.Render(line2)
	}

	_ = badgesW // currently unused in layout math; columns are loose for project rows
	rendered := fmt.Sprintf("%s\n%s", clamp.Render(line1), clamp.Render(line2))
	d.rowCache.Set(cacheKey, rendered)
	fmt.Fprint(w, rendered)
}

// renderHeader draws a section divider like "── Current Window ──".
// It always occupies 2 rows (label + blank) so cursor math stays consistent
// with sessionItem rows.
func (d sessionDelegate) renderHeader(w io.Writer, m list.Model, h headerItem) {
	width := m.Width()
	if width <= 0 {
		fmt.Fprint(w, "\n")
		return
	}
	label := " " + h.label + " "
	dashes := width - lipgloss.Width(label) - 2
	if dashes < 0 {
		dashes = 0
	}
	left := strings.Repeat("─", dashes/2)
	right := strings.Repeat("─", dashes-dashes/2)
	style := dimStyle.Bold(true)
	line1 := style.Render(left + label + right)
	clamp := lipgloss.NewStyle().MaxWidth(width)
	fmt.Fprintf(w, "%s\n%s", clamp.Render(line1), "")
}

func (d sessionDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	if h, ok := item.(headerItem); ok {
		d.renderHeader(w, m, h)
		return
	}
	if pi, ok := item.(projectItem); ok {
		d.renderProject(w, m, index, pi)
		return
	}
	si, ok := item.(sessionItem)
	if !ok {
		return
	}

	s := si.sess
	selected := index == m.Index()
	cacheKey := d.sessionCacheKey(m, index, si, selected)
	if cached, ok := d.rowCache.Get(cacheKey); ok {
		fmt.Fprint(w, cached)
		return
	}
	width := m.Width()

	// Tree connector prefix for depth>0 teammates
	treePrefix := ""
	treePrefixW := 0
	if si.treeDepth > 0 {
		connector := "├─ "
		if si.treeLast {
			connector = "└─ "
		}
		treePrefix = dimStyle.Render(connector)
		treePrefixW = 3 // "├─ " is 3 cells wide
	}

	// Fold chevron for group-head rows: open when expanded, closed when collapsed.
	// Non-group rows get a single space so columns stay aligned across rows.
	foldPrefix := " "
	foldPrefixW := 1
	if si.groupChildren > 0 && si.treeDepth == 0 {
		if si.groupFolded {
			foldPrefix = dimStyle.Render(iconFoldClosed)
		} else {
			foldPrefix = dimStyle.Render(iconFoldOpen)
		}
	}

	isMultiSelected := d.selectedSet != nil && d.selectedSet[s.ID]
	cursor := "  "
	if selected && isMultiSelected {
		cursor = selectMarkStyle.Render(iconSelect) + " "
	} else if isMultiSelected {
		cursor = selectMarkStyle.Render(iconSelect) + " "
	} else if selected {
		cursor = "> "
	}

	// Aligned columns: ID  TIME  MSG  PROJECT  [badges]
	idStyle := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	timeStyle := dimStyle
	msgStyle := lipgloss.NewStyle().Foreground(colorAccent)
	projStyle := lipgloss.NewStyle()
	branchStyle := dimStyle
	promptStyle := dimStyle
	if selected {
		idStyle = idStyle.Foreground(lipgloss.Color("#A78BFA"))
		timeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))
		msgStyle = msgStyle.Bold(true)
		projStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#E2E8F0")).Bold(true)
		branchStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))
		promptStyle = selectedStyle
	}

	// Status dot (live/busy) rendered as a fixed 2-cell column before the ID so
	// rows stay aligned whether or not a dot is present. Replaces the old
	// LIVE/BUSY/HERE text badges.
	dot := statusDot(s)
	dotPrefix := "  " // 2 cells: reserve column even when no dot
	dotPrefixW := 2
	if dot != "" {
		dotPrefix = dot + " "
	}

	idStr := idStyle.Render(s.ShortID)

	timeRaw := timeAgo(s.ModTime)
	timePad := fmt.Sprintf("%-*s", d.timeW, timeRaw)
	timeStr := timeStyle.Render(timePad)

	msgRaw := fmt.Sprintf("%dm", s.MsgCount)
	msgPad := fmt.Sprintf("%*s", d.msgW, msgRaw)
	msgStr := msgStyle.Render(msgPad)

	// Build badges first to know their width. LIVE/BUSY/HERE are no longer text
	// badges — live/busy is the status dot before the ID, and "current window"
	// is conveyed by the dot column plus the HERE-free layout.
	badges := ""
	badgesW := 0
	hide := d.hiddenBadges
	switch s.Lifecycle() {
	case session.LifecycleBG:
		if !hide["BG"] {
			badges = appendBadge(badges, &badgesW, bgBadgeStyle, badgeLabel(iconBadgeBg, "BG"))
		}
	case session.LifecycleStuck:
		if !hide["STUCK"] {
			badges = appendBadge(badges, &badgesW, stuckBadgeStyle, badgeLabel(iconBadgeStuck, "STUCK"))
		}
	case session.LifecycleWait:
		if !hide["WAIT"] {
			badges = appendBadge(badges, &badgesW, waitBadgeStyle, badgeLabel(iconBadgeWait, "WAIT"))
		}
	case session.LifecycleDone:
		if !hide["DONE"] {
			badges = appendBadge(badges, &badgesW, doneBadgeStyle, badgeLabel(iconBadgeDone, "DONE"))
		}
	}
	// Monitor badge: surfaces sessions that have at least one Monitor tool
	// invocation while the Claude process is still live. The [BG] badge
	// already signals "background work running", but doesn't distinguish a
	// Monitor (long-running watcher) from a one-off background Bash, and
	// users explicitly want to see which sessions are currently watching
	// something.
	if s.IsLive && s.HasMonitorJobs && !hide["MON"] {
		// ActiveMonitorCount is kill/stop-adjusted from the loaded ShellJobs
		// (populated for live sessions), so a count of 0 means every monitor has
		// ended — hide the badge in that case.
		if n := s.ActiveMonitorCount(); n > 0 {
			monLabel := "MON"
			if n > 1 {
				monLabel = fmt.Sprintf("MON×%d", n)
			}
			badges = appendBadge(badges, &badgesW, monBadgeStyle, badgeLabel(iconBadgeMon, monLabel))
		}
	}
	// Live session blocked on an unanswered question — the user needs to act.
	if s.IsLive && s.AwaitingInput && !hide["INPUT"] {
		badges = appendBadge(badges, &badgesW, inputBadgeStyle, badgeLabel(iconBadgeInput, "INPUT"))
	}
	// Open PR / Jira badge: surfaces unfinished external work attached to this
	// session. Only open PRs (and non-done Jira) are counted; merged/closed are
	// intentionally hidden so the badge means "still needs attention".
	if !hide["PR"] {
		openPRs, openJira := s.OpenRefCounts()
		if openPRs > 0 {
			label := "PR"
			if openPRs > 1 {
				label = fmt.Sprintf("PR×%d", openPRs)
			}
			badges = appendBadge(badges, &badgesW, prBadgeStyle, badgeLabel(iconBadgePR, label))
		}
		if openJira > 0 {
			label := "JIRA"
			if openJira > 1 {
				label = fmt.Sprintf("JIRA×%d", openJira)
			}
			badges = appendBadge(badges, &badgesW, memoryBadge, badgeLabel(iconTask, label))
		}
	}
	// Custom user badges
	for _, badge := range s.CustomBadges {
		badgeText := "[" + badge + "]"
		badges += " " + customBadgeStyle.Render(badgeText)
		badgesW += len(badgeText) + 1
	}
	if s.IsRemote {
		remoteBadge := lipgloss.NewStyle().Foreground(lipgloss.Color("#7C3AED")).Bold(true)
		badges = appendBadge(badges, &badgesW, remoteBadge, badgeLabel(iconBadgeRemote, "REMOTE"))
	}

	// Child-count badge for group head rows. When folded we always show it
	// (so the user knows what's hidden); when expanded we still surface it
	// because the children may scroll out of view in long lists.
	if si.groupChildren > 0 && si.treeDepth == 0 {
		countText := fmt.Sprintf("[+%d]", si.groupChildren)
		badges += " " + dimStyle.Bold(true).Render(countText)
		badgesW += len(countText) + 1
	}

	// Calculate available width for project column
	// cursor(2) + fold + tree + dot(2) + id(8) + 2 + time + 2 + msg + 2 + project + badges
	fixedW := 2 + foldPrefixW + treePrefixW + dotPrefixW + 8 + 2 + d.timeW + 2 + d.msgW + 2 + badgesW
	maxProjW := width - fixedW
	if maxProjW < 4 {
		maxProjW = 4
	}

	// For tree children, show teammate name, fork indicator, slug, or branch
	projName := s.ProjectName
	if si.treeDepth > 0 && s.TeammateName != "" {
		projName = s.TeammateName
	} else if si.treeDepth > 0 && s.ParentSessionID != "" {
		projName = "fork:" + s.ParentSessionID[:8]
	} else if si.treeDepth > 0 && s.PlanSlug != "" {
		projName = s.PlanSlug
	} else if si.treeDepth > 0 && s.GitBranch != "" {
		projName = s.GitBranch
	}
	branch := ""
	if s.GitBranch != "" && si.treeDepth == 0 {
		branch = " (" + s.GitBranch + ")"
	}
	fullProj := projName + branch
	filterTerm := listFilterTerm(m)

	// Style teammate names in cyan, fork children in amber
	if si.treeDepth > 0 && s.TeammateName != "" {
		projStyle = teamBadge
		if selected {
			projStyle = teamBadge.Bold(true)
		}
	} else if si.treeDepth > 0 && s.ParentSessionID != "" {
		projStyle = forkBadge
		if selected {
			projStyle = forkBadge.Bold(true)
		}
	}

	// Truncate project to fit, ensuring badges are never clipped
	project := ""
	if filterTerm != "" && maxProjW > 0 {
		project = highlightSnippet(fullProj, filterTerm, maxProjW, projStyle)
	} else if len(fullProj) > maxProjW {
		trunc := fullProj[:maxProjW-3] + "..."
		project = projStyle.Render(trunc)
	} else {
		project = projStyle.Render(projName)
		if branch != "" {
			project += branchStyle.Render(branch)
		}
	}

	line1 := fmt.Sprintf("%s%s%s%s%s  %s  %s  %s%s", cursor, foldPrefix, treePrefix, dotPrefix, idStr, timeStr, msgStr, project, badges)

	prompt := s.FirstPrompt
	maxW := width - 6 - treePrefixW - foldPrefixW - dotPrefixW
	promptIndent := "    " + strings.Repeat(" ", treePrefixW+foldPrefixW+dotPrefixW)
	var line2 string
	if filterTerm != "" && maxW > 0 {
		line2 = promptIndent + highlightSnippet(prompt, filterTerm, maxW, promptStyle)
	} else {
		if maxW > 0 && len(prompt) > maxW {
			prompt = prompt[:maxW-3] + "..."
		}
		line2 = promptIndent + promptStyle.Render(prompt)
	}

	if selected {
		// Pad lines to full width for background highlight
		l1Bare := lipgloss.Width(line1)
		if l1Bare < width {
			line1 += strings.Repeat(" ", width-l1Bare)
		}
		l2Bare := lipgloss.Width(line2)
		if l2Bare < width {
			line2 += strings.Repeat(" ", width-l2Bare)
		}
		line1 = selectedRowStyle.Render(line1)
		line2 = selectedRowStyle.Render(line2)
	}

	clamp := lipgloss.NewStyle().MaxWidth(width)
	rendered := fmt.Sprintf("%s\n%s", clamp.Render(line1), clamp.Render(line2))
	d.rowCache.Set(cacheKey, rendered)
	fmt.Fprint(w, rendered)
}

func computeSessionColWidths(sessions []session.Session) (timeW, msgW int) {
	for _, s := range sessions {
		if tw := len(timeAgo(s.ModTime)); tw > timeW {
			timeW = tw
		}
		if mw := len(fmt.Sprintf("%dm", s.MsgCount)); mw > msgW {
			msgW = mw
		}
	}
	return
}

func newSessionList(sessions []session.Session, width, height int, groupMode int, selectedSet map[string]bool, hiddenBadges map[string]bool, folded map[string]bool, rowCache *sessionRowCache, worktreeDir ...string) list.Model {
	items := buildGroupedItems(sessions, groupMode, folded, worktreeDir...)

	timeW, msgW := computeSessionColWidths(sessions)

	l := list.New(items, sessionDelegate{timeW: timeW, msgW: msgW, selectedSet: selectedSet, hiddenBadges: hiddenBadges, rowCache: rowCache}, width, height)
	initListBase(&l)
	l.SetFilteringEnabled(true)

	// Use chain-aware filter for grouped modes so children stay visible
	// when their parent matches (and vice versa).
	var base list.FilterFunc
	if groupMode == groupChain || groupMode == groupFork || groupMode == groupTree || groupMode == groupBaseProject || groupMode == groupProjectCentric {
		base = buildChainAwareFilter(items)
	} else {
		base = substringFilter
	}
	l.Filter = wrapPinCurrentWindow(items, base)
	configureListSearch(&l)
	l.SetSize(width, height) // re-compute pagination after hiding bars
	return l
}

// wrapPinCurrentWindow ensures that current-window sessions and section
// headers are always included in filter results, regardless of the search
// term. Matched items keep their highlight; pinned items are returned with
// no MatchedIndexes so they render normally.
func wrapPinCurrentWindow(items []list.Item, base list.FilterFunc) list.FilterFunc {
	pinned := make(map[int]bool)
	hasCurrent := false
	for i, item := range items {
		switch v := item.(type) {
		case headerItem:
			pinned[i] = true
		case sessionItem:
			if v.sess.IsCurrentWindow {
				pinned[i] = true
				hasCurrent = true
			}
		}
	}
	return func(term string, targets []string) []list.Rank {
		ranks := base(term, targets)
		if !hasCurrent && len(pinned) == 0 {
			return ranks
		}
		seen := make(map[int]list.Rank, len(ranks))
		for _, r := range ranks {
			seen[r.Index] = r
		}
		for idx := range pinned {
			if _, ok := seen[idx]; !ok {
				seen[idx] = list.Rank{Index: idx}
			}
		}
		out := make([]list.Rank, 0, len(seen))
		for i := range items {
			if r, ok := seen[i]; ok {
				out = append(out, r)
			}
		}
		// Drop any trailing/empty section headers (e.g. "Sessions" with no
		// rest items left after filtering).
		out = trimEmptyHeaders(out, items)
		return out
	}
}

// trimEmptyHeaders removes header items that have no non-header item following
// them in the rank list.
func trimEmptyHeaders(ranks []list.Rank, items []list.Item) []list.Rank {
	if len(ranks) == 0 {
		return ranks
	}
	keep := make([]bool, len(ranks))
	hasNonHeaderAfter := false
	for i := len(ranks) - 1; i >= 0; i-- {
		if _, isHeader := items[ranks[i].Index].(headerItem); isHeader {
			keep[i] = hasNonHeaderAfter
		} else {
			keep[i] = true
			hasNonHeaderAfter = true
		}
	}
	out := ranks[:0]
	for i, r := range ranks {
		if keep[i] {
			out = append(out, r)
		}
	}
	return out
}

// buildChainAwareFilter returns a filter function that preserves parent-child
// relationships. When a depth=0 parent matches, all its depth=1 children stay
// visible. When a depth=1 child matches, its parent also stays visible.
func buildChainAwareFilter(items []list.Item) list.FilterFunc {
	// Pre-compute parent-child relationships.
	parentOf := make(map[int]int)     // child index → parent index
	childrenOf := make(map[int][]int) // parent index → child indices
	lastParent := -1
	for i, item := range items {
		switch v := item.(type) {
		case projectItem:
			lastParent = i
		case sessionItem:
			if v.treeDepth == 0 {
				lastParent = i
			} else if lastParent >= 0 {
				parentOf[i] = lastParent
				childrenOf[lastParent] = append(childrenOf[lastParent], i)
			}
		}
	}

	return func(term string, targets []string) []list.Rank {
		// Run normal substring match on all items.
		baseRanks := substringFilter(term, targets)
		if len(baseRanks) == 0 {
			return baseRanks
		}

		matchSet := make(map[int]list.Rank, len(baseRanks))
		for _, r := range baseRanks {
			matchSet[r.Index] = r
		}

		// Expand: parent match includes children; child match includes parent.
		expanded := make(map[int]bool)
		for idx := range matchSet {
			expanded[idx] = true
			// If this is a parent, include all children
			for _, childIdx := range childrenOf[idx] {
				expanded[childIdx] = true
			}
			// If this is a child, include parent
			if pIdx, ok := parentOf[idx]; ok {
				expanded[pIdx] = true
			}
		}

		// Build result preserving original order.
		var result []list.Rank
		for i := range items {
			if !expanded[i] {
				continue
			}
			if r, ok := matchSet[i]; ok {
				result = append(result, r)
			} else {
				// Included by relationship, no highlight
				result = append(result, list.Rank{Index: i})
			}
		}
		return result
	}
}

// buildTreeItems groups sessions by team, placing leaders at depth=0 and
// teammates at depth=1, interleaved with standalone sessions by recency.
func buildTreeItems(sessions []session.Session, folded map[string]bool) []list.Item {
	type teamGroup struct {
		projectPath string
		teamName    string
		leader      *session.Session
		teammates   []session.Session
		bestTime    time.Time // most recent ModTime in group
	}

	// key: "projectPath\x00teamName"
	groups := make(map[string]*teamGroup)
	var standalone []session.Session

	for i := range sessions {
		s := &sessions[i]
		if s.TeamName == "" {
			standalone = append(standalone, *s)
			continue
		}
		key := s.ProjectPath + "\x00" + s.TeamName
		g, ok := groups[key]
		if !ok {
			g = &teamGroup{projectPath: s.ProjectPath, teamName: s.TeamName}
			groups[key] = g
		}
		if s.TeamRole == "leader" {
			g.leader = s
		} else {
			g.teammates = append(g.teammates, *s)
		}
		if s.ModTime.After(g.bestTime) {
			g.bestTime = s.ModTime
		}
	}

	// Sort teammates within each group by Created time
	for _, g := range groups {
		sort.Slice(g.teammates, func(i, j int) bool {
			return g.teammates[i].Created.Before(g.teammates[j].Created)
		})
	}

	// Collect all groups into a slice and sort by bestTime descending
	groupList := make([]*teamGroup, 0, len(groups))
	for _, g := range groups {
		groupList = append(groupList, g)
	}
	sort.Slice(groupList, func(i, j int) bool {
		return groupList[i].bestTime.After(groupList[j].bestTime)
	})

	// Sort standalone by ModTime descending
	sort.Slice(standalone, func(i, j int) bool {
		return standalone[i].ModTime.After(standalone[j].ModTime)
	})

	// Merge groups and standalone items by their representative time
	var items []list.Item
	gi, si := 0, 0
	for gi < len(groupList) || si < len(standalone) {
		useGroup := false
		if gi < len(groupList) && si < len(standalone) {
			useGroup = groupList[gi].bestTime.After(standalone[si].ModTime)
		} else {
			useGroup = gi < len(groupList)
		}

		if useGroup {
			g := groupList[gi]
			gi++
			groupKey := "team:" + g.projectPath + "\x00" + g.teamName
			// Leader (or first teammate as header if no leader)
			var header session.Session
			if g.leader != nil {
				header = *g.leader
			} else if len(g.teammates) > 0 {
				header = g.teammates[0]
				g.teammates = g.teammates[1:]
			}
			children := g.teammates
			isFolded := folded[groupKey] && len(children) > 0
			items = append(items, sessionItem{
				sess:          header,
				treeDepth:     0,
				groupKey:      groupKey,
				groupChildren: len(children),
				groupFolded:   isFolded,
			})
			if !isFolded {
				for ti, tm := range children {
					items = append(items, sessionItem{
						sess:      tm,
						treeDepth: 1,
						treeLast:  ti == len(children)-1,
					})
				}
			}
		} else {
			items = append(items, sessionItem{sess: standalone[si], treeDepth: 0})
			si++
		}
	}

	return items
}

// buildChainGroupItems groups sessions that share the same PlanSlug (continuation
// chain). The earliest session in each chain becomes the depth=0 header; later
// sessions are depth=1 children sorted by Created time.
func buildChainGroupItems(sessions []session.Session, folded map[string]bool) []list.Item {
	type chainGroup struct {
		slug     string
		sessions []session.Session
		bestTime time.Time
	}

	groups := make(map[string]*chainGroup)
	var noSlug []session.Session

	for i := range sessions {
		s := &sessions[i]
		if s.PlanSlug == "" {
			noSlug = append(noSlug, *s)
			continue
		}
		g, ok := groups[s.PlanSlug]
		if !ok {
			g = &chainGroup{slug: s.PlanSlug}
			groups[s.PlanSlug] = g
		}
		g.sessions = append(g.sessions, *s)
		if s.ModTime.After(g.bestTime) {
			g.bestTime = s.ModTime
		}
	}

	// Sort each chain by Created time (earliest first)
	for _, g := range groups {
		sort.Slice(g.sessions, func(i, j int) bool {
			return g.sessions[i].Created.Before(g.sessions[j].Created)
		})
	}

	// Separate chains (2+ sessions) from singletons
	var chainList []*chainGroup
	var standalone []session.Session
	for _, g := range groups {
		if len(g.sessions) <= 1 {
			standalone = append(standalone, g.sessions...)
			continue
		}
		chainList = append(chainList, g)
	}
	standalone = append(standalone, noSlug...)

	// Sort chains by bestTime desc, standalone by ModTime desc
	sort.Slice(chainList, func(i, j int) bool {
		return chainList[i].bestTime.After(chainList[j].bestTime)
	})
	sort.Slice(standalone, func(i, j int) bool {
		return standalone[i].ModTime.After(standalone[j].ModTime)
	})

	// Merge by recency
	var items []list.Item
	ci, si := 0, 0
	for ci < len(chainList) || si < len(standalone) {
		useChain := false
		if ci < len(chainList) && si < len(standalone) {
			useChain = chainList[ci].bestTime.After(standalone[si].ModTime)
		} else {
			useChain = ci < len(chainList)
		}
		if useChain {
			g := chainList[ci]
			ci++
			groupKey := "chain:" + g.slug
			children := g.sessions[1:]
			isFolded := folded[groupKey] && len(children) > 0
			// Earliest session is the header
			items = append(items, sessionItem{
				sess:          g.sessions[0],
				treeDepth:     0,
				groupKey:      groupKey,
				groupChildren: len(children),
				groupFolded:   isFolded,
			})
			if isFolded {
				continue
			}
			for idx, ch := range children {
				items = append(items, sessionItem{
					sess:      ch,
					treeDepth: 1,
					treeLast:  idx == len(children)-1,
				})
			}
		} else {
			items = append(items, sessionItem{sess: standalone[si], treeDepth: 0})
			si++
		}
	}
	return items
}

// buildForkGroupItems groups forked sessions under their parent session.
// Only ParentSessionID relationships are used — sessions without fork
// relationships appear standalone (flat).
func buildForkGroupItems(sessions []session.Session, folded map[string]bool) []list.Item {
	type forkGroup struct {
		sessions []session.Session
		bestTime time.Time
	}

	byID := make(map[string]*session.Session, len(sessions))
	for i := range sessions {
		byID[sessions[i].ID] = &sessions[i]
	}

	// Walk fork parent chain to find root ancestor in our session list
	rootOf := func(s *session.Session) string {
		cur := s
		seen := map[string]bool{cur.ID: true}
		for cur.ParentSessionID != "" {
			parent, ok := byID[cur.ParentSessionID]
			if !ok || seen[parent.ID] {
				break
			}
			seen[parent.ID] = true
			cur = parent
		}
		return cur.ID
	}

	groups := make(map[string]*forkGroup)
	assigned := make(map[string]bool)

	for i := range sessions {
		s := &sessions[i]
		if s.ParentSessionID == "" {
			continue
		}
		rootID := rootOf(s)
		g, ok := groups[rootID]
		if !ok {
			g = &forkGroup{}
			groups[rootID] = g
			// Include the root session itself
			if root, exists := byID[rootID]; exists && !assigned[rootID] {
				g.sessions = append(g.sessions, *root)
				assigned[rootID] = true
				if root.ModTime.After(g.bestTime) {
					g.bestTime = root.ModTime
				}
			}
		}
		if !assigned[s.ID] {
			g.sessions = append(g.sessions, *s)
			assigned[s.ID] = true
			if s.ModTime.After(g.bestTime) {
				g.bestTime = s.ModTime
			}
		}
	}

	// Sort each group by Created time (earliest first)
	for _, g := range groups {
		sort.Slice(g.sessions, func(i, j int) bool {
			return g.sessions[i].Created.Before(g.sessions[j].Created)
		})
	}

	var forkList []*forkGroup
	var standalone []session.Session
	for _, g := range groups {
		if len(g.sessions) <= 1 {
			standalone = append(standalone, g.sessions...)
			continue
		}
		forkList = append(forkList, g)
	}
	for i := range sessions {
		if !assigned[sessions[i].ID] {
			standalone = append(standalone, sessions[i])
		}
	}

	sort.Slice(forkList, func(i, j int) bool {
		return forkList[i].bestTime.After(forkList[j].bestTime)
	})
	sort.Slice(standalone, func(i, j int) bool {
		return standalone[i].ModTime.After(standalone[j].ModTime)
	})

	// Merge by recency
	var items []list.Item
	fi, si := 0, 0
	for fi < len(forkList) || si < len(standalone) {
		useFork := false
		if fi < len(forkList) && si < len(standalone) {
			useFork = forkList[fi].bestTime.After(standalone[si].ModTime)
		} else {
			useFork = fi < len(forkList)
		}
		if useFork {
			g := forkList[fi]
			fi++
			rootID := ""
			if len(g.sessions) > 0 {
				rootID = g.sessions[0].ID
			}
			groupKey := "fork:" + rootID
			children := g.sessions[1:]
			isFolded := folded[groupKey] && len(children) > 0
			items = append(items, sessionItem{
				sess:          g.sessions[0],
				treeDepth:     0,
				groupKey:      groupKey,
				groupChildren: len(children),
				groupFolded:   isFolded,
			})
			if isFolded {
				continue
			}
			for idx, ch := range children {
				items = append(items, sessionItem{
					sess:      ch,
					treeDepth: 1,
					treeLast:  idx == len(children)-1,
				})
			}
		} else {
			items = append(items, sessionItem{sess: standalone[si], treeDepth: 0})
			si++
		}
	}
	return items
}

// buildProjectGroupItems groups sessions by ProjectPath. The most recent
// session in each project becomes the depth=0 header; the rest are depth=1
// children showing branch name instead of project.
func buildProjectGroupItems(sessions []session.Session, folded map[string]bool) []list.Item {
	type projGroup struct {
		projectPath string
		sessions    []session.Session
		bestTime    time.Time
	}

	groups := make(map[string]*projGroup)
	for i := range sessions {
		s := &sessions[i]
		key := s.ProjectPath
		g, ok := groups[key]
		if !ok {
			g = &projGroup{projectPath: key}
			groups[key] = g
		}
		g.sessions = append(g.sessions, *s)
		if s.ModTime.After(g.bestTime) {
			g.bestTime = s.ModTime
		}
	}

	// Sort each group by ModTime desc
	for _, g := range groups {
		sort.Slice(g.sessions, func(i, j int) bool {
			return g.sessions[i].ModTime.After(g.sessions[j].ModTime)
		})
	}

	// Sort groups by bestTime desc
	groupList := make([]*projGroup, 0, len(groups))
	for _, g := range groups {
		groupList = append(groupList, g)
	}
	sort.Slice(groupList, func(i, j int) bool {
		return groupList[i].bestTime.After(groupList[j].bestTime)
	})

	var items []list.Item
	for _, g := range groupList {
		if len(g.sessions) == 1 {
			// Single session in project — no tree nesting
			items = append(items, sessionItem{sess: g.sessions[0], treeDepth: 0})
			continue
		}
		groupKey := "proj:" + g.projectPath
		children := g.sessions[1:]
		isFolded := folded[groupKey]
		// First (most recent) session is the header
		items = append(items, sessionItem{
			sess:          g.sessions[0],
			treeDepth:     0,
			groupKey:      groupKey,
			groupChildren: len(children),
			groupFolded:   isFolded,
		})
		if isFolded {
			continue
		}
		for ci, ch := range children {
			items = append(items, sessionItem{
				sess:      ch,
				treeDepth: 1,
				treeLast:  ci == len(children)-1,
			})
		}
	}

	return items
}

// buildBaseProjectGroupItems groups sessions by base repository, resolving
// worktrees to their main repo using git info (.git file) or path patterns.
// Sessions from the same base repo (main + worktrees) appear under one group.
func buildBaseProjectGroupItems(sessions []session.Session, folded map[string]bool, worktreeDirs ...string) []list.Item {
	type baseGroup struct {
		basePath string
		sessions []session.Session
		bestTime time.Time
	}

	groups := make(map[string]*baseGroup)
	for i := range sessions {
		s := &sessions[i]
		basePath := session.ResolveBaseRepo(s.ProjectPath, worktreeDirs...)
		g, ok := groups[basePath]
		if !ok {
			g = &baseGroup{basePath: basePath}
			groups[basePath] = g
		}
		g.sessions = append(g.sessions, *s)
		if s.ModTime.After(g.bestTime) {
			g.bestTime = s.ModTime
		}
	}

	// Sort each group: main-repo sessions first, then worktrees, both by ModTime desc
	for _, g := range groups {
		sort.Slice(g.sessions, func(i, j int) bool {
			iIsWT := g.sessions[i].IsWorktree || g.sessions[i].ProjectPath != g.basePath
			jIsWT := g.sessions[j].IsWorktree || g.sessions[j].ProjectPath != g.basePath
			if iIsWT != jIsWT {
				return !iIsWT // main repo sessions first
			}
			return g.sessions[i].ModTime.After(g.sessions[j].ModTime)
		})
	}

	// Sort groups by bestTime desc
	groupList := make([]*baseGroup, 0, len(groups))
	for _, g := range groups {
		groupList = append(groupList, g)
	}
	sort.Slice(groupList, func(i, j int) bool {
		return groupList[i].bestTime.After(groupList[j].bestTime)
	})

	var items []list.Item
	for _, g := range groupList {
		if len(g.sessions) == 1 {
			items = append(items, sessionItem{sess: g.sessions[0], treeDepth: 0})
			continue
		}
		groupKey := "repo:" + g.basePath
		children := g.sessions[1:]
		isFolded := folded[groupKey]
		items = append(items, sessionItem{
			sess:          g.sessions[0],
			treeDepth:     0,
			groupKey:      groupKey,
			groupChildren: len(children),
			groupFolded:   isFolded,
		})
		if isFolded {
			continue
		}
		for ci, ch := range children {
			items = append(items, sessionItem{
				sess:      ch,
				treeDepth: 1,
				treeLast:  ci == len(children)-1,
			})
		}
	}

	return items
}

// buildProjectCentricItems creates a project-centric view where each base
// repository becomes a first-class row (`projectItem`). Its child sessions
// (`sessionItem` with treeDepth=1) are shown beneath it only when the
// project is expanded — controlled by the same `folded` map used by other
// group modes, keyed by `repo:<basePath>`.
func buildProjectCentricItems(sessions []session.Session, folded map[string]bool, worktreeDirs ...string) []list.Item {
	type proj struct {
		basePath string
		name     string
		branch   string
		sessions []session.Session
		bestTime time.Time
	}

	groups := make(map[string]*proj)
	for i := range sessions {
		s := &sessions[i]
		basePath := session.ResolveBaseRepo(s.ProjectPath, worktreeDirs...)
		g, ok := groups[basePath]
		if !ok {
			g = &proj{basePath: basePath}
			groups[basePath] = g
		}
		g.sessions = append(g.sessions, *s)
		if s.ModTime.After(g.bestTime) {
			g.bestTime = s.ModTime
		}
		// First non-worktree session contributes the display name and branch.
		if g.name == "" && !s.IsWorktree && s.ProjectPath == basePath {
			g.name = s.ProjectName
			g.branch = s.GitBranch
		}
	}
	// Fallback display name: any session's ProjectName, else basename of basePath.
	for _, g := range groups {
		if g.name != "" {
			continue
		}
		for _, s := range g.sessions {
			if s.ProjectName != "" {
				g.name = s.ProjectName
				break
			}
		}
		if g.name == "" {
			g.name = filepathBase(g.basePath)
		}
	}

	// Sort each project's sessions: main-repo first (by ModTime desc), then
	// worktrees (by ModTime desc).
	for _, g := range groups {
		sort.Slice(g.sessions, func(i, j int) bool {
			iIsWT := g.sessions[i].IsWorktree || g.sessions[i].ProjectPath != g.basePath
			jIsWT := g.sessions[j].IsWorktree || g.sessions[j].ProjectPath != g.basePath
			if iIsWT != jIsWT {
				return !iIsWT
			}
			return g.sessions[i].ModTime.After(g.sessions[j].ModTime)
		})
	}

	// Sort projects by recency.
	projList := make([]*proj, 0, len(groups))
	for _, g := range groups {
		projList = append(projList, g)
	}
	sort.Slice(projList, func(i, j int) bool {
		return projList[i].bestTime.After(projList[j].bestTime)
	})

	items := make([]list.Item, 0, len(projList)*2)
	for _, g := range projList {
		key := "repo:" + g.basePath
		expanded := !folded[key]
		pi := projectItem{
			basePath:    g.basePath,
			displayName: g.name,
			branch:      g.branch,
			sessions:    g.sessions,
			bestTime:    g.bestTime,
			expanded:    expanded,
			totalMsgs:   0,
		}
		for _, s := range g.sessions {
			pi.totalMsgs += s.MsgCount
			if s.IsWorktree || s.ProjectPath != g.basePath {
				pi.worktrees++
			}
			if s.IsLive {
				pi.liveSessions++
			}
			if s.IsCurrentWindow {
				pi.hereCount++
			}
			if s.IsLive && s.ActiveMonitorCount() > 0 {
				pi.monSessions++
			}
			if s.IsLive && s.AwaitingInput {
				pi.inputSessions++
			}
			if openPRs, _ := s.OpenRefCounts(); openPRs > 0 {
				pi.openPRs += openPRs
			}
			switch s.Lifecycle() {
			case session.LifecycleBusy:
				pi.busyCount++
			case session.LifecycleBG:
				pi.bgSessions++
			case session.LifecycleStuck:
				pi.stuckCount++
			case session.LifecycleWait:
				pi.waitCount++
			case session.LifecycleDone:
				pi.doneCount++
			}
		}
		// Compute a representative lifecycle for the row's badge.
		switch {
		case pi.busyCount > 0:
			pi.lifecycle = session.LifecycleBusy
		case pi.bgSessions > 0:
			pi.lifecycle = session.LifecycleBG
		case pi.stuckCount > 0:
			pi.lifecycle = session.LifecycleStuck
		case pi.waitCount > 0:
			pi.lifecycle = session.LifecycleWait
		case pi.doneCount > 0:
			pi.lifecycle = session.LifecycleDone
		default:
			pi.lifecycle = session.LifecycleNone
		}
		items = append(items, pi)
		if !expanded {
			continue
		}
		for ci, ch := range g.sessions {
			items = append(items, sessionItem{
				sess:      ch,
				treeDepth: 1,
				treeLast:  ci == len(g.sessions)-1,
			})
		}
	}
	return items
}

// filepathBase returns the trailing path element. We avoid importing
// filepath here for this single use to keep the package's imports tidy.
func filepathBase(p string) string {
	if p == "" {
		return ""
	}
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[i+1:]
		}
	}
	return p
}

// statusDot renders the live/busy status indicator that replaces the old
// LIVE/BUSY text badges: a single ● before the session ID. Returns an empty
// string (no dot) for non-live sessions. Green = live & idle, amber =
// busy/responding.
func statusDot(s session.Session) string {
	if !s.IsLive {
		return ""
	}
	if s.Lifecycle() == session.LifecycleBusy {
		return busyDotStyle.Render(iconStatusDot)
	}
	return liveDotStyle.Render(iconStatusDot)
}

func appendBadge(badges string, badgesW *int, style lipgloss.Style, text string) string {
	badges += " " + style.Render(text)
	*badgesW += lipgloss.Width(text) + 1
	return badges
}

func timeAgo(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("Jan 02")
	}
}

type modalOptions struct {
	maxWidth  int
	maxHeight int
	paddingX  int
	paddingY  int
}

func overlayCenteredModal(bg, fg string, screenW, screenH int, opts modalOptions) string {
	if fg == "" {
		return bg
	}
	bgLines := strings.Split(bg, "\n")
	fgLines := strings.Split(fg, "\n")

	for len(bgLines) < screenH {
		bgLines = append(bgLines, "")
	}

	fgH := len(fgLines)
	fgW := 0
	for _, l := range fgLines {
		if w := lipgloss.Width(l); w > fgW {
			fgW = w
		}
	}

	padX := max(opts.paddingX, 0)
	padY := max(opts.paddingY, 0)
	outerW := fgW + padX*2
	outerH := fgH + padY*2
	if opts.maxWidth > 0 {
		outerW = min(outerW, opts.maxWidth)
	}
	if opts.maxHeight > 0 {
		outerH = min(outerH, opts.maxHeight)
	}

	startY := max((screenH-outerH)/2, 0) + padY
	startX := max((screenW-outerW)/2, 0) + padX

	for i, fgLine := range fgLines {
		bgIdx := startY + i
		if bgIdx >= len(bgLines) {
			break
		}
		bgLines[bgIdx] = overlayLine(bgLines[bgIdx], fgLine, startX, screenW)
	}

	if len(bgLines) > screenH {
		bgLines = bgLines[:screenH]
	}
	return strings.Join(bgLines, "\n")
}

// renderHelpModal renders a centered bordered modal with help content overlaid on bg.
func renderHelpModal(bg string, screenW, screenH int, km Keymap, shortcutHint string) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	d := dimStyle

	var sb strings.Builder
	sb.WriteString(titleStyle.Render(" ccx — Help") + "\n\n")

	// Badges: two-column layout
	sb.WriteString(headerStyle.Render(" Badges") + "\n")
	type badge struct {
		style lipgloss.Style
		badge string
		desc  string
	}
	allBadges := []badge{
		{liveDotStyle, iconStatusDot + " ", "Live & idle (dot before ID)"},
		{busyDotStyle, iconStatusDot + " ", "Busy / responding now"},
		{prBadgeStyle, badgeLabel(iconBadgePR, "PR"), "Open pull request(s)"},
		{memoryBadge, badgeLabel(iconTask, "JIRA"), "Open Jira issue(s)"},
		{bgBadgeStyle, badgeLabel(iconBadgeBg, "BG"), "Background shell/monitor/cron"},
		{monBadgeStyle, badgeLabel(iconBadgeMon, "MON"), "Monitor tool currently in flight"},
		{inputBadgeStyle, badgeLabel(iconBadgeInput, "INPUT"), "Awaiting your answer (AskUserQuestion)"},
		{waitBadgeStyle, badgeLabel(iconBadgeWait, "WAIT"), "Idle, waiting for user"},
		{doneBadgeStyle, badgeLabel(iconBadgeDone, "DONE"), "All work completed"},
		{stuckBadgeStyle, badgeLabel(iconBadgeStuck, "STUCK"), "Live but stale with unfinished work"},
		{lipgloss.NewStyle().Foreground(lipgloss.Color("#7C3AED")).Bold(true), badgeLabel(iconBadgeRemote, "REMOTE"), "Remote (experimental)"},
	}
	// Render badges in pairs (two per line)
	for i := 0; i < len(allBadges); i += 2 {
		b := allBadges[i]
		left := fmt.Sprintf(" %s %-16s", b.style.Render(fmt.Sprintf("%-6s", b.badge)), d.Render(b.desc))
		if i+1 < len(allBadges) {
			b2 := allBadges[i+1]
			right := fmt.Sprintf("  %s %s", b2.style.Render(fmt.Sprintf("%-6s", b2.badge)), d.Render(b2.desc))
			sb.WriteString(left + right + "\n")
		} else {
			sb.WriteString(left + "\n")
		}
	}

	// Search filters: two-column layout
	sb.WriteString("\n" + headerStyle.Render(" Search Filters") + "\n")
	type filter struct{ filter, desc string }
	allFilters := []filter{
		{"is:here", "In current window"},
		{"is:live", "Live sessions"},
		{"is:busy", "Responding now"},
		{"is:bg", "Background work in flight"},
		{"is:wait", "Idle, waiting for user"},
		{"is:done", "All work completed"},
		{"D", "Toggle completed-only"},
		{"is:stuck", "Stale, unfinished"},
		{"is:wt", "Worktree sessions"},
		{"is:team", "Team sessions"},
		{"has:mem", "With memory"},
		{"has:todo", "With todos"},
		{"has:task", "With tasks"},
		{"has:plan", "With plans"},
		{"has:agent", "With subagents"},
		{"has:compact", "With compaction"},
		{"has:skill", "With skills"},
		{"has:mcp", "With MCP tools"},
		{"is:mon", "Monitor in flight"},
		{"is:input", "Awaiting user answer"},
		{"proj:<name>", "By project name"},
		{"team:<name>", "By team name"},
		{"is:fork", "Forked sessions"},
		{"is:remote", "Remote sessions (exp)"},
	}
	for i := 0; i < len(allFilters); i += 2 {
		f := allFilters[i]
		left := fmt.Sprintf(" %-13s %s", f.filter, d.Render(fmt.Sprintf("%-17s", f.desc)))
		if i+1 < len(allFilters) {
			f2 := allFilters[i+1]
			right := fmt.Sprintf(" %-13s %s", f2.filter, d.Render(f2.desc))
			sb.WriteString(left + right + "\n")
		} else {
			sb.WriteString(left + "\n")
		}
	}

	// Keybindings: single column but concise descriptions
	sb.WriteString("\n" + headerStyle.Render(" Keybindings") + "\n")
	sk := km.Session
	keys := []struct{ key, desc string }{
		{displayKey(sk.Open) + " / " + displayKey(sk.Right), "Open / preview"},
		{displayKey(sk.Escape) + " / " + displayKey(sk.Left), "Back / close"},
		{displayKey(sk.Edit), "Edit session files"},
		{displayKey(sk.Actions), "Actions (" + displayKey(km.Actions.Delete) + "/" + displayKey(km.Actions.Move) + "/" + displayKey(km.Actions.Resume) + "/" + displayKey(km.Actions.CopyPath) + "/" + displayKey(km.Actions.Worktree) + "/" + displayKey(km.Actions.Kill) + "/" + displayKey(km.Actions.Input) + "/" + displayKey(km.Actions.Jump) + ")"},
		{displayKey(sk.Search), "Search / filter"},
		{displayKey(km.Views.Stats), "Global stats"},
		{displayKey(sk.Refresh), "Refresh list"},
		{displayKey(sk.Preview), "Cycle preview mode (conv→stats→mem→tasks/plan)"},
		{displayKey(sk.Live), "Live preview (^Q:unfocus)"},
		{displayKey(sk.Select), "Toggle multi-select"},
		{"o", "Fold/expand project group"},
		{"f / F", "Fold all / expand all groups"},
		{"D", "Toggle completed-only filter"},
		{displayKey(sk.Help), "This help"},
		{displayKey(sk.Quit), "Quit"},
	}
	for _, k := range keys {
		sb.WriteString(fmt.Sprintf(" %-12s %s\n", k.key, d.Render(k.desc)))
	}

	// Number key shortcuts for current view
	if shortcutHint != "" {
		sb.WriteString("\n" + headerStyle.Render(" Shortcuts") + "\n")
		sb.WriteString(" " + d.Render(shortcutHint) + "\n")
	}

	body := strings.TrimRight(sb.String(), "\n")
	bodyLines := strings.Split(body, "\n")

	// Modal dimensions: fit content with padding, capped to screen
	modalW := 72
	if modalW > screenW-4 {
		modalW = screenW - 4
	}
	modalH := len(bodyLines) + 2 // +2 for top/bottom border
	if modalH > screenH-2 {
		modalH = screenH - 2
		bodyLines = bodyLines[:modalH-2]
		body = strings.Join(bodyLines, "\n")
	}

	modalStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorPrimary).
		Width(modalW).
		Padding(0, 1)

	modal := modalStyle.Render(body)

	return overlayCenter(bg, modal, screenW, screenH)
}

// renderFullTextModal renders a scrollable modal showing the full text of a
// conversation entry, overlaid on bg.
// renderConfirmModal shows a centered y/n confirmation dialog.
func renderConfirmModal(bg, message string, screenW, screenH int) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
	hintStyle := lipgloss.NewStyle().Foreground(colorDim)

	body := titleStyle.Render("  "+message) + "\n\n" +
		"  " + lipgloss.NewStyle().Bold(true).Foreground(colorAccent).Render("y") + hintStyle.Render(": confirm") +
		"    " + hintStyle.Render("any other key: cancel")

	modalW := min(len(message)+10, screenW-10)
	if modalW < 30 {
		modalW = 30
	}

	modal := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorPrimary).
		Width(modalW).
		Padding(1, 1).
		Render(body)

	return overlayCenter(bg, modal, screenW, screenH)
}

func renderFullTextModal(bg, text string, scroll, screenW, screenH int) string {
	// Modal size: 80% of screen, capped
	modalW := min(screenW*4/5, screenW-6)
	if modalW < 20 {
		modalW = screenW - 4
	}
	innerW := modalW - 4 // border(2) + padding(2)

	wrapped := wrapText(text, innerW)
	lines := strings.Split(wrapped, "\n")

	// Visible height inside modal (reserve border + title)
	innerH := min(screenH*3/4, screenH-4)
	bodyH := innerH - 1 // 1 line for title

	// Clamp scroll
	maxScroll := max(len(lines)-bodyH, 0)
	if scroll > maxScroll {
		scroll = maxScroll
	}

	// Slice visible lines
	end := min(scroll+bodyH, len(lines))
	visible := lines[scroll:end]

	// Title with scroll indicator
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
	title := titleStyle.Render(" Full Text")
	if len(lines) > bodyH {
		pct := 0
		if maxScroll > 0 {
			pct = scroll * 100 / maxScroll
		}
		title += dimStyle.Render(fmt.Sprintf("  (%d%%)", pct))
	}

	body := title + "\n" + strings.Join(visible, "\n")

	modalStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorPrimary).
		Width(modalW).
		Padding(0, 1)

	modal := modalStyle.Render(body)
	return overlayCenter(bg, modal, screenW, screenH)
}

// overlayCenter places fg (the modal) centered on top of bg, preserving bg
// content outside the modal area.
func overlayCenter(bg, fg string, width, height int) string {
	bgLines := strings.Split(bg, "\n")
	fgLines := strings.Split(fg, "\n")

	// Pad bg to full height
	for len(bgLines) < height {
		bgLines = append(bgLines, "")
	}

	fgH := len(fgLines)
	fgW := 0
	for _, l := range fgLines {
		if w := lipgloss.Width(l); w > fgW {
			fgW = w
		}
	}

	// Center offsets. Pull startX left if the modal would overflow the right
	// edge, so its right border is never truncated by overlayLine. (Only when
	// the modal is genuinely wider than the screen does clamping to 0 still
	// leave an overflow, which overlayLine then trims as a last resort.)
	startY := (height - fgH) / 2
	startX := (width - fgW) / 2
	if startX < 0 {
		startX = 0
	}
	if startX+fgW > width {
		startX = max(width-fgW, 0)
	}
	if startY < 0 {
		startY = 0
	}

	for i, fgLine := range fgLines {
		bgIdx := startY + i
		if bgIdx >= len(bgLines) {
			break
		}
		bgLines[bgIdx] = overlayLine(bgLines[bgIdx], fgLine, startX, width)
	}

	// Trim to height
	if len(bgLines) > height {
		bgLines = bgLines[:height]
	}
	return strings.Join(bgLines, "\n")
}

// placeHintBox overlays hint box lines onto the bottom of content,
// preserving background content on both sides of the box.
// dividerCol is the cell-width column where the split divider sits (0 = no split).
func placeHintBox(content, hintBox string, dividerCol int) string {
	contentLines := strings.Split(content, "\n")
	boxLines := strings.Split(hintBox, "\n")
	startY := len(contentLines) - len(boxLines)
	if startY < 0 {
		startY = 0
	}
	maxW := 0
	for _, l := range contentLines {
		if w := lipgloss.Width(l); w > maxW {
			maxW = w
		}
	}
	if maxW == 0 {
		maxW = 120
	}
	for i, bl := range boxLines {
		y := startY + i
		if y < len(contentLines) {
			limit := maxW
			if dividerCol > 0 {
				limit = dividerCol
			}
			contentLines[y] = overlayLine(contentLines[y], bl, 1, limit)
		}
	}
	return strings.Join(contentLines, "\n")
}

// overlayLine replaces a portion of bgLine starting at col with fgLine,
// handling ANSI escape sequences properly. After the overlay, it restores
// the background's ANSI state so right-side cells keep their styling.
func overlayLine(bgLine, fgLine string, col, maxWidth int) string {
	bgCells := splitANSICells(bgLine)
	fgW := min(lipgloss.Width(fgLine), max(maxWidth-col, 0))
	if fgW <= 0 {
		return bgLine
	}
	fgLine = ansi.Truncate(fgLine, fgW, "")

	// Pad bg cells to reach col
	for len(bgCells) < col+fgW && len(bgCells) < maxWidth {
		bgCells = append(bgCells, " ")
	}

	// Find the last active ANSI SGR sequence at the splice point by scanning
	// bg cells that will be replaced. Track last non-reset SGR to restore.
	spliceEnd := col + fgW
	if spliceEnd > len(bgCells) {
		spliceEnd = len(bgCells)
	}
	lastSGR := "" // last SGR escape (e.g. "\x1b[38;2;...m")
	for i := 0; i < spliceEnd; i++ {
		cell := bgCells[i]
		// Extract ANSI SGR sequences from this cell
		for j := 0; j < len(cell); j++ {
			if cell[j] == '\x1b' && j+1 < len(cell) && cell[j+1] == '[' {
				// Find end of escape
				k := j + 2
				for k < len(cell) && !((cell[k] >= 'A' && cell[k] <= 'Z') || (cell[k] >= 'a' && cell[k] <= 'z')) {
					k++
				}
				if k < len(cell) && cell[k] == 'm' {
					seq := cell[j : k+1]
					if seq == "\x1b[0m" {
						lastSGR = "" // reset clears state
					} else {
						lastSGR = seq
					}
				}
				j = k
			}
		}
	}

	// Build result: bg[:col] + fg + reset + restore_bg_state + bg[col+fgW:]
	var sb strings.Builder
	for i := 0; i < col && i < len(bgCells); i++ {
		sb.WriteString(bgCells[i])
	}
	sb.WriteString(fgLine)
	sb.WriteString("\x1b[0m")
	// Restore the bg ANSI state for right-side cells that inherit styling
	if lastSGR != "" {
		sb.WriteString(lastSGR)
	}
	for i := col + fgW; i < len(bgCells); i++ {
		sb.WriteString(bgCells[i])
	}
	return sb.String()
}

// splitANSICells splits a string into per-cell chunks, each containing the
// character plus any preceding ANSI escape sequences. This allows replacing
// cells while preserving styling of surrounding content.
func splitANSICells(s string) []string {
	var cells []string
	var pending strings.Builder // accumulates ANSI escapes before next printable
	inEsc := false

	for _, r := range s {
		if r == '\x1b' {
			inEsc = true
			pending.WriteRune(r)
			continue
		}
		if inEsc {
			pending.WriteRune(r)
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || r == '~' {
				inEsc = false
			}
			continue
		}
		pending.WriteRune(r)
		cells = append(cells, pending.String())
		pending.Reset()
		// Wide characters (CJK, etc.) take 2 columns — add a padding cell
		if runewidth.RuneWidth(r) == 2 {
			cells = append(cells, "")
		}
	}
	// Trailing escapes (no printable after them) — attach to last cell or discard
	if pending.Len() > 0 {
		if len(cells) > 0 {
			cells[len(cells)-1] += pending.String()
		}
	}
	return cells
}
