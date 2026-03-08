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

// Group mode constants
const (
	groupFlat    = 0
	groupProject = 1
	groupTree    = 2
	groupChain   = 3
	groupFork    = 4
)

// buildGroupedItems returns list items for the given group mode.
func buildGroupedItems(sessions []session.Session, groupMode int) []list.Item {
	switch groupMode {
	case groupProject:
		return buildProjectGroupItems(sessions)
	case groupTree:
		return buildTreeItems(sessions)
	case groupChain:
		return buildChainGroupItems(sessions)
	case groupFork:
		return buildForkGroupItems(sessions)
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
	if s.sess.ParentSessionID != "" {
		parts = append(parts, "is:fork")
	}
	return strings.Join(parts, " ")
}

type sessionDelegate struct {
	timeW       int              // max width of time-ago column
	msgW        int              // max width of message count column
	selectedSet map[string]bool  // shared reference to App.selectedSet
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

	isMultiSelected := d.selectedSet != nil && d.selectedSet[s.ID]
	cursor := "  "
	if selected && isMultiSelected {
		cursor = selectMarkStyle.Render("✓") + " "
	} else if isMultiSelected {
		cursor = selectMarkStyle.Render("✓") + " "
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
	if s.ParentSessionID != "" {
		badges += " " + forkBadge.Render("[F]")
		badgesW += 4
	}

	// Calculate available width for project column
	// cursor(2) + tree(treePrefixW) + id(8) + 2 + time + 2 + msg + 2 + project + badges
	fixedW := 2 + treePrefixW + 8 + 2 + d.timeW + 2 + d.msgW + 2 + badgesW
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

func newSessionList(sessions []session.Session, width, height int, groupMode int, selectedSet map[string]bool) list.Model {
	items := buildGroupedItems(sessions, groupMode)

	timeW, msgW := computeSessionColWidths(sessions)

	l := list.New(items, sessionDelegate{timeW: timeW, msgW: msgW, selectedSet: selectedSet}, width, height)
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

// buildChainGroupItems groups sessions that share the same PlanSlug (continuation
// chain). The earliest session in each chain becomes the depth=0 header; later
// sessions are depth=1 children sorted by Created time.
func buildChainGroupItems(sessions []session.Session) []list.Item {
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
			// Earliest session is the header
			items = append(items, sessionItem{sess: g.sessions[0], treeDepth: 0})
			children := g.sessions[1:]
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
func buildForkGroupItems(sessions []session.Session) []list.Item {
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
			items = append(items, sessionItem{sess: g.sessions[0], treeDepth: 0})
			children := g.sessions[1:]
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

// renderHelpModal renders a centered bordered modal with help content overlaid on bg.
func renderHelpModal(bg string, screenW, screenH int, km Keymap) string {
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
		{liveBadge, "[LIVE]", "Running Claude"},
		{busyBadge, "[BUSY]", "Responding"},
		{memoryBadge, "[M]", "Has memory"},
		{worktreeBadge, "[W]", "Git worktree"},
		{todoBadge, "[T]", "Has todos"},
		{taskBadge, "[K]", "Has tasks"},
		{planBadge, "[P]", "Has plan"},
		{agentBadgeStyle, "[A]", "Has subagents"},
		{compactBadgeStyle, "[C]", "Compacted"},
		{todoBadge, "[S]", "Used skills"},
		{mcpBadgeStyle, "[X]", "Used MCP"},
		{forkBadge, "[F]", "Forked session"},
	}
	// Render badges in pairs (two per line)
	for i := 0; i < len(allBadges); i += 2 {
		b := allBadges[i]
		left := fmt.Sprintf(" %s %s", b.style.Render(fmt.Sprintf("%-6s", b.badge)), d.Render(fmt.Sprintf("%-15s", b.desc)))
		if i+1 < len(allBadges) {
			b2 := allBadges[i+1]
			right := fmt.Sprintf(" %s %s", b2.style.Render(fmt.Sprintf("%-6s", b2.badge)), d.Render(b2.desc))
			sb.WriteString(left + right + "\n")
		} else {
			sb.WriteString(left + "\n")
		}
	}

	// Search filters: two-column layout
	sb.WriteString("\n" + headerStyle.Render(" Search Filters") + "\n")
	type filter struct{ filter, desc string }
	allFilters := []filter{
		{"is:live", "Live sessions"},
		{"is:busy", "Busy sessions"},
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
		{"team:<name>", "By team name"},
		{"is:fork", "Forked sessions"},
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
		{displayKey(sk.Actions), "Actions (" + displayKey(km.Actions.Delete) + "/" + displayKey(km.Actions.Move) + "/" + displayKey(km.Actions.Resume) + "/" + displayKey(km.Actions.Worktree) + "/" + displayKey(km.Actions.Kill) + "/" + displayKey(km.Actions.Input) + "/" + displayKey(km.Actions.Jump) + ")"},
		{displayKey(sk.Search), "Search / filter"},
		{displayKey(sk.Group), "Group (flat→proj→tree→chain)"},
		{displayKey(km.Views.Stats), "Global stats"},
		{displayKey(sk.Refresh), "Refresh list"},
		{displayKey(sk.Preview), "Cycle preview mode"},
		{displayKey(sk.Live), "Live preview (^Q:unfocus)"},
		{displayKey(sk.Select), "Toggle multi-select"},
		{displayKey(sk.Help), "This help"},
		{displayKey(sk.Quit), "Quit"},
	}
	for _, k := range keys {
		sb.WriteString(fmt.Sprintf(" %-12s %s\n", k.key, d.Render(k.desc)))
	}

	body := strings.TrimRight(sb.String(), "\n")
	bodyLines := strings.Split(body, "\n")

	// Modal dimensions: fit content with padding, capped to screen
	modalW := 60
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

	// Center offsets
	startY := (height - fgH) / 2
	startX := (width - fgW) / 2
	if startY < 0 {
		startY = 0
	}
	if startX < 0 {
		startX = 0
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
func placeHintBox(content, hintBox string) string {
	contentLines := strings.Split(content, "\n")
	boxLines := strings.Split(hintBox, "\n")
	startY := len(contentLines) - len(boxLines)
	if startY < 0 {
		startY = 0
	}
	// Find max width of content for overlay
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
			contentLines[y] = overlayLine(contentLines[y], bl, 1, maxW)
		}
	}
	return strings.Join(contentLines, "\n")
}

// overlayLine replaces a portion of bgLine starting at col with fgLine,
// handling ANSI escape sequences properly. After the overlay, it restores
// the background's ANSI state so right-side cells keep their styling.
func overlayLine(bgLine, fgLine string, col, maxWidth int) string {
	bgCells := splitANSICells(bgLine)
	fgW := lipgloss.Width(fgLine)

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
	}
	// Trailing escapes (no printable after them) — attach to last cell or discard
	if pending.Len() > 0 {
		if len(cells) > 0 {
			cells[len(cells)-1] += pending.String()
		}
	}
	return cells
}
