package tui

import (
	"fmt"
	"strings"

	"github.com/sendbird/ccx/internal/tmux"
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

	// Normal session list/preview
	sk := a.keymap.Session
	h := fmtKey(sk.Open, "open") + " " + fmtKey(sk.Edit, "edit") + " " + fmtKey(sk.Actions, "actions") + " " + fmtKey(sk.Views, "views") + " " + fmtKey(sk.Refresh, "refresh")
	if a.notifyUnreadCount() > 0 {
		h += " n:notify"
	}
	if a.config.PickMode {
		h = fmtKey(sk.Pick, "pick") + " " + h
	}
	if !a.sessSplit.Show {
		h += " g/G:top/end →:preview tab/S-tab:preview"
	} else if a.sessSplit.Focus {
		switch a.sessPreviewMode {
		case sessPreviewConversation:
			h += " ↑↓:nav c:full " + fmtKey(sk.Actions, "actions") + " " + fmtKey(sk.Open, "jump") + " ←:unfocus /:search tab:mode"
		case sessPreviewAgents:
			h += " ↑↓:nav " + fmtKey(sk.Open, "jump") + " ←:unfocus tab:mode"
		case sessPreviewWorkflows:
			h += " ↑↓:agent ↵:transcript ←:unfocus tab:mode"
		case sessPreviewRefs:
			h += " ↑↓:nav ↵:open sp:select y:copy ←:unfocus tab:mode"
		default:
			h += " ↑↓:scroll ←:unfocus tab:mode"
		}
		if a.sessPreviewMode != sessPreviewConversation {
			h += " esc:messages"
		} else {
			h += " esc:list"
		}
		h += " " + displayKey(sk.ResizeShrink) + displayKey(sk.ResizeGrow) + ":resize"
	} else {
		h += " g/G:top/end tab/S-tab:preview →:focus ←:close "
		if a.sessPreviewMode != sessPreviewConversation {
			h += "esc:messages "
		} else {
			h += "esc:close "
		}
		h += displayKey(sk.ResizeShrink) + displayKey(sk.ResizeGrow) + ":resize"
	}
	if a.config.TmuxEnabled && tmux.InTmux() {
		h += " " + fmtKey(sk.Live, "live") + " " + fmtKey(sk.Switch, "switch")
	}
	if sc := a.shortcutHint(); sc != "" {
		h += " " + dimStyle.Render(sc)
	}
	h += " D:completed " + fmtKey(sk.Search, "search") + " " + fmtKey(sk.Help, "help") + " " + fmtKey(sk.Quit, "quit")
	return formatHelp(h)
}

// --- Conversation view help ---

func (a *App) convHelpLine(badges string) string {
	if a.executionContextMenu {
		return formatHelp("↵:jump to origin  esc:close")
	}
	if a.conv.execution.Focused {
		return formatHelp("↑↓/jk:context ↵/→:open x:menu K:up-region A/esc:back q:quit")
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
		} else {
			h = joinHelpSections(h, interactionHelpText(a.conversationPreviewUnfocusedHelpActions("inspector")...))
		}
		switch {
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
		return filterInfo + " " + badges + formatHelp(joinHelpSections(h, "/:search", "q:quit"))
	}
	return badges + formatHelp(joinHelpSections(h, "/:search", "q:quit"))
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
	if a.cfgSearchTerm != "" {
		badge := fmt.Sprintf("[%d/%d]", a.cfgSearchIdx+1, len(a.cfgSearchMatch))
		if len(a.cfgSearchMatch) == 0 {
			badge = "[0/0]"
		}
		return "  " + filterBadge.Render(badge) + formatHelp(" n/N:next/prev esc:clear")
	}

	h := "sp:sel x:actions p:page tab:filter P:project a:new /:search " + a.keymap.Session.Refresh + ":refresh v:views q:quit"
	if a.cfgHasSelection() {
		h = "sp:sel x:actions p:page tab:filter esc:clear q:quit"
	}
	if a.cfgSplit.Show {
		if a.cfgSplit.Focus {
			h = "↑↓:scroll esc:unfocus q:quit"
		} else if a.cfgHasSelection() {
			h = "↑↓:nav →:focus sp:sel x:actions p:page esc:clear q:quit"
		} else {
			h = "↑↓:nav →:focus sp:sel x:actions p:page tab:filter P:project a:new v:views q:quit"
		}
	}
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
		h := "↑↓:nav →:preview sp:sel x:actions e:edit c:copy-path o:shell esc:back q:quit"
		if a.plgDetailSplit.Show && a.plgDetailSplit.Focus {
			h = "↑↓:scroll ←:unfocus q:quit"
		}
		return "  " + formatHelp(h)
	}
	if a.plgSearching {
		return "  " + a.plgSearchInput.View() + helpStyle.Render("  enter:apply esc:cancel")
	}
	if a.plgSearchTerm != "" {
		return "  " + filterBadge.Render(a.plgSearchTerm) + formatHelp(" n/N:next/prev esc:clear")
	}
	h := "↑↓:nav ↵:open →:preview sp:select x:actions /:search " + a.keymap.Session.Refresh + ":refresh v:views esc:back q:quit"
	if a.plgSplit.Show && a.plgSplit.Focus {
		h = "↑↓:scroll ←:unfocus q:quit"
	}
	if a.plgHasSelection() {
		badges := filterBadge.Render(fmt.Sprintf("%d sel", len(a.plgSelectedSet)))
		return "  " + badges + formatHelp(" "+h)
	}
	return "  " + formatHelp(h)
}
