package tui

import (
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// initListBase applies standard ccx styling to a list.Model:
// hides title, status bar, filter UI, pagination, and help; disables quit keybindings.
func initListBase(l *list.Model) {
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowFilter(false)
	l.SetShowPagination(false)
	l.SetShowHelp(false)
	l.DisableQuitKeybindings()
}

// configureListSearch customizes list search: changes prompt to "Search: ",
// removes vim j/k from cursor navigation, and prevents arrow/navigation keys
// from closing the search input.
func configureListSearch(l *list.Model) {
	l.FilterInput.Prompt = "Search: "
	l.KeyMap.AcceptWhileFiltering = key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "apply"),
	)
	// Arrow-only navigation (remove j/k)
	l.KeyMap.CursorUp = key.NewBinding(
		key.WithKeys("up"),
		key.WithHelp("↑", "up"),
	)
	l.KeyMap.CursorDown = key.NewBinding(
		key.WithKeys("down"),
		key.WithHelp("↓", "down"),
	)
}

// startListSearch activates the search prompt.
// The filter input is rendered in the help line (not in the list header) so
// that list items stay at a stable position.
func startListSearch(l *list.Model) tea.Cmd {
	if l.Width() == 0 {
		return nil
	}
	// Simulate "/" to open filter — must assign back because Update is a value receiver
	openMsg := tea.KeyMsg(tea.Key{Type: tea.KeyRunes, Runes: []rune{'/'}})
	newL, cmd := l.Update(openMsg)
	*l = newL
	return cmd
}

// applyListFilter sets the filter text and applies it,
// leaving the list in FilterApplied state with results narrowed.
func applyListFilter(l *list.Model, query string) {
	if l.Width() == 0 || query == "" {
		return
	}
	l.SetFilterText(query)
}

// syncFilterVisibility is intentionally a no-op. The filter bar inside the
// list is always hidden; the filter input is rendered in the bottom help line.
func syncFilterVisibility(_ *list.Model) {}

// highlightSnippet truncates text to maxW, centering the window around the
// first case-insensitive match of term. The matched portion is rendered with
// matchHighlight; the rest with baseStyle. If term is empty or not found, falls
// back to normal head-truncation styled entirely in baseStyle.
func highlightSnippet(text, term string, maxW int, baseStyle lipgloss.Style) string {
	if maxW <= 0 {
		return ""
	}
	// Fallback: simple truncation from head
	truncate := func(s string) string {
		if len(s) <= maxW {
			return baseStyle.Render(s)
		}
		if maxW <= 3 {
			return baseStyle.Render(s[:maxW])
		}
		return baseStyle.Render(s[:maxW-3] + "...")
	}

	if term == "" {
		return truncate(text)
	}

	lower := strings.ToLower(text)
	lowerTerm := strings.ToLower(term)
	idx := strings.Index(lower, lowerTerm)
	if idx < 0 {
		return truncate(text)
	}

	matchLen := len(term)
	matchEnd := idx + matchLen

	// Text fits entirely — just highlight in place
	if len(text) <= maxW {
		return baseStyle.Render(text[:idx]) +
			matchHighlight.Render(text[idx:matchEnd]) +
			baseStyle.Render(text[matchEnd:])
	}

	// Need to extract a window. Compute how much visible space we have
	// after reserving room for "..." on each side.
	needLeft := idx > 0                // will we need left ellipsis?
	needRight := matchEnd < len(text)  // will we need right ellipsis?
	const ellipsis = "..."
	const eW = 3

	// Available chars for the actual snippet content
	contentW := maxW
	if needLeft {
		contentW -= eW
	}
	if needRight {
		contentW -= eW
	}
	if contentW < matchLen {
		// Not enough room even for the match — show as much of match as possible
		contentW = maxW
		needLeft = false
		needRight = false
	}

	// Center the match within contentW
	pad := contentW - matchLen
	leftPad := pad / 2
	rightPad := pad - leftPad

	winStart := idx - leftPad
	winEnd := matchEnd + rightPad

	// Clamp to text bounds and adjust
	if winStart < 0 {
		winEnd -= winStart // shift right
		winStart = 0
	}
	if winEnd > len(text) {
		winStart -= winEnd - len(text) // shift left
		winEnd = len(text)
	}
	if winStart < 0 {
		winStart = 0
	}

	// Re-evaluate ellipsis need after clamping
	needLeft = winStart > 0
	needRight = winEnd < len(text)

	// Final safety: ensure slice bounds are valid
	if winStart > len(text) {
		winStart = len(text)
	}
	if winEnd > len(text) {
		winEnd = len(text)
	}
	if winStart >= winEnd {
		return truncate(text)
	}

	snippet := text[winStart:winEnd]
	localIdx := idx - winStart
	localEnd := matchEnd - winStart
	if localIdx < 0 {
		localIdx = 0
	}
	if localEnd > len(snippet) {
		localEnd = len(snippet)
	}

	var sb strings.Builder
	if needLeft {
		sb.WriteString(baseStyle.Render(ellipsis))
	}
	sb.WriteString(baseStyle.Render(snippet[:localIdx]))
	sb.WriteString(matchHighlight.Render(snippet[localIdx:localEnd]))
	sb.WriteString(baseStyle.Render(snippet[localEnd:]))
	if needRight {
		sb.WriteString(baseStyle.Render(ellipsis))
	}
	return sb.String()
}

// highlightInline highlights all case-insensitive occurrences of term in text
// using matchHighlight style, with the rest rendered in baseStyle.
func highlightInline(text, term string, baseStyle lipgloss.Style) string {
	if term == "" {
		return baseStyle.Render(text)
	}
	lower := strings.ToLower(text)
	lowerTerm := strings.ToLower(term)
	idx := strings.Index(lower, lowerTerm)
	if idx < 0 {
		return baseStyle.Render(text)
	}
	var sb strings.Builder
	for idx >= 0 {
		sb.WriteString(baseStyle.Render(text[:idx]))
		sb.WriteString(matchHighlight.Render(text[idx : idx+len(term)]))
		text = text[idx+len(term):]
		lower = lower[idx+len(term):]
		idx = strings.Index(lower, lowerTerm)
	}
	sb.WriteString(baseStyle.Render(text))
	return sb.String()
}

// listFilterTerm returns the active filter term for a list, or "" if not filtering.
func listFilterTerm(m list.Model) string {
	if m.FilterState() == list.Filtering || m.FilterState() == list.FilterApplied {
		return m.FilterValue()
	}
	return ""
}

var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return ansiRegex.ReplaceAllString(s, "")
}

func copyToClipboard(text string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

func openInPager(styledContent string) tea.Cmd {
	plain := stripANSI(styledContent)
	tmpFile, err := os.CreateTemp("", "ccx-*.txt")
	if err != nil {
		return nil
	}
	tmpFile.WriteString(plain)
	tmpFile.Close()

	c := exec.Command("less", tmpFile.Name())
	return tea.ExecProcess(c, func(err error) tea.Msg {
		os.Remove(tmpFile.Name())
		return nil
	})
}
