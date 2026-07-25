package tui

import "github.com/charmbracelet/lipgloss"

const (
	// Line-art icon palette. Prefer outline Nerd Font / Font Awesome
	// glyphs over emoji so the TUI stays monochrome and abstract.
	iconIdle        = "" // circle-o
	iconActive      = "" // dot-circle-o
	iconFocused     = "" // circle
	iconProgress    = "" // spinner
	iconDone        = "" // check-circle-o
	iconStopped     = "" // stop-circle-o
	iconWaiting     = "" // hourglass-o
	iconTask        = "" // list-ul
	iconFolder      = "" // folder-o
	iconFolderOpen  = "" // folder-open-o
	iconAgent       = "" // code
	iconImage       = "" // file-image-o
	iconHook        = "" // bolt
	iconBlockMarker = "" // dot-circle-o
	iconFoldClosed  = "▸"
	iconFoldOpen    = "▾"
	iconSelect      = iconDone

	iconRoleUser      = ""
	iconRoleAssistant = ""
	iconRoleCompact   = ""
	iconBadgeLive     = ""
	iconBadgeBusy     = ""
	iconBadgeBg       = ""
	iconBadgeMon      = ""
	iconBadgeHere     = ""
	iconBadgeDone     = ""
	iconBadgeWait     = ""
	iconBadgeStuck    = ""
	iconBadgeInput    = "" // live session awaiting user answer (AskUserQuestion)
	iconBadgeRemote   = ""
	iconTrendUp       = "▴"
	iconTrendDown     = "▾"
	iconStatusDot     = "●" // live/busy status dot before the session ID
	iconBadgePR       = "" // open pull request (code-fork glyph)
	iconBarFull       = "█"
	iconBarLight      = "▓"
	iconBarMid        = "▒"
	iconBarEmpty      = "░"
)

func roleChip(role string) string {
	switch role {
	case "user":
		return iconRoleUser + " usr"
	case "assistant":
		return iconRoleAssistant + " ast"
	case "compact":
		return iconRoleCompact + " ctx"
	default:
		return role
	}
}

func sectionTitle(icon, text string) string {
	return icon + "  " + text
}

func badgeLabel(icon, text string) string {
	return "[" + icon + " " + text + "]"
}

var (
	colorPrimary       = lipgloss.Color("#7C3AED")
	colorTitleBg       = lipgloss.Color("#1E293B") // subtle dark bg for title bar / selected row bg
	colorDim           = lipgloss.Color("#6B7280")
	colorAccent        = lipgloss.Color("#10B981")
	colorUser          = lipgloss.Color("#3B82F6")
	colorAssistant     = lipgloss.Color("#F59E0B")
	colorError         = lipgloss.Color("#EF4444")
	colorWorktree      = lipgloss.Color("#8B5CF6")
	colorFilter        = lipgloss.Color("#EC4899")
	colorBorderFocused = lipgloss.Color("#38BDF8")
	colorBorderDim     = lipgloss.Color("#374151")
	// Extended palette — named tokens so theming/NO_COLOR can be applied
	// centrally and raw hex never appears in render paths.
	colorSuccess    = lipgloss.Color("#22C55E") // green
	colorGold       = lipgloss.Color("#FBBF24") // memory / wait badge
	colorPurple     = lipgloss.Color("#A78BFA") // compact / plan / PR / monitor badge
	colorCyan       = lipgloss.Color("#22D3EE") // bg badge
	colorTeal       = lipgloss.Color("#06B6D4") // team / agent badge
	colorOrange     = lipgloss.Color("#FB923C") // task / hook badge
	colorPink       = lipgloss.Color("#F472B6") // here badge
	colorLight      = lipgloss.Color("#E2E8F0") // light text
	colorSky        = lipgloss.Color("#7DD3FC") // light cyan (web URL / diff hunk)
	colorSelectedFg = lipgloss.Color("#D1D5DB")
	colorHelp       = lipgloss.Color("#9CA3AF") // shortcut-key gray
	colorBorder     = lipgloss.Color("#4B5563")
	colorWhite      = lipgloss.Color("#FFFFFF")
	colorRed        = lipgloss.Color("#FF0000") // pure red (diff/delete marker)
	colorRedSoft    = lipgloss.Color("#F87171")
	colorGray       = lipgloss.Color("#A1A1AA")
	colorSkyBlue    = lipgloss.Color("#87CEEB")
	colorLime       = lipgloss.Color("#84CC16") // custom badge
	colorIndigo     = lipgloss.Color("#6366F1")
	colorGreenSoft  = lipgloss.Color("#4ADE80")
	colorNavy       = lipgloss.Color("#3B4D7A")
	colorNavyDark   = lipgloss.Color("#2A3A5C")
	colorMatchPink  = lipgloss.Color("#F9A8D4") // search-match highlight

	helpStyle = lipgloss.NewStyle().Foreground(colorDim)

	userLabelStyle      = lipgloss.NewStyle().Foreground(colorUser).Bold(true)
	assistantLabelStyle = lipgloss.NewStyle().Foreground(colorAssistant).Bold(true)
	toolStyle           = lipgloss.NewStyle().Foreground(colorDim).Italic(true)
	toolBlockStyle      = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	errorStyle          = lipgloss.NewStyle().Foreground(colorError)
	dimStyle            = lipgloss.NewStyle().Foreground(colorDim)
	selectedStyle       = lipgloss.NewStyle().Foreground(colorSelectedFg)
	selectedRowStyle    = lipgloss.NewStyle().Background(colorTitleBg)
	filterBadge         = lipgloss.NewStyle().Foreground(colorFilter).Bold(true)
	teamBadge           = lipgloss.NewStyle().Foreground(colorTeal).Bold(true)
	agentBadgeStyle     = lipgloss.NewStyle().Foreground(colorTeal).Bold(true)
	compactBadgeStyle   = lipgloss.NewStyle().Foreground(colorPurple).Bold(true)
	taskBadgeStyle      = lipgloss.NewStyle().Foreground(colorOrange).Bold(true)
	memoryBadge         = lipgloss.NewStyle().Foreground(colorGold).Bold(true)
	memoryMatchStyle    = lipgloss.NewStyle().Foreground(colorFilter).Bold(true)
	planBadge           = lipgloss.NewStyle().Foreground(colorPurple).Bold(true)
	liveBadge           = lipgloss.NewStyle().Foreground(colorSuccess).Bold(true)
	busyBadge           = lipgloss.NewStyle().Foreground(colorAssistant).Bold(true)
	forkBadge           = lipgloss.NewStyle().Foreground(colorAssistant).Bold(true)
	hereBadge           = lipgloss.NewStyle().Foreground(colorPink).Bold(true)
	bgBadgeStyle        = lipgloss.NewStyle().Foreground(colorCyan).Bold(true)
	monBadgeStyle       = lipgloss.NewStyle().Foreground(colorPurple).Bold(true)
	waitBadgeStyle      = lipgloss.NewStyle().Foreground(colorGold).Bold(true)
	doneBadgeStyle      = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	stuckBadgeStyle     = lipgloss.NewStyle().Foreground(colorError).Bold(true)
	inputBadgeStyle     = lipgloss.NewStyle().Foreground(colorFilter).Bold(true)
	// PR reference badge (open PRs surfaced on the session row) — GitHub purple.
	prBadgeStyle = lipgloss.NewStyle().Foreground(colorPurple).Bold(true)
	// Status dots that replace the LIVE/BUSY text badges: a single ● before the
	// session ID. Green = live & idle, amber = busy/responding.
	liveDotStyle     = lipgloss.NewStyle().Foreground(colorSuccess).Bold(true)
	busyDotStyle     = lipgloss.NewStyle().Foreground(colorAssistant).Bold(true)
	customBadgeStyle = lipgloss.NewStyle().Foreground(colorLime).Bold(true).Italic(true)
	blockCursorStyle = lipgloss.NewStyle().Foreground(colorBorderFocused).Bold(true)
	blockSelectedBg  = lipgloss.NewStyle().Background(colorTitleBg)
	previewBorder    = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder(), true, false, false, false).
				BorderForeground(colorDim)

	// Message list: tool-only continuation rows
	toolOnlyLabelStyle = lipgloss.NewStyle().Foreground(colorDim)
	toolOnlySepStyle   = lipgloss.NewStyle().Foreground(colorBorder)
	acDimStyle         = lipgloss.NewStyle().Foreground(colorDim).Italic(true)

	// Conversation preview
	convCursorStyle = lipgloss.NewStyle().Foreground(colorBorderFocused).Bold(true)
	convSepStyle    = lipgloss.NewStyle().Foreground(colorBorderDim)

	// Search match highlight
	matchHighlight = lipgloss.NewStyle().Foreground(colorMatchPink).Bold(true)

	// Help line: shortcut keys vs description text
	helpKeyStyle = lipgloss.NewStyle().Foreground(colorHelp)

	// Stats rendering (shared across renderSessionStats, renderGlobalStats, timelines)
	statTitleStyle  = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	statNumStyle    = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	statAccentStyle = lipgloss.NewStyle().Foreground(colorAccent)
	statInputStyle  = lipgloss.NewStyle().Foreground(colorUser)
	statOutputStyle = lipgloss.NewStyle().Foreground(colorAssistant)
	statCostStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))

	// Multi-select checkmark
	selectMarkStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)

	// Task status icons in conversation
	taskDoneStyle       = lipgloss.NewStyle().Foreground(colorAccent)
	taskInProgressStyle = lipgloss.NewStyle().Foreground(colorAssistant)

	// Fleet notification indicator
	notifyBadgeStyle = lipgloss.NewStyle().Foreground(colorAssistant).Bold(true)
	notifyCountStyle = lipgloss.NewStyle().Foreground(colorAssistant)

	// Skill and hook styles for message detail
	skillBlockStyle = lipgloss.NewStyle().Foreground(colorPurple).Bold(true)
	// Monitor tool style — cyan, matching the shells preview Monitor color.
	monitorBlockStyle = lipgloss.NewStyle().Foreground(colorCyan).Bold(true)
	hookBadgeStyle    = lipgloss.NewStyle().Foreground(colorOrange)
	hookDetailStyle   = lipgloss.NewStyle().Foreground(colorHelp).Italic(true)
)
