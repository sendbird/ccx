package tui

import (
	"fmt"
	"strings"
)

// fmtHints builds a help line from alternating key, desc pairs.
// Example: fmtHints("↵", "open", "e", "edit", "q", "quit") → "↵:open e:edit q:quit"
func fmtHints(pairs ...string) string {
	var parts []string
	for i := 0; i+1 < len(pairs); i += 2 {
		parts = append(parts, displayKey(pairs[i])+":"+pairs[i+1])
	}
	return formatHelp(strings.Join(parts, " "))
}

// helpSuffix is the shared trailing hint for navigable footers: the help key
// and quit. The full key list lives in the "?" overlay.
func (a *App) helpSuffix() string {
	return fmtKey(a.keymap.Session.Help, "help") + " " + fmtKey(a.keymap.Session.Quit, "quit")
}

// --- Session view help ---

func (a *App) sessHelpLine() string {
	// Loading
	if a.sessionsLoading && len(a.sessions) == 0 {
		return formatHelp("loading… q:quit")
	}
	if len(a.sessions) == 0 {
		return formatHelp("q:quit")
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
		return formatHelp("p:page — pick a preview")
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

	// Pane proxy (live preview)
	if a.sessSplit.Focus && a.paneProxy != nil && a.sessPreviewMode == sessPreviewLive {
		return "  " + a.paneProxyIndicator() + " " + formatHelp("keys→pane ^G:jump ^N:newline ^Q:unfocus")
	}
	if a.paneProxy != nil && a.sessPreviewMode == sessPreviewLive && !a.sessSplit.Focus {
		return "  " + a.paneProxyIndicator() + " " + formatHelp("→:focus esc:close []:resize")
	}

	// Normal session list/preview — concise; full list is in the "?" overlay.
	sk := a.keymap.Session
	var h string
	if !a.sessSplit.Show {
		h = fmtKey(sk.Open, "open") + " " + fmtKey(sk.Actions, "actions") + " →:preview " + fmtKey(sk.Search, "search")
	} else if a.sessSplit.Focus {
		switch a.sessPreviewMode {
		case sessPreviewConversation:
			h = "↑↓:nav c:full " + fmtKey(sk.Open, "jump") + " ←:unfocus tab:mode"
		case sessPreviewAgents:
			h = "↑↓:nav " + fmtKey(sk.Open, "jump") + " ←:unfocus tab:mode"
		case sessPreviewWorkflows:
			h = "↑↓:agent ↵:transcript ←:unfocus tab:mode"
		case sessPreviewRefs:
			h = "↑↓:nav ↵:open sp:select ←:unfocus tab:mode"
		case sessPreviewContexts:
			h = "↑↓:node ↵:open ←:unfocus tab:mode"
		default:
			h = "↑↓:scroll ←:unfocus tab:mode"
		}
	} else {
		h = "↑↓:nav →:focus tab:mode ←:close"
	}
	h += " " + a.helpSuffix()
	return formatHelp(h)
}

// --- Conversation view help ---

func (a *App) convHelpLine(badges string) string {
	if a.executionContextMenu {
		return formatHelp("↵:jump to origin  esc:close")
	}
	if a.conv.execution.Focused {
		return formatHelp("↑↓:context ↵:open x:menu A/esc:back q:quit")
	}
	if a.conv.memorySearching {
		return "  " + a.conv.memorySearchTI.View() + helpStyle.Render("  enter:apply esc:cancel")
	}
	if a.conv.blockFiltering {
		return "  " + a.conv.blockFilterTI.View() + helpStyle.Render("  enter:apply esc:cancel")
	}

	sp := &a.conv.split
	h := interactionHelpText(a.conversationPrimaryHelpActions()...)
	escLabel := "sessions"
	if sp.Show {
		if sp.Focus {
			next := previewModeLabels[(a.conv.rightPaneMode+1)%len(previewModeLabels)]
			if a.conv.rightPaneMode == previewText {
				h = joinHelpSections(h, interactionHelpText(a.conversationPreviewTextHelpActions(next)...))
			} else {
				h = joinHelpSections(h, interactionHelpText(a.conversationPreviewStructuredHelpActions(next)...))
			}
			// On the Changes tab, `m` toggles per-occurrence vs per-file merge.
			if a.conv.inspector.Tab == inspectorChanges {
				h = joinHelpSections(h, interactionHelpText(labelAction("", "m", "merge")))
			}
		} else {
			h = joinHelpSections(h, interactionHelpText(a.conversationPreviewUnfocusedHelpActions("inspector")...))
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
		h = joinHelpSections(h, interactionHelpText(labelAction("", "esc", escLabel), resizeHelpAction(a)))
	} else if len(a.navStack) > 0 || a.conv.task.ID != "" || a.conv.cron.ID != "" {
		escLabel = "parent"
		h = joinHelpSections(h, interactionHelpText(a.conversationPreviewHiddenHelpActions()...), "esc:"+escLabel)
	} else {
		h = joinHelpSections(h, interactionHelpText(a.conversationPreviewHiddenHelpActions()...), "esc:"+escLabel)
	}

	if sp.Folds != nil && sp.Folds.BlockFilter != "" {
		vis := countVisibleBlocks(sp.Folds.BlockVisible)
		total := len(sp.Folds.Entry.Content)
		filterInfo := filterBadge.Render(fmt.Sprintf(" [%d/%d] %s", vis, total, sp.Folds.BlockFilter))
		return filterInfo + " " + badges + formatHelp(joinHelpSections(h, a.helpSuffix()))
	}
	return badges + formatHelp(joinHelpSections(h, a.helpSuffix()))
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
		h := "↵:edit e:edit /:search esc:back"
		if a.cfgSearchTerm != "" {
			badge := fmt.Sprintf("[%d/%d]", a.cfgSearchIdx+1, len(a.cfgSearchMatch))
			if len(a.cfgSearchMatch) == 0 {
				badge = "[0/0]"
			}
			return "  " + filterBadge.Render(badge) + formatHelp(" n/N:next/prev esc:clear-search")
		}
		if a.cfgSplit.Show && a.cfgSplit.Focus {
			h = "↑↓:scroll esc:unfocus"
		}
		h += " " + a.helpSuffix()
		return formatHelp(h)
	}
	if a.cfgSearchTerm != "" {
		badge := fmt.Sprintf("[%d/%d]", a.cfgSearchIdx+1, len(a.cfgSearchMatch))
		if len(a.cfgSearchMatch) == 0 {
			badge = "[0/0]"
		}
		return "  " + filterBadge.Render(badge) + formatHelp(" n/N:next/prev esc:clear")
	}

	h := "↵:open x:actions " + a.keymap.Session.Search + ":search " + a.keymap.Session.Views + ":views"
	if a.cfgHasSelection() {
		h = "sp:sel x:actions esc:clear"
	}
	if a.cfgSplit.Show {
		if a.cfgSplit.Focus {
			h = "↑↓:scroll esc:unfocus"
		} else {
			h = "↑↓:nav →:focus x:actions " + a.keymap.Session.Views + ":views"
		}
	}
	h += " " + a.helpSuffix()
	var badges string
	if fl := a.cfgFilterLabel(); fl != "" {
		badges += filterBadge.Render(fl) + " "
	}
	if a.cfgHasSelection() {
		badges += filterBadge.Render(fmt.Sprintf("%d selected", len(a.cfgSelectedSet))) + " "
	}
	return "  " + badges + formatHelp(h)
}

// --- Plugins view help ---

func (a *App) pluginsHelpLine() string {
	if a.plgDetailActive {
		h := "↑↓:nav →:preview x:actions e:edit esc:back"
		if a.plgDetailSplit.Show && a.plgDetailSplit.Focus {
			h = "↑↓:scroll ←:unfocus"
		}
		return "  " + formatHelp(h+" "+a.helpSuffix())
	}
	if a.plgSearching {
		return "  " + a.plgSearchInput.View() + helpStyle.Render("  enter:apply esc:cancel")
	}
	if a.plgSearchTerm != "" {
		return "  " + filterBadge.Render(a.plgSearchTerm) + formatHelp(" n/N:next/prev esc:clear")
	}
	h := "↵:open →:preview x:actions " + a.keymap.Session.Search + ":search"
	if a.plgSplit.Show && a.plgSplit.Focus {
		h = "↑↓:scroll ←:unfocus"
	}
	h += " " + a.helpSuffix()
	if a.plgHasSelection() {
		badges := filterBadge.Render(fmt.Sprintf("%d sel", len(a.plgSelectedSet)))
		return "  " + badges + formatHelp(" "+h)
	}
	return "  " + formatHelp(h)
}
