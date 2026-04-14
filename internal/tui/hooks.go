package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// hookEntry represents a single hook command within a matcher group.
type hookEntry struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout"`
}

// hookMatcher represents a matcher group with its hooks.
type hookMatcher struct {
	Matcher string      `json:"matcher"`
	Hooks   []hookEntry `json:"hooks"`
}

// hooksConfig is the top-level hooks structure from settings.json.
type hooksConfig map[string][]hookMatcher

func (a *App) openHooksView() (tea.Model, tea.Cmd) {
	contentH := a.height - 3
	a.hooksVP = viewport.New(a.width, contentH)
	a.hooksVP.SetContent(renderHooksView(a.width))
	a.state = viewConfig
	return a, nil
}

func (a *App) handleHooksKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// Translate navigation aliases (vim hjkl, etc.)
	if _, navMsg := a.keymap.TranslateNav(key, msg); navMsg.Type != msg.Type {
		msg = navMsg
	}

	switch msg.String() {
	case "q":
		return a.quit()
	case "esc":
		a.state = viewSessions
		return a, nil
	case a.keymap.Session.Refresh:
		a.hooksVP.SetContent(renderHooksView(a.width))
		a.copiedMsg = "Refreshed"
		return a, nil
	case "e":
		home, err := os.UserHomeDir()
		if err != nil {
			a.copiedMsg = "Cannot find home dir"
			return a, nil
		}
		return a.openInEditor(filepath.Join(home, ".claude", "settings.json"))
	}
	var cmd tea.Cmd
	a.hooksVP, cmd = a.hooksVP.Update(msg)
	return a, cmd
}

func renderHooksView(width int) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return dimStyle.Render("(cannot determine home directory)")
	}

	settingsPath := filepath.Join(home, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return dimStyle.Render("(no settings.json found)")
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return dimStyle.Render("(invalid settings.json)")
	}

	hooksRaw, ok := raw["hooks"]
	if !ok {
		return dimStyle.Render("(no hooks configured)")
	}

	var hooks hooksConfig
	if err := json.Unmarshal(hooksRaw, &hooks); err != nil {
		return dimStyle.Render("(invalid hooks format)")
	}

	if len(hooks) == 0 {
		return dimStyle.Render("(no hooks configured)")
	}

	titleStyle := statTitleStyle
	numStyle := statNumStyle
	labelStyle := dimStyle
	rulerW := min(width, 50)
	ruler := dimStyle.Render(strings.Repeat("─", rulerW))

	var sb strings.Builder

	// Gather stats
	totalHooks := 0
	totalMatchers := 0
	totalEvents := len(hooks)
	typeCounts := map[string]int{}
	for _, matchers := range hooks {
		totalMatchers += len(matchers)
		for _, m := range matchers {
			totalHooks += len(m.Hooks)
			for _, h := range m.Hooks {
				t := h.Type
				if t == "" {
					t = "command"
				}
				typeCounts[t]++
			}
		}
	}

	// Overview
	sb.WriteString(titleStyle.Render("HOOKS OVERVIEW") + "\n")
	sb.WriteString(ruler + "\n")
	sb.WriteString(fmt.Sprintf("  Events    %s\n", numStyle.Render(fmt.Sprintf("%d", totalEvents))))
	sb.WriteString(fmt.Sprintf("  Matchers  %s\n", numStyle.Render(fmt.Sprintf("%d", totalMatchers))))
	sb.WriteString(fmt.Sprintf("  Hooks     %s\n", numStyle.Render(fmt.Sprintf("%d", totalHooks))))
	sb.WriteString("\n")

	// Type breakdown
	if len(typeCounts) > 0 {
		sb.WriteString(titleStyle.Render("HOOK TYPES") + "\n")
		sb.WriteString(ruler + "\n")
		for t, c := range typeCounts {
			sb.WriteString(fmt.Sprintf("  %-12s %s\n", labelStyle.Render(t), numStyle.Render(fmt.Sprintf("%d", c))))
		}
		sb.WriteString("\n")
	}

	// Event timeline: bar chart showing hook count per event
	sb.WriteString(titleStyle.Render("EVENT DISTRIBUTION") + "\n")
	sb.WriteString(ruler + "\n")
	maxCount := 0
	for _, matchers := range hooks {
		c := 0
		for _, m := range matchers {
			c += len(m.Hooks)
		}
		if c > maxCount {
			maxCount = c
		}
	}

	// Event order for consistent display
	eventOrder := []string{
		"UserPromptSubmit",
		"PreToolUse",
		"PostToolUse",
		"PreCompact",
		"Notification",
		"Stop",
	}

	barW := rulerW - 22 // space for label + count
	if barW < 5 {
		barW = 5
	}

	renderBar := func(event string, matchers []hookMatcher) {
		c := 0
		for _, m := range matchers {
			c += len(m.Hooks)
		}
		fill := 0
		if maxCount > 0 {
			fill = c * barW / maxCount
		}
		if fill < 1 && c > 0 {
			fill = 1
		}
		bar := statAccentStyle.Render(strings.Repeat("█", fill)) + dimStyle.Render(strings.Repeat("░", barW-fill))
		label := fmt.Sprintf("  %-16s", event)
		sb.WriteString(labelStyle.Render(label) + bar + " " + numStyle.Render(fmt.Sprintf("%d", c)) + "\n")
	}

	rendered := make(map[string]bool)
	for _, event := range eventOrder {
		matchers, ok := hooks[event]
		if !ok {
			continue
		}
		rendered[event] = true
		renderBar(event, matchers)
	}
	for event, matchers := range hooks {
		if rendered[event] {
			continue
		}
		renderBar(event, matchers)
	}
	sb.WriteString("\n")

	// Detailed event sections
	rendered = make(map[string]bool)
	for _, event := range eventOrder {
		matchers, ok := hooks[event]
		if !ok {
			continue
		}
		rendered[event] = true
		renderHookEvent(&sb, event, matchers, width, ruler, titleStyle, numStyle, labelStyle)
	}
	for event, matchers := range hooks {
		if rendered[event] {
			continue
		}
		renderHookEvent(&sb, event, matchers, width, ruler, titleStyle, numStyle, labelStyle)
	}

	return sb.String()
}

func renderHookEvent(sb *strings.Builder, event string, matchers []hookMatcher, width int, ruler string, titleStyle, numStyle, labelStyle lipgloss.Style) {
	hookCount := 0
	for _, m := range matchers {
		hookCount += len(m.Hooks)
	}

	header := fmt.Sprintf("%s (%d hooks, %d matchers)", event, hookCount, len(matchers))
	sb.WriteString(titleStyle.Render(header) + "\n")
	sb.WriteString(ruler + "\n")

	for mi, m := range matchers {
		matcher := m.Matcher
		if matcher == "" {
			matcher = "*"
		}
		connector := "├─"
		if mi == len(matchers)-1 {
			connector = "└─"
		}
		connStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#4B5563"))
		sb.WriteString(fmt.Sprintf("  %s %s %s\n",
			connStyle.Render(connector),
			labelStyle.Render("match:"),
			numStyle.Render(matcher)))

		for hi, h := range m.Hooks {
			cmd := h.Command
			// Shorten home dir prefix
			home, _ := os.UserHomeDir()
			if home != "" {
				cmd = strings.ReplaceAll(cmd, home, "~")
			}

			// Show hook type if not default
			typeTag := ""
			if h.Type != "" && h.Type != "command" {
				typeTag = lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA")).Render("["+h.Type+"] ")
			}

			// Child connector
			childConn := "├─"
			if hi == len(m.Hooks)-1 {
				childConn = "└─"
			}
			indent := "  │ "
			if mi == len(matchers)-1 {
				indent = "    "
			}

			maxW := width - len(indent) - 5 - len(typeTag)
			if maxW > 0 && len(cmd) > maxW {
				cmd = cmd[:maxW-3] + "..."
			}
			sb.WriteString(fmt.Sprintf("  %s %s %s%s\n", indent, connStyle.Render(childConn), typeTag, cmd))
		}
	}
	sb.WriteString("\n")
}
