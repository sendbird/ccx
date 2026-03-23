package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
	"gopkg.in/yaml.v3"
)

// openStatsPageMsg is sent to open a specific stats detail page after stats loads.
type openStatsPageMsg struct {
	page statsDetailMode
}

// cmdEntry defines a single command in the command registry.
type cmdEntry struct {
	name    string   // canonical name, e.g. "group:flat"
	aliases []string // shorter aliases, e.g. ["g:flat", "gf"]
	desc    string   // shown in suggestion hint
	views   int      // bitmask of views where this command appears (0 = all views)
	action  func(a *App) (tea.Model, tea.Cmd)
}

// View bitmask constants for cmdEntry.views.
const (
	cmdSessions = 1 << viewSessions
	cmdConv     = 1 << viewConversation
	cmdMsgFull  = 1 << viewMessageFull
	cmdConfig   = 1 << viewConfig
	cmdPlugins  = 1 << viewPlugins
	cmdStats    = 1 << viewGlobalStats
	cmdAll      = 0 // shown in all views
)

// buildCmdRegistry creates the full list of available commands.
func buildCmdRegistry() []cmdEntry {
	return []cmdEntry{
		// Group modes (sessions only)
		{name: "group:flat", aliases: []string{"g:flat"}, desc: "flat view", views: cmdSessions,
			action: func(a *App) (tea.Model, tea.Cmd) { a.sessGroupMode = groupFlat; a.rebuildSessionList(); return a, nil }},
		{name: "group:proj", aliases: []string{"g:proj"}, desc: "project groups", views: cmdSessions,
			action: func(a *App) (tea.Model, tea.Cmd) { a.sessGroupMode = groupProject; a.rebuildSessionList(); return a, nil }},
		{name: "group:tree", aliases: []string{"g:tree"}, desc: "tree view", views: cmdSessions,
			action: func(a *App) (tea.Model, tea.Cmd) { a.sessGroupMode = groupTree; a.rebuildSessionList(); return a, nil }},
		{name: "group:chain", aliases: []string{"g:chain"}, desc: "chain groups", views: cmdSessions,
			action: func(a *App) (tea.Model, tea.Cmd) { a.sessGroupMode = groupChain; a.rebuildSessionList(); return a, nil }},
		{name: "group:fork", aliases: []string{"g:fork"}, desc: "fork groups", views: cmdSessions,
			action: func(a *App) (tea.Model, tea.Cmd) { a.sessGroupMode = groupFork; a.rebuildSessionList(); return a, nil }},

		// Preview modes (sessions only)
		{name: "preview:conv", aliases: []string{"p:conv"}, desc: "conversation preview", views: cmdSessions,
			action: func(a *App) (tea.Model, tea.Cmd) { a.setSessPreviewMode(sessPreviewConversation); return a, nil }},
		{name: "preview:stats", aliases: []string{"p:stats"}, desc: "stats preview", views: cmdSessions,
			action: func(a *App) (tea.Model, tea.Cmd) { a.setSessPreviewMode(sessPreviewStats); return a, nil }},
		{name: "preview:mem", aliases: []string{"p:mem"}, desc: "memory preview", views: cmdSessions,
			action: func(a *App) (tea.Model, tea.Cmd) { a.setSessPreviewMode(sessPreviewMemory); return a, nil }},
		{name: "preview:tasks", aliases: []string{"p:tasks"}, desc: "tasks preview", views: cmdSessions,
			action: func(a *App) (tea.Model, tea.Cmd) { a.setSessPreviewMode(sessPreviewTasksPlan); return a, nil }},
		{name: "preview:live", aliases: []string{"p:live"}, desc: "live preview", views: cmdSessions,
			action: func(a *App) (tea.Model, tea.Cmd) { a.setSessPreviewMode(sessPreviewLive); return a, nil }},

		// Views
		{
			name: "view:sessions", aliases: []string{"v:sessions", "v:sess"},
			desc: "session browser",
			action: func(a *App) (tea.Model, tea.Cmd) {
				a.state = viewSessions
				return a, nil
			},
		},
		{
			name: "view:stats", aliases: []string{"v:stats"},
			desc: "global stats",
			action: func(a *App) (tea.Model, tea.Cmd) {
				return a.openGlobalStats()
			},
		},
		{
			name: "view:stats:tools", aliases: []string{"v:stats:t"},
			desc: "stats → tools",
			action: func(a *App) (tea.Model, tea.Cmd) {
				m, cmd := a.openGlobalStats()
				return m, tea.Batch(cmd, func() tea.Msg { return openStatsPageMsg{statsDetailTools} })
			},
		},
		{
			name: "view:stats:mcp", aliases: []string{"v:stats:m"},
			desc: "stats → mcp tools",
			action: func(a *App) (tea.Model, tea.Cmd) {
				m, cmd := a.openGlobalStats()
				return m, tea.Batch(cmd, func() tea.Msg { return openStatsPageMsg{statsDetailMCP} })
			},
		},
		{
			name: "view:stats:agents", aliases: []string{"v:stats:a"},
			desc: "stats → agents",
			action: func(a *App) (tea.Model, tea.Cmd) {
				m, cmd := a.openGlobalStats()
				return m, tea.Batch(cmd, func() tea.Msg { return openStatsPageMsg{statsDetailAgents} })
			},
		},
		{
			name: "view:stats:skills", aliases: []string{"v:stats:s"},
			desc: "stats → skills",
			action: func(a *App) (tea.Model, tea.Cmd) {
				m, cmd := a.openGlobalStats()
				return m, tea.Batch(cmd, func() tea.Msg { return openStatsPageMsg{statsDetailSkills} })
			},
		},
		{
			name: "view:stats:commands", aliases: []string{"v:stats:c"},
			desc: "stats → commands",
			action: func(a *App) (tea.Model, tea.Cmd) {
				m, cmd := a.openGlobalStats()
				return m, tea.Batch(cmd, func() tea.Msg { return openStatsPageMsg{statsDetailCommands} })
			},
		},
		{
			name: "view:stats:errors", aliases: []string{"v:stats:e"},
			desc: "stats → errors",
			action: func(a *App) (tea.Model, tea.Cmd) {
				m, cmd := a.openGlobalStats()
				return m, tea.Batch(cmd, func() tea.Msg { return openStatsPageMsg{statsDetailErrors} })
			},
		},
		{
			name: "view:config", aliases: []string{"v:config"},
			desc: "config explorer",
			action: func(a *App) (tea.Model, tea.Cmd) {
				return a.openConfigExplorer()
			},
		},
		{
			name: "view:config:hooks", aliases: []string{"v:hooks"},
			desc: "config → hooks",
			action: func(a *App) (tea.Model, tea.Cmd) {
				return a.openHooksView()
			},
		},
		{
			name: "view:plugins", aliases: []string{"v:plugins", "v:plg"},
			desc: "plugin explorer",
			action: func(a *App) (tea.Model, tea.Cmd) {
				return a.openPluginExplorer()
			},
		},

		// Page filters — config pages
		{
			name: "page:memory", aliases: []string{"p:memory", "page:mem"},
			desc: "filter to memory/global", views: cmdConfig,
			action: func(a *App) (tea.Model, tea.Cmd) {
				if a.state == viewConfig {
					a.cfgFilterCat = cfgFilterMemory
					a.rebuildCfgList()
				}
				return a, nil
			},
		},
		// Config page filters
		{name: "page:project", aliases: []string{"p:project", "page:proj"}, desc: "filter to project", views: cmdConfig,
			action: func(a *App) (tea.Model, tea.Cmd) { a.cfgFilterCat = int(session.ConfigProject); a.rebuildCfgList(); return a, nil }},
		{name: "page:local", aliases: []string{"p:local"}, desc: "filter to local", views: cmdConfig,
			action: func(a *App) (tea.Model, tea.Cmd) { a.cfgFilterCat = int(session.ConfigLocal); a.rebuildCfgList(); return a, nil }},
		{name: "page:skills", aliases: []string{"p:skills"}, desc: "filter to skills", views: cmdConfig,
			action: func(a *App) (tea.Model, tea.Cmd) { a.cfgFilterCat = int(session.ConfigSkill); a.rebuildCfgList(); return a, nil }},
		{name: "page:agents", aliases: []string{"p:agents"}, desc: "filter to agents", views: cmdConfig,
			action: func(a *App) (tea.Model, tea.Cmd) { a.cfgFilterCat = int(session.ConfigAgent); a.rebuildCfgList(); return a, nil }},
		{name: "page:commands", aliases: []string{"p:commands", "page:cmds"}, desc: "filter to commands", views: cmdConfig,
			action: func(a *App) (tea.Model, tea.Cmd) { a.cfgFilterCat = int(session.ConfigCommand); a.rebuildCfgList(); return a, nil }},
		{name: "page:mcp", aliases: []string{"p:mcp"}, desc: "filter to MCP", views: cmdConfig,
			action: func(a *App) (tea.Model, tea.Cmd) { a.cfgFilterCat = int(session.ConfigMCP); a.rebuildCfgList(); return a, nil }},
		{name: "page:hooks", aliases: []string{"p:hooks"}, desc: "filter to hooks", views: cmdConfig,
			action: func(a *App) (tea.Model, tea.Cmd) { a.cfgFilterCat = int(session.ConfigHook); a.rebuildCfgList(); return a, nil }},

		// Stats page filters
		{name: "page:tools", aliases: []string{"p:tools"}, desc: "stats → tools", views: cmdStats,
			action: func(a *App) (tea.Model, tea.Cmd) { return a.openStatsDetail(statsDetailTools) }},
		{name: "page:errors", aliases: []string{"p:errors"}, desc: "stats → errors", views: cmdStats,
			action: func(a *App) (tea.Model, tea.Cmd) { return a.openStatsDetail(statsDetailErrors) }},

		// Overview (config + stats)
		{name: "page:overview", aliases: []string{"p:overview"}, desc: "back to overview", views: cmdConfig | cmdStats,
			action: func(a *App) (tea.Model, tea.Cmd) {
				if a.state == viewGlobalStats {
					a.statsDetail = statsDetailNone
				} else if a.state == viewConfig {
					a.cfgFilterCat = cfgFilterAll
					a.rebuildCfgList()
				}
				return a, nil
			}},

		// Session actions
		{name: "set:ratio", aliases: []string{"ratio"}, desc: "set split ratio (15-85)", views: cmdSessions,
			action: func(a *App) (tea.Model, tea.Cmd) {
				// Handled specially in executeCommand for "set:ratio N" syntax
				a.copiedMsg = "Usage: set:ratio N (15-85)"
				return a, nil
			}},
		{name: "refresh", aliases: []string{"R"}, desc: "refresh sessions", views: cmdSessions,
			action: func(a *App) (tea.Model, tea.Cmd) { cmd := a.doRefresh(); a.copiedMsg = "Refreshed"; return a, cmd }},

		// Global
		{name: "keymap:edit", aliases: []string{"km:edit"}, desc: "edit keymap config",
			action: func(a *App) (tea.Model, tea.Cmd) { return a.bootstrapAndEditKeymap() }},
	}
}

// setSessPreviewMode switches the session preview to the given mode,
// opening the split pane if needed.
func (a *App) setSessPreviewMode(mode sessPreview) {
	a.closePaneProxy()
	a.sessPreviewMode = mode
	a.sessSplit.CacheKey = ""
	if !a.sessSplit.Show {
		idx := a.sessionList.Index()
		a.sessSplit.Show = true
		contentH := max(a.height-3, 1)
		a.sessionList.SetSize(a.sessSplit.ListWidth(a.width, a.splitRatio), contentH)
		a.sessionList.Select(idx)
	}
}

// startCmdMode initializes command mode with an empty text input.
func (a *App) startCmdMode() {
	a.cmdMode = true
	ti := textinput.New()
	ti.Prompt = ":"
	ti.Width = a.width - 20
	ti.Focus()
	a.cmdInput = ti
	a.cmdSuggIdx = -1
	a.updateCmdSuggestions()
}

// handleCmdMode processes key events while in command mode.
func (a *App) handleCmdMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	switch key {
	case "enter":
		a.cmdMode = false
		// If a suggestion is highlighted, execute it directly
		if a.cmdSuggIdx >= 0 && a.cmdSuggIdx < len(a.cmdSuggestions) {
			entry := a.cmdSuggestions[a.cmdSuggIdx]
			if entry.action != nil {
				return entry.action(a)
			}
			// Category hint (no action) — fill as prefix and reopen
			a.cmdMode = true
			a.cmdInput.SetValue(entry.name)
			a.cmdInput.SetCursor(len(entry.name))
			a.updateCmdSuggestions()
			return a, nil
		}
		input := a.cmdInput.Value()
		if input == "" {
			return a, nil
		}
		return a.executeCommand(input)

	case "esc":
		a.cmdMode = false
		return a, nil

	case "tab":
		if len(a.cmdSuggestions) > 0 {
			if a.cmdSuggIdx < 0 {
				a.cmdSuggIdx = 0
			}
			completed := a.cmdSuggestions[a.cmdSuggIdx].name
			// For multi-command: replace only the last part
			cur := a.cmdInput.Value()
			if idx := strings.LastIndex(cur, " "); idx >= 0 {
				completed = cur[:idx+1] + completed
			}
			a.cmdInput.SetValue(completed)
			a.cmdInput.SetCursor(len(a.cmdInput.Value()))
			a.updateCmdSuggestions()
		}
		return a, nil

	case "shift+tab":
		if len(a.cmdSuggestions) > 0 {
			a.cmdSuggIdx--
			if a.cmdSuggIdx < 0 {
				a.cmdSuggIdx = len(a.cmdSuggestions) - 1
			}
		}
		return a, nil

	case "up":
		if len(a.cmdSuggestions) > 0 {
			a.cmdSuggIdx--
			if a.cmdSuggIdx < 0 {
				a.cmdSuggIdx = len(a.cmdSuggestions) - 1
			}
		}
		return a, nil

	case "down":
		if len(a.cmdSuggestions) > 0 {
			a.cmdSuggIdx++
			if a.cmdSuggIdx >= len(a.cmdSuggestions) {
				a.cmdSuggIdx = 0
			}
		}
		return a, nil

	default:
		var cmd tea.Cmd
		a.cmdInput, cmd = a.cmdInput.Update(msg)
		a.updateCmdSuggestions()
		return a, cmd
	}
}

// updateCmdSuggestions filters the command registry based on current input and view.
func (a *App) updateCmdSuggestions() {
	input := strings.ToLower(a.cmdInput.Value())
	a.cmdSuggestions = nil
	a.cmdSuggIdx = -1
	viewBit := 1 << int(a.state)

	if input == "" {
		// Show context-aware category hints
		a.cmdSuggestions = append(a.cmdSuggestions,
			cmdEntry{name: "view:", desc: "sessions stats config plugins"})
		switch a.state {
		case viewSessions:
			a.cmdSuggestions = append(a.cmdSuggestions,
				cmdEntry{name: "group:", desc: "flat proj tree chain fork"},
				cmdEntry{name: "preview:", desc: "conv stats mem tasks live"},
				cmdEntry{name: "set:ratio", desc: "N  (15-85)"},
				cmdEntry{name: "refresh", desc: "reload sessions"})
		case viewConfig:
			a.cmdSuggestions = append(a.cmdSuggestions,
				cmdEntry{name: "page:", desc: "memory project hooks mcp ..."})
		case viewGlobalStats:
			a.cmdSuggestions = append(a.cmdSuggestions,
				cmdEntry{name: "page:", desc: "tools errors overview"})
		}
		a.cmdSuggestions = append(a.cmdSuggestions,
			cmdEntry{name: "keymap:edit", desc: "edit keymap config"})
		return
	}

	// For multi-command input, match against the last part
	matchInput := input
	if idx := strings.LastIndex(input, " "); idx >= 0 {
		matchInput = input[idx+1:]
	}
	if matchInput == "" {
		return
	}

	for _, entry := range a.cmdRegistry {
		if entry.views != cmdAll && entry.views&viewBit == 0 {
			continue // skip commands not applicable to current view
		}
		if matchCmdEntry(entry, matchInput) {
			a.cmdSuggestions = append(a.cmdSuggestions, entry)
			if len(a.cmdSuggestions) >= 8 {
				break
			}
		}
	}
}

// matchCmdEntry returns true if input is a prefix of the entry's name or any alias.
// Case-insensitive.
func matchCmdEntry(entry cmdEntry, input string) bool {
	lower := strings.ToLower(input)
	if strings.HasPrefix(strings.ToLower(entry.name), lower) {
		return true
	}
	for _, alias := range entry.aliases {
		if strings.HasPrefix(strings.ToLower(alias), lower) {
			return true
		}
	}
	return false
}

// executeCommand runs a command by name/alias. Supports space-separated
// multi-commands like "view:config page:hooks" — each part is executed in order.
func (a *App) executeCommand(input string) (tea.Model, tea.Cmd) {
	lower := strings.ToLower(strings.TrimSpace(input))

	// Check set:ratio N (consumes the whole input)
	if strings.HasPrefix(lower, "set:ratio") {
		return a.executeCmdSetRatio(input)
	}

	// Split into parts for multi-command support
	parts := strings.Fields(lower)
	var cmds []tea.Cmd
	for _, part := range parts {
		entry, ok := a.findCmdEntry(part)
		if !ok {
			a.copiedMsg = "Unknown command: " + part
			return a, tea.Batch(cmds...)
		}
		_, cmd := entry.action(a)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return a, tea.Batch(cmds...)
}

// findCmdEntry looks up a command entry by exact name or alias match.
func (a *App) findCmdEntry(lower string) (cmdEntry, bool) {
	for _, entry := range a.cmdRegistry {
		if strings.ToLower(entry.name) == lower {
			return entry, true
		}
		for _, alias := range entry.aliases {
			if strings.ToLower(alias) == lower {
				return entry, true
			}
		}
	}
	return cmdEntry{}, false
}

// executeCmdSetRatio handles "set:ratio N" commands.
func (a *App) executeCmdSetRatio(input string) (tea.Model, tea.Cmd) {
	parts := strings.Fields(input)
	if len(parts) < 2 {
		a.copiedMsg = "Usage: set:ratio N"
		return a, nil
	}
	// Handle "set:ratio 50" or "set:ratio50" (no space after colon)
	valStr := parts[len(parts)-1]
	n, err := strconv.Atoi(valStr)
	if err != nil {
		a.copiedMsg = "Invalid ratio: " + valStr
		return a, nil
	}
	if n < 15 {
		n = 15
	}
	if n > 85 {
		n = 85
	}
	a.splitRatio = n
	if a.sessSplit.Show {
		contentH := max(a.height-3, 1)
		a.sessionList.SetSize(a.sessSplit.ListWidth(a.width, a.splitRatio), contentH)
	}
	a.copiedMsg = fmt.Sprintf("Ratio: %d%%", n)
	return a, nil
}

// renderCmdHintBox renders the floating suggestion box for command mode.
func (a *App) renderCmdHintBox() string {
	if len(a.cmdSuggestions) == 0 {
		return ""
	}

	hl := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	d := dimStyle
	sel := lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8")).Bold(true)

	var lines []string
	for i, entry := range a.cmdSuggestions {
		name := entry.name
		desc := entry.desc
		if i == a.cmdSuggIdx {
			lines = append(lines, sel.Render(name)+"  "+d.Render(desc))
		} else {
			lines = append(lines, hl.Render(name)+"  "+d.Render(desc))
		}
	}

	body := strings.Join(lines, "\n")
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorDim).
		Padding(0, 1)
	return boxStyle.Render(body)
}

// parseCmdSetRatioValue extracts the integer value from a "set:ratio N" command.
// bootstrapAndEditKeymap creates the config file with defaults if it doesn't
// exist, then opens it in $EDITOR.
func (a *App) bootstrapAndEditKeymap() (tea.Model, tea.Cmd) {
	home, err := os.UserHomeDir()
	if err != nil {
		a.copiedMsg = "Cannot find home dir"
		return a, nil
	}
	configPath := filepath.Join(home, ".config", "ccx", "config.yaml")

	// Create file with defaults if missing
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
			a.copiedMsg = "mkdir failed: " + err.Error()
			return a, nil
		}
		data, err := yaml.Marshal(DefaultKeymap())
		if err != nil {
			a.copiedMsg = "marshal failed: " + err.Error()
			return a, nil
		}
		header := "# ccx keymap configuration\n# Uncomment and change values to customize keybindings.\n# Restart ccx after editing.\n\n"
		if err := os.WriteFile(configPath, []byte(header+string(data)), 0644); err != nil {
			a.copiedMsg = "write failed: " + err.Error()
			return a, nil
		}
	}

	return a.openInEditor(configPath)
}

// Exported for testing.
func parseCmdSetRatioValue(input string) (int, bool) {
	parts := strings.Fields(input)
	if len(parts) < 2 {
		return 0, false
	}
	valStr := parts[len(parts)-1]
	n, err := strconv.Atoi(valStr)
	if err != nil {
		return 0, false
	}
	return n, true
}
