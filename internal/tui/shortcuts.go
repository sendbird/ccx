package tui

import tea "github.com/charmbracelet/bubbletea"

// ShortcutMap maps a key string ("1"-"9") to a command registry name.
type ShortcutMap map[string]string

// ViewShortcuts defines shortcuts for left (list) and right (preview) focus sides.
type ViewShortcuts struct {
	Left  ShortcutMap `yaml:"left,omitempty"`
	Right ShortcutMap `yaml:"right,omitempty"`
}

// Shortcuts maps view names to their focus-scoped shortcuts.
type Shortcuts map[string]ViewShortcuts

// DefaultShortcuts returns sensible defaults for all views.
func DefaultShortcuts() Shortcuts {
	return Shortcuts{
		"sessions": {
			Left: ShortcutMap{
				"1": "preview:conv",
				"2": "preview:stats",
				"3": "preview:mem",
				"4": "preview:tasks",
				"5": "preview:agents",
				"6": "preview:live",
			},
		},
		"conversation": {
			Left: ShortcutMap{
				"1": "pane:flat",
				"2": "pane:tree",
			},
			Right: ShortcutMap{
				"1": "detail:compact",
				"2": "detail:standard",
				"3": "detail:verbose",
			},
		},
		"config": {
			Left: ShortcutMap{
				"1": "page:overview",
				"2": "page:memory",
				"3": "page:project",
				"4": "page:skills",
				"5": "page:hooks",
				"6": "page:mcp",
			},
		},
		"stats": {
			Left: ShortcutMap{
				"1": "page:overview",
				"2": "page:tools",
				"3": "page:errors",
			},
		},
	}
}

// mergeShortcuts overlays user shortcuts onto defaults.
// User entries override; unset entries keep defaults.
func mergeShortcuts(dst Shortcuts, src Shortcuts) {
	for viewName, srcVS := range src {
		dstVS, ok := dst[viewName]
		if !ok {
			dst[viewName] = srcVS
			continue
		}
		if srcVS.Left != nil {
			if dstVS.Left == nil {
				dstVS.Left = make(ShortcutMap)
			}
			for k, v := range srcVS.Left {
				dstVS.Left[k] = v
			}
		}
		if srcVS.Right != nil {
			if dstVS.Right == nil {
				dstVS.Right = make(ShortcutMap)
			}
			for k, v := range srcVS.Right {
				dstVS.Right[k] = v
			}
		}
		dst[viewName] = dstVS
	}
	migrateShortcuts(dst)
}

func migrateShortcuts(sc Shortcuts) {
	sess, ok := sc["sessions"]
	if !ok || sess.Left == nil {
		return
	}
	if sess.Left["5"] == "preview:live" {
		sess.Left["5"] = "preview:agents"
		if _, exists := sess.Left["6"]; !exists {
			sess.Left["6"] = "preview:live"
		}
		sc["sessions"] = sess
	}
}

// handleShortcutKey checks if a key press matches a shortcut for the current
// view and focus side, and executes the corresponding command.
// Returns (model, cmd, true) if handled, (nil, nil, false) otherwise.
func (a *App) handleShortcutKey(key string) (tea.Model, tea.Cmd, bool) {
	viewName := a.currentViewName()
	vs, ok := a.shortcuts[viewName]
	if !ok {
		return nil, nil, false
	}

	side := a.currentFocusSide()
	var sm ShortcutMap
	if side == "right" {
		sm = vs.Right
		if len(sm) == 0 {
			sm = vs.Left
		}
	} else {
		sm = vs.Left
		if len(sm) == 0 {
			sm = vs.Right
		}
	}
	if sm == nil {
		return nil, nil, false
	}

	cmdName, ok := sm[key]
	if !ok {
		return nil, nil, false
	}

	entry, found := a.findCmdEntry(cmdName)
	if !found {
		return nil, nil, false
	}

	// Respect view restriction on the command
	if entry.views != 0 && entry.views&(1<<int(a.state)) == 0 {
		return nil, nil, false
	}

	m, cmd := entry.action(a)
	return m, cmd, true
}

// currentViewName returns the string key for the current view.
func (a *App) currentViewName() string {
	switch a.state {
	case viewSessions:
		return "sessions"
	case viewConversation:
		return "conversation"
	case viewMessageFull:
		return "messagefull"
	case viewConfig:
		return "config"
	case viewPlugins:
		return "plugins"
	case viewGlobalStats:
		return "stats"
	}
	return ""
}

// currentFocusSide returns "left" or "right" based on which split pane has focus.
func (a *App) currentFocusSide() string {
	switch a.state {
	case viewSessions:
		if a.sessSplit.Focus && a.sessSplit.Show {
			return "right"
		}
	case viewConversation:
		if a.conv.split.Focus && a.conv.split.Show {
			return "right"
		}
	case viewConfig:
		if a.cfgSplit.Focus && a.cfgSplit.Show {
			return "right"
		}
	case viewPlugins:
		if a.plgSplit.Focus && a.plgSplit.Show {
			return "right"
		}
	}
	return "left"
}

// isInOverlay returns true when a popup menu or overlay is active.
func (a *App) isInOverlay() bool {
	return a.actionsMenu || a.editMenu || a.convActionsMenu ||
		a.viewsMenu || a.statsPageMenu || a.convPageMenu || a.convPageActionsMenu || a.showHelp
}

// shortcutHint returns a compact hint string showing active shortcuts
// for the current view and focus side. e.g. "1:conv 2:stats 3:mem"
func (a *App) shortcutHint() string {
	viewName := a.currentViewName()
	vs, ok := a.shortcuts[viewName]
	if !ok {
		return ""
	}

	side := a.currentFocusSide()
	var sm ShortcutMap
	if side == "right" {
		sm = vs.Right
		if len(sm) == 0 {
			sm = vs.Left
		}
	} else {
		sm = vs.Left
		if len(sm) == 0 {
			sm = vs.Right
		}
	}
	if len(sm) == 0 {
		return ""
	}

	// Build hint in key order (1-9)
	var parts []string
	for i := '1'; i <= '9'; i++ {
		key := string(i)
		if cmd, ok := sm[key]; ok {
			// Shorten command name: "preview:conv" -> "conv"
			short := cmd
			if idx := len(cmd) - 1; idx > 0 {
				for j := len(cmd) - 1; j >= 0; j-- {
					if cmd[j] == ':' {
						short = cmd[j+1:]
						break
					}
				}
			}
			parts = append(parts, key+":"+short)
		}
	}
	if len(parts) == 0 {
		return ""
	}

	result := parts[0]
	for _, p := range parts[1:] {
		result += " " + p
	}
	return result
}
