package tui

import (
	"fmt"
)

// --- Session view help ---

func (a *App) sessHelpLine() string {
	// Loading
	if a.sessionsLoading && len(a.sessions) == 0 {
		return a.footer("  " + helpStyle.Render("loading…"))
	}
	if len(a.sessions) == 0 {
		return a.footer("")
	}

	// Modal overlays
	if a.confirmMsg != "" {
		return formatHelp("y:confirm  any:cancel")
	}
	if a.sessConvFullText != "" {
		return formatHelp("↑↓:scroll pgup/pgdn:page esc/c:close")
	}
	if a.showHelp {
		return formatHelp("press any key to close")
	}
	if a.tagMenu {
		return "" // Tag menu has its own help
	}
	if a.sessPageMenu {
		return menuFooter(a.bind("session", "page_menu"), "pick a preview")
	}
	if a.moveMode {
		return "  " + a.moveInput.View() + helpStyle.Render("  enter:move esc:cancel")
	}
	if a.worktreeMode {
		hint := "  enter:create esc:cancel"
		if a.worktreeNewMode {
			hint = "  enter:new session (empty=main) esc:cancel"
		}
		return "  " + a.worktreeInput.View() + helpStyle.Render(hint)
	}
	if a.sessConvSearching {
		return "  " + a.sessConvSearchInput.View() + helpStyle.Render("  enter:apply esc:cancel")
	}
	if a.dayOutputSearching {
		return "  " + a.dayOutputSearchTI.View() + helpStyle.Render("  enter:apply esc:cancel")
	}

	// Pane proxy (live preview)
	if a.sessSplit.Focus && a.paneProxy != nil && a.sessPreviewMode == sessPreviewLive {
		return "  " + a.paneProxyIndicator() + " " + formatHelp("keys→pane ^G:jump ^N:newline ^Q:unfocus")
	}
	if a.paneProxy != nil && a.sessPreviewMode == sessPreviewLive && !a.sessSplit.Focus {
		return a.footer("  "+a.paneProxyIndicator(), a.bind("session", "right"), a.bindAs("session", "escape", "close"), a.resizeHint())
	}

	// Normal session list/preview — concise; full list is in the "?" overlay.
	// Hints are in priority order; footer() drops from the end when narrow.
	nav, unfocus, mode, page := fh("↑↓", "nav"), a.bindAs("session", "left", "unfocus"), a.bind("session", "preview"), a.bind("session", "page_menu")
	var hints []footerHint
	switch {
	case !a.sessSplit.Show:
		hints = []footerHint{a.bind("session", "open"), a.bind("session", "actions"), a.bindAs("session", "right", "preview"), page, a.bind("session", "search")}
	case a.sessSplit.Focus:
		switch {
		case a.outputsPreviewActionsActive():
			// Both digests: the row under the cursor is what the keys act on.
			hints = []footerHint{nav, a.bindAs("session", "open", "jump"), fh("o", "open"), a.bind("session", "actions")}
			if a.selectedOwnsDayPane() {
				// Only the day pane is tabbed by kind — the per-session digest
				// has one fixed list, so advertising it there would mislead.
				hints = append(hints, fh("1-9/tab", "kind"))
			}
			hints = append(hints, unfocus, page)
		case a.sessPreviewMode == sessPreviewConversation:
			hints = []footerHint{nav, fh("c", "full"), a.bindAs("session", "open", "jump"), unfocus, mode}
		case a.sessPreviewMode == sessPreviewAgents:
			hints = []footerHint{nav, a.bindAs("session", "open", "jump"), unfocus, mode}
		case a.sessPreviewMode == sessPreviewWorkflows:
			hints = []footerHint{fh("↑↓", "agent"), a.bindAs("session", "open", "transcript"), unfocus, mode}
		case a.sessPreviewMode == sessPreviewRefs:
			hints = []footerHint{nav, a.bind("session", "open"), a.bind("session", "select"), unfocus, mode}
		case a.sessPreviewMode == sessPreviewContexts:
			hints = []footerHint{fh("↑↓", "node"), a.bind("session", "open"), unfocus, mode}
		default:
			hints = []footerHint{fh("↑↓", "scroll"), unfocus, mode}
		}
	default:
		hints = []footerHint{nav, a.bind("session", "right"), mode, page, a.bindAs("session", "left", "close")}
	}
	return a.footer("", hints...)
}

// resizeHint is the split's shrink/grow/reset hint ("<>=:resize"). It goes
// last in a footer, so a narrow footer drops it first; "[ ]" stay aliases
// and are listed in the "?" overlay.
func (a *App) resizeHint() footerHint {
	km := a.keymap
	k := displayKey(km.Session.SplitShrink) + displayKey(km.Session.SplitGrow) + displayKey(km.Session.SplitReset)
	return footerHint{key: k, label: "resize"}
}

// splitResizeKeys is the "?" overlay spelling of the split keys ("< / > / =").
func splitResizeKeys(km Keymap) string {
	return displayKey(km.Session.SplitShrink) + " / " + displayKey(km.Session.SplitGrow) + " / " + displayKey(km.Session.SplitReset)
}

// splitHelpRow is the "?" overlay row for the split keys in a split view.
func (a *App) splitHelpRow() helpRow {
	return helpRow{splitResizeKeys(a.keymap), "Shrink / grow list, reset split (or drag the divider)"}
}

// --- Conversation view help ---

func (a *App) convHelpLine(badges string) string {
	if a.executionContextMenu {
		return formatHelp("↵:jump to origin  esc:close")
	}
	if a.conv.execution.Focused {
		back := a.bindAs("conversation", "execution_contexts", "back")
		if back.key != "" {
			back.key += "/" + displayKey(a.keymap.Session.Escape)
		}
		return a.footer("", fh("↑↓", "context"), a.bind("session", "open"), a.bindAs("conversation", "actions", "menu"), back)
	}
	if a.conv.memorySearching {
		return "  " + a.conv.memorySearchTI.View() + helpStyle.Render("  enter:apply esc:cancel")
	}
	if a.conv.blockFiltering {
		return "  " + a.conv.blockFilterTI.View() + helpStyle.Render("  enter:apply esc:cancel")
	}

	sp := &a.conv.split
	hints := hintsFromActions(a.conversationPrimaryHelpActions()...)
	escLabel := "sessions"
	if sp.Show {
		if sp.Focus {
			next := previewModeLabels[(a.conv.rightPaneMode+1)%len(previewModeLabels)]
			var pane []footerHint
			if a.conv.rightPaneMode == previewText {
				pane = hintsFromActions(a.conversationPreviewTextHelpActions(next)...)
			} else {
				pane = hintsFromActions(a.conversationPreviewStructuredHelpActions(next)...)
			}
			// On the Changes tab, `m` toggles per-occurrence vs per-file merge.
			if a.conv.inspector.Tab == inspectorChanges {
				pane = append(pane, fh("m", "merge"))
			}
			// The focused pane's keys come first: a narrow footer drops the
			// list-level hints before them.
			hints = append(pane, hints...)
		} else {
			hints = append(hints, hintsFromActions(a.conversationPreviewUnfocusedHelpActions("inspector")...)...)
		}
		switch {
		case a.conv.inspector.MemorySearch != "":
			escLabel = "clear search"
		case sp.Folds != nil && sp.Folds.BlockFilter != "":
			escLabel = "clear filter"
		case len(a.conv.inspector.History) > 0 || a.conv.inspector.Zoom || a.conv.inspector.MetaDrill != "" || a.conv.inspector.MetaPlanDrill != "":
			escLabel = "back"
		case sp.Focus:
			escLabel = "list"
		case len(a.navStack) > 0 || a.conv.task.ID != "" || a.conv.cron.ID != "":
			escLabel = "parent"
		default:
			escLabel = "close"
		}
		// esc is how the user leaves this view: a narrow footer drops the
		// optional hints before it.
		hints = append(hints, a.bindAs("session", "escape", escLabel).keep(), a.resizeHint())
	} else {
		if len(a.navStack) > 0 || a.conv.task.ID != "" || a.conv.cron.ID != "" {
			escLabel = "parent"
		}
		hints = append(hints, hintsFromActions(a.conversationPreviewHiddenHelpActions()...)...)
		hints = append(hints, a.bindAs("session", "escape", escLabel).keep())
	}

	prefix := badges
	if sp.Folds != nil && sp.Folds.BlockFilter != "" {
		vis := countVisibleBlocks(sp.Folds.BlockVisible)
		total := len(sp.Folds.Entry.Content)
		prefix = filterBadge.Render(fmt.Sprintf(" [%d/%d] %s", vis, total, sp.Folds.BlockFilter)) + " " + badges
	}
	return a.footer(prefix, hints...)
}

// --- Config view help ---

func (a *App) configHelpLine() string {
	if a.cfgProjectPicker {
		return formatHelp("/:filter ↵:select esc:cancel")
	}
	if a.cfgNaming {
		return "  " + a.cfgNamingInput.View() + helpStyle.Render("  enter:create esc:cancel")
	}
	if a.cfgSearching {
		return "  " + a.cfgSearchInput.View() + helpStyle.Render("  enter:apply esc:cancel")
	}
	if a.cfgSkillBrowse {
		if a.cfgSearchTerm != "" {
			badge := fmt.Sprintf("[%d/%d]", a.cfgSearchIdx+1, len(a.cfgSearchMatch))
			if len(a.cfgSearchMatch) == 0 {
				badge = "[0/0]"
			}
			return "  " + filterBadge.Render(badge) + formatHelp(" n/b:next/prev esc:clear-search")
		}
		if a.cfgSplit.Show && a.cfgSplit.Focus {
			return a.footer("", fh("↑↓", "scroll"), fh("esc", "unfocus"))
		}
		return a.footer("", fh("↵", "edit"), fh("e", "edit"), a.bind("session", "search"), fh("esc", "back"))
	}
	if a.cfgPluginsPage {
		// PLUGINS page reuses the plugins help line plus page-cycling hints.
		return a.pluginsHelpLine()
	}
	if a.cfgSearchTerm != "" {
		badge := fmt.Sprintf("[%d/%d]", a.cfgSearchIdx+1, len(a.cfgSearchMatch))
		if len(a.cfgSearchMatch) == 0 {
			badge = "[0/0]"
		}
		return "  " + filterBadge.Render(badge) + formatHelp(" n/b:next/prev esc:clear")
	}

	actions, page, views := a.bind("session", "actions"), fh("[]", "page"), a.bind("session", "views")
	hints := []footerHint{fh("↵", "open"), actions, page, a.bind("session", "search"), views}
	if a.cfgHasSelection() {
		hints = []footerHint{fh("sp", "sel"), actions, page, fh("esc", "clear")}
	}
	if a.cfgSplit.Show {
		if a.cfgSplit.Focus {
			hints = []footerHint{fh("↑↓", "scroll"), fh("esc", "unfocus")}
		} else {
			hints = []footerHint{fh("↑↓", "nav"), fh("→", "focus"), actions, page, views}
		}
	}
	prefix := "  "
	if a.cfgHasSelection() {
		prefix += filterBadge.Render(fmt.Sprintf("%d selected", len(a.cfgSelectedSet))) + " "
	}
	return a.footer(prefix, hints...)
}

// --- Plugins view help ---

func (a *App) pluginsHelpLine() string {
	if a.plgDetailActive {
		if a.plgDetailSplit.Show && a.plgDetailSplit.Focus {
			return a.footer("  ", fh("↑↓", "scroll"), fh("←", "unfocus"))
		}
		return a.footer("  ", fh("↑↓", "nav"), fh("→", "preview"), a.bind("session", "actions"), fh("e", "edit"), fh("esc", "back"))
	}
	if a.plgSearching {
		return "  " + a.plgSearchInput.View() + helpStyle.Render("  enter:apply esc:cancel")
	}
	if a.plgSearchTerm != "" {
		return "  " + filterBadge.Render(a.plgSearchTerm) + formatHelp(" n/b:next/prev esc:clear")
	}
	hints := []footerHint{a.bind("session", "open"), fh("→", "preview"), a.bind("session", "actions"), fh("[]", "page"), a.bind("session", "search")}
	if a.plgSplit.Show && a.plgSplit.Focus {
		hints = []footerHint{fh("↑↓", "scroll"), fh("←", "unfocus"), fh("[]", "page")}
	}
	prefix := "  "
	if a.plgHasSelection() {
		prefix += filterBadge.Render(fmt.Sprintf("%d sel", len(a.plgSelectedSet))) + " "
	}
	return a.footer(prefix, hints...)
}
