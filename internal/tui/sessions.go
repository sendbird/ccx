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
	"github.com/keyolk/ccx/internal/session"
)

// Group mode constants
const (
	groupFlat    = 0
	groupProject = 1
	groupTree    = 2
)

// buildGroupedItems returns list items for the given group mode.
func buildGroupedItems(sessions []session.Session, groupMode int) []list.Item {
	switch groupMode {
	case groupProject:
		return buildProjectGroupItems(sessions)
	case groupTree:
		return buildTreeItems(sessions)
	default:
		items := make([]list.Item, len(sessions))
		for i, s := range sessions {
			items[i] = sessionItem{sess: s}
		}
		return items
	}
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
		var firstIdx int
		for ti, tt := range terms {
			idx := strings.Index(lower, tt)
			if idx < 0 {
				allMatch = false
				break
			}
			if ti == 0 {
				firstIdx = idx
			}
		}
		if !allMatch {
			continue
		}
		// Use first term match for highlight indices
		matched := make([]int, len(terms[0]))
		for j := range len(terms[0]) {
			matched[j] = firstIdx + j
		}
		ranks = append(ranks, list.Rank{Index: i, MatchedIndexes: matched})
	}
	return ranks
}

type sessionItem struct {
	sess      session.Session
	treeDepth int  // 0=root, 1=teammate child
	treeLast  bool // last child in group (└─ vs ├─)
}

func (s sessionItem) FilterValue() string {
	parts := []string{
		s.sess.ProjectPath,
		s.sess.ProjectName,
		s.sess.GitBranch,
		s.sess.ShortID,
		s.sess.FirstPrompt,
	}
	if s.sess.IsLive {
		parts = append(parts, "is:live")
	}
	if s.sess.IsResponding {
		parts = append(parts, "is:busy")
	}
	if s.sess.IsWorktree {
		parts = append(parts, "is:wt")
	}
	if s.sess.HasMemory {
		parts = append(parts, "has:mem")
	}
	if s.sess.HasTodos {
		parts = append(parts, "has:todo")
	}
	if s.sess.HasTasks {
		parts = append(parts, "has:task")
	}
	if s.sess.HasPlan {
		parts = append(parts, "has:plan")
	}
	if s.sess.HasAgents {
		parts = append(parts, "has:agent")
	}
	if s.sess.HasCompaction {
		parts = append(parts, "has:compact")
	}
	if s.sess.HasSkills {
		parts = append(parts, "has:skill")
	}
	if s.sess.HasMCP {
		parts = append(parts, "has:mcp")
	}
	if s.sess.TeamName != "" {
		parts = append(parts, "is:team", "team:"+s.sess.TeamName)
	}
	if s.sess.TeammateName != "" {
		parts = append(parts, s.sess.TeammateName)
	}
	return strings.Join(parts, " ")
}

type sessionDelegate struct {
	timeW int // max width of time-ago column
	msgW  int // max width of message count column
}

func (d sessionDelegate) Height() int                             { return 2 }
func (d sessionDelegate) Spacing() int                            { return 0 }
func (d sessionDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (d sessionDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	si, ok := item.(sessionItem)
	if !ok {
		return
	}

	s := si.sess
	selected := index == m.Index()
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

	cursor := "  "
	if selected {
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

	idStr := idStyle.Render(s.ShortID)

	timeRaw := timeAgo(s.ModTime)
	timePad := fmt.Sprintf("%-*s", d.timeW, timeRaw)
	timeStr := timeStyle.Render(timePad)

	msgRaw := fmt.Sprintf("%dm", s.MsgCount)
	msgPad := fmt.Sprintf("%*s", d.msgW, msgRaw)
	msgStr := msgStyle.Render(msgPad)

	// Build badges first to know their width
	badges := ""
	badgesW := 0
	if s.IsLive {
		if s.IsResponding {
			badges += " " + busyBadge.Render("[BUSY]")
		} else {
			badges += " " + liveBadge.Render("[LIVE]")
		}
		badgesW += 7
	}
	if s.HasMemory {
		badges += " " + memoryBadge.Render("[M]")
		badgesW += 4
	}
	if s.IsWorktree {
		badges += " " + worktreeBadge.Render("[W]")
		badgesW += 4
	}
	if s.HasTodos {
		badges += " " + todoBadge.Render("[T]")
		badgesW += 4
	}
	if s.HasTasks {
		badges += " " + taskBadge.Render("[K]")
		badgesW += 4
	}
	if s.HasPlan {
		badges += " " + planBadge.Render("[P]")
		badgesW += 4
	}
	if s.HasAgents {
		badges += " " + agentBadgeStyle.Render("[A]")
		badgesW += 4
	}
	if s.HasCompaction {
		badges += " " + compactBadgeStyle.Render("[C]")
		badgesW += 4
	}
	if s.HasSkills {
		badges += " " + todoBadge.Render("[S]")
		badgesW += 4
	}
	if s.HasMCP {
		badges += " " + mcpBadgeStyle.Render("[X]")
		badgesW += 4
	}

	// Calculate available width for project column
	// cursor(2) + tree(treePrefixW) + id(8) + 2 + time + 2 + msg + 2 + project + badges
	fixedW := 2 + treePrefixW + 8 + 2 + d.timeW + 2 + d.msgW + 2 + badgesW
	maxProjW := width - fixedW
	if maxProjW < 4 {
		maxProjW = 4
	}

	// For tree children, show teammate name (team mode) or branch (project mode)
	projName := s.ProjectName
	if si.treeDepth > 0 && s.TeammateName != "" {
		projName = s.TeammateName
	} else if si.treeDepth > 0 && s.GitBranch != "" {
		projName = s.GitBranch
	}
	branch := ""
	if s.GitBranch != "" && si.treeDepth == 0 {
		branch = " (" + s.GitBranch + ")"
	}
	fullProj := projName + branch
	filterTerm := listFilterTerm(m)

	// Style teammate names in cyan
	if si.treeDepth > 0 && s.TeammateName != "" {
		projStyle = teamBadge
		if selected {
			projStyle = teamBadge.Bold(true)
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

	line1 := fmt.Sprintf("%s%s%s  %s  %s  %s%s", cursor, treePrefix, idStr, timeStr, msgStr, project, badges)

	prompt := s.FirstPrompt
	maxW := width - 6 - treePrefixW
	promptIndent := "    " + strings.Repeat(" ", treePrefixW)
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
	fmt.Fprintf(w, "%s\n%s", clamp.Render(line1), clamp.Render(line2))
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

func newSessionList(sessions []session.Session, width, height int, groupMode int) list.Model {
	items := buildGroupedItems(sessions, groupMode)

	timeW, msgW := computeSessionColWidths(sessions)

	l := list.New(items, sessionDelegate{timeW: timeW, msgW: msgW}, width, height)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowFilter(false)
	l.SetShowPagination(false)
	l.SetFilteringEnabled(true)
	l.SetShowHelp(false)
	l.Filter = substringFilter
	l.DisableQuitKeybindings()
	configureListSearch(&l)
	l.SetSize(width, height) // re-compute pagination after hiding bars
	return l
}

// buildTreeItems groups sessions by team, placing leaders at depth=0 and
// teammates at depth=1, interleaved with standalone sessions by recency.
func buildTreeItems(sessions []session.Session) []list.Item {
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
			// Leader (or first teammate as header if no leader)
			if g.leader != nil {
				items = append(items, sessionItem{sess: *g.leader, treeDepth: 0})
			} else if len(g.teammates) > 0 {
				items = append(items, sessionItem{sess: g.teammates[0], treeDepth: 0})
				g.teammates = g.teammates[1:]
			}
			for ti, tm := range g.teammates {
				items = append(items, sessionItem{
					sess:      tm,
					treeDepth: 1,
					treeLast:  ti == len(g.teammates)-1,
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
func buildProjectGroupItems(sessions []session.Session) []list.Item {
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
		// First (most recent) session is the header
		items = append(items, sessionItem{sess: g.sessions[0], treeDepth: 0})
		children := g.sessions[1:]
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

// renderHelpOverlay renders a help screen showing badge descriptions and search filters.
func renderHelpOverlay(width, height int) string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(colorPrimary)
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(colorAccent)

	var sb strings.Builder
	sb.WriteString(titleStyle.Render("  ccx — Help") + "\n\n")

	sb.WriteString(headerStyle.Render("  Badges") + "\n")
	badges := []struct{ badge, desc string }{
		{"[LIVE]", "Session has a running Claude process"},
		{"[BUSY]", "Session is actively responding"},
		{"[M]", "Session has CLAUDE.md memory file"},
		{"[W]", "Session uses a git worktree"},
		{"[T]", "Session has todos (TodoWrite)"},
		{"[K]", "Session has tasks (TaskCreate/TaskUpdate)"},
		{"[P]", "Session has a plan file"},
		{"[A]", "Session spawned subagents (Task tool)"},
		{"[C]", "Session hit context limit (compacted)"},
		{"[S]", "Session used skills"},
		{"[X]", "Session used MCP tools"},
	}
	for _, b := range badges {
		sb.WriteString(fmt.Sprintf("    %-8s %s\n", b.badge, dimStyle.Render(b.desc)))
	}

	sb.WriteString("\n" + headerStyle.Render("  Search Filters") + "\n")
	filters := []struct{ filter, desc string }{
		{"is:live", "Live sessions"},
		{"is:busy", "Busy (responding) sessions"},
		{"is:wt", "Worktree sessions"},
		{"is:team", "Team sessions"},
		{"has:mem", "Sessions with memory"},
		{"has:todo", "Sessions with todos"},
		{"has:task", "Sessions with tasks"},
		{"has:plan", "Sessions with plans"},
		{"has:agent", "Sessions with subagents"},
		{"has:compact", "Sessions with compaction"},
		{"has:skill", "Sessions with skills"},
		{"has:mcp", "Sessions with MCP tools"},
		{"team:<name>", "Filter by team name"},
	}
	for _, f := range filters {
		sb.WriteString(fmt.Sprintf("    %-14s %s\n", f.filter, dimStyle.Render(f.desc)))
	}

	sb.WriteString("\n" + headerStyle.Render("  Keybindings") + "\n")
	keys := []struct{ key, desc string }{
		{"↵ / →", "Open session / preview"},
		{"g", "Open project directory"},
		{"e", "Edit session files"},
		{"x", "Actions menu (delete, move, worktree)"},
		{"/", "Search / filter sessions"},
		{"G", "Cycle grouping (flat → project → tree)"},
		{"S", "Global stats"},
		{"R", "Refresh session list"},
		{"tab", "Toggle/cycle preview mode"},
		{"L", "Live session modal (tmux)"},
		{"?", "This help screen"},
	}
	for _, k := range keys {
		sb.WriteString(fmt.Sprintf("    %-14s %s\n", k.key, dimStyle.Render(k.desc)))
	}

	text := sb.String()
	lines := strings.Split(text, "\n")
	// Pad to fill height
	for len(lines) < height {
		lines = append(lines, "")
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}
