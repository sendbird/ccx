package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

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
			Left: flowOrderSessionsLeft(),
		},
		"conversation": {
			Right: ShortcutMap{
				"1": "detail:compact",
				"2": "detail:standard",
				"3": "detail:verbose",
			},
		},
		"config": {
			Left: flowOrderConfigLeft(),
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
	// Detect a stale pre-0 sessions layout from the USER config before the merge
	// clobbers it — the default (dst) always has a "0" key, so the decision must
	// be based on what the user actually persisted.
	staleSessions := isPreZeroSessionsLayout(src)
	staleConfig := isStaleConfigLayout(src)

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
	if staleSessions {
		dst["sessions"] = ViewShortcuts{Left: flowOrderSessionsLeft(), Right: dst["sessions"].Right}
	}
	if staleConfig {
		dst["config"] = ViewShortcuts{Left: flowOrderConfigLeft(), Right: dst["config"].Right}
	}
}

// isPreZeroSessionsLayout reports whether the user config's sessions.left is a
// pre-0-key default layout (1=conv with live at 5 or 6, no 0 key) that should
// be migrated wholesale to the flow-ordered layout. A customized layout (has a
// "0", or "1" != preview:conv) returns false and is left alone.
func isPreZeroSessionsLayout(src Shortcuts) bool {
	sess, ok := src["sessions"]
	if !ok || sess.Left == nil {
		return false
	}
	if _, hasZero := sess.Left["0"]; hasZero {
		return false
	}
	return sess.Left["1"] == "preview:conv" &&
		(sess.Left["6"] == "preview:live" || sess.Left["5"] == "preview:live")
}

// flowOrderSessionsLeft is the canonical flow-ordered sessions.left layout.
func flowOrderSessionsLeft() ShortcutMap {
	return ShortcutMap{
		"0": "preview:live",
		"1": "preview:conv",
		"2": "preview:contexts",
		"3": "preview:agents",
		"4": "preview:tasks",
		"5": "preview:refs",
		"6": "preview:mem",
		"7": "preview:stats",
		"8": "preview:wf",
		"9": "preview:shells",
	}
}

// flowOrderConfigLeft is the canonical config.left layout: number keys 1-9 map
// to the config header tabs in order (ALL, MEMORY, SKILLS, AGENTS, COMMANDS,
// HOOKS, MCP, ENTERPRISE, PLUGINS).
func flowOrderConfigLeft() ShortcutMap {
	return ShortcutMap{
		"1": "page:all",
		"2": "page:memory",
		"3": "page:skills",
		"4": "page:agents",
		"5": "page:commands",
		"6": "page:hooks",
		"7": "page:mcp",
		"8": "page:enterprise",
		"9": "page:plugins",
	}
}

// isStaleConfigLayout reports whether the user config's config.left is the
// pre-tab layout (1=overview, 2=memory, 3=project, 4=skills, 5=hooks, 6=mcp)
// that should be migrated to the flow-ordered tab layout. A customized layout
// (has a "9", or "3" != page:project) is left alone.
func isStaleConfigLayout(src Shortcuts) bool {
	c, ok := src["config"]
	if !ok || c.Left == nil {
		return false
	}
	if _, hasNine := c.Left["9"]; hasNine {
		return false
	}
	return c.Left["3"] == "page:project"
}

// migrateShortcuts rewrites a stale pre-0 sessions layout in place to the
// flow-ordered layout. Kept for direct callers/tests; mergeShortcuts does the
// same detection against the user src before the merge clobbers it.
func migrateShortcuts(sc Shortcuts) {
	if isPreZeroSessionsLayout(sc) {
		sess := sc["sessions"]
		sess.Left = flowOrderSessionsLeft()
		sc["sessions"] = sess
	}
}

// rowSupportsPreviewModes reports whether the row under the cursor can honor a
// preview-mode shortcut.
//
// A date row, and a project row nested inside one, always render that scope's
// outputs pane: updateSessionPreview() routes them to updateDayPreview /
// updateDayProjectPreview without ever consulting sessPreviewMode. Firing a
// preview-mode shortcut there changed hidden state and repainted nothing, so
// the digits looked broken — and shortcutHint() advertised all ten of them
// anyway. Only sessions have preview modes, so only sessions get the digits.
//
// Plain project rows in the non-daily browser are deliberately excluded from
// this check: selectedSession() falls back to the project's most-recent session
// there, and refs/outputs modes preview it directly, so the digits do change
// what is on screen.
func (a *App) rowSupportsPreviewModes() bool {
	if a.state != viewSessions {
		return true
	}
	return !a.selectedOwnsDayPane()
}

// isPreviewModeCmd reports whether a command name is one of the preview-mode
// switches that only apply to a session row.
func isPreviewModeCmd(name string) bool {
	return strings.HasPrefix(name, "preview:")
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

	// Swallow rather than fall through: the digit is bound to a preview mode
	// the current row cannot show, and letting it reach the list would scroll
	// the cursor instead — a second surprise on top of the first.
	if isPreviewModeCmd(cmdName) && !a.rowSupportsPreviewModes() {
		return a, nil, true
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
		a.executionContextMenu || a.conv.execution.Focused ||
		a.viewsMenu || a.statsPageMenu || a.inspectorMenu || a.showHelp ||
		a.stateMenu
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

	// Build hint in key order (0-9); 0 is rendered first as the quick "live" key.
	previewOK := a.rowSupportsPreviewModes()
	var parts []string
	for _, i := range "0123456789" {
		key := string(i)
		if cmd, ok := sm[key]; ok {
			// A hint for a key that does nothing on this row is worse than no
			// hint: it is the footer promising a mode the row cannot render.
			if isPreviewModeCmd(cmd) && !previewOK {
				continue
			}
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
