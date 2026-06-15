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
	iconBadgeRemote   = ""
	iconTrendUp       = "▴"
	iconTrendDown     = "▾"
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
	colorTitleBg       = lipgloss.Color("#1E293B") // subtle dark bg for title bar
	colorDim           = lipgloss.Color("#6B7280")
	colorAccent        = lipgloss.Color("#10B981")
	colorUser          = lipgloss.Color("#3B82F6")
	colorAssistant     = lipgloss.Color("#F59E0B")
	colorError         = lipgloss.Color("#EF4444")
	colorWorktree      = lipgloss.Color("#8B5CF6")
	colorFilter        = lipgloss.Color("#EC4899")
	colorBorderFocused = lipgloss.Color("#38BDF8")
	colorBorderDim     = lipgloss.Color("#374151")

	helpStyle = lipgloss.NewStyle().Foreground(colorDim)

	userLabelStyle      = lipgloss.NewStyle().Foreground(colorUser).Bold(true)
	assistantLabelStyle = lipgloss.NewStyle().Foreground(colorAssistant).Bold(true)
	toolStyle           = lipgloss.NewStyle().Foreground(colorDim).Italic(true)
	toolBlockStyle      = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	errorStyle          = lipgloss.NewStyle().Foreground(colorError)
	dimStyle            = lipgloss.NewStyle().Foreground(colorDim)
	selectedStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#D1D5DB"))
	selectedRowStyle    = lipgloss.NewStyle().Background(lipgloss.Color("#1E293B"))
	filterBadge         = lipgloss.NewStyle().Foreground(colorFilter).Bold(true)
	teamBadge           = lipgloss.NewStyle().Foreground(lipgloss.Color("#06B6D4")).Bold(true)
	agentBadgeStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#06B6D4")).Bold(true)
	compactBadgeStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA")).Bold(true)
	taskBadgeStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#FB923C")).Bold(true)
	memoryBadge         = lipgloss.NewStyle().Foreground(lipgloss.Color("#FBBF24")).Bold(true)
	planBadge           = lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA")).Bold(true)
	liveBadge           = lipgloss.NewStyle().Foreground(lipgloss.Color("#22C55E")).Bold(true)
	busyBadge           = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Bold(true)
	forkBadge           = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Bold(true)
	hereBadge           = lipgloss.NewStyle().Foreground(lipgloss.Color("#F472B6")).Bold(true)
	bgBadgeStyle        = lipgloss.NewStyle().Foreground(lipgloss.Color("#22D3EE")).Bold(true)
	monBadgeStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA")).Bold(true)
	waitBadgeStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#FBBF24")).Bold(true)
	doneBadgeStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#10B981")).Bold(true)
	stuckBadgeStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444")).Bold(true)
	customBadgeStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#84CC16")).Bold(true).Italic(true)
	blockCursorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8")).Bold(true)
	blockSelectedBg     = lipgloss.NewStyle().Background(lipgloss.Color("#1E293B"))
	previewBorder       = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder(), true, false, false, false).
				BorderForeground(colorDim)

	// Message list: tool-only continuation rows
	toolOnlyLabelStyle = lipgloss.NewStyle().Foreground(colorDim)
	toolOnlySepStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#4B5563"))
	acDimStyle         = lipgloss.NewStyle().Foreground(colorDim).Italic(true)

	// Conversation preview
	convCursorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8")).Bold(true)
	convSepStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#374151"))

	// Search match highlight
	matchHighlight = lipgloss.NewStyle().Foreground(lipgloss.Color("#F9A8D4")).Bold(true)

	// Help line: shortcut keys vs description text
	helpKeyStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))

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

	// Skill and hook styles for message detail
	skillBlockStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA")).Bold(true)
	hookBadgeStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#FB923C"))
	hookDetailStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF")).Italic(true)
)
