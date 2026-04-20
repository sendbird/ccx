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
	if a.config.PickMode {
		h = fmtKey(sk.Pick, "pick") + " " + h
	}
	if !a.sessSplit.Show {
		h += " →:preview tab:group"
	} else if a.sessSplit.Focus {
		switch a.sessPreviewMode {
		case sessPreviewConversation:
			h += " ↑↓:nav c:full " + fmtKey(sk.Open, "jump") + " ←:unfocus /:search tab:mode"
		case sessPreviewAgents:
			h += " ↑↓:nav " + fmtKey(sk.Open, "jump") + " ←:unfocus tab:mode"
		default:
			h += " ↑↓:scroll ←:unfocus tab:mode"
		}
		h += " " + displayKey(sk.ResizeShrink) + displayKey(sk.ResizeGrow) + ":resize"
	} else {
		h += " tab:group →:focus ←:close " + displayKey(sk.ResizeShrink) + displayKey(sk.ResizeGrow) + ":resize"
	}
	if a.config.TmuxEnabled && tmux.InTmux() {
		h += " " + fmtKey(sk.Live, "live")
	}
	if sc := a.shortcutHint(); sc != "" {
		h += " " + dimStyle.Render(sc)
	}
	h += " " + fmtKey(sk.Search, "search") + " " + fmtKey(sk.Help, "help") + " " + fmtKey(sk.Quit, "quit")
	return formatHelp(h)
}

// --- Conversation view help ---

func (a *App) convHelpLine(badges string) string {
	if a.conv.blockFiltering {
		return "  " + a.conv.blockFilterTI.View() + helpStyle.Render("  enter:apply esc:cancel")
	}

	sp := &a.conv.split
	h := interactionHelpText(a.conversationPrimaryHelpActions()...)
	if sp.Show {
		if sp.Focus {
			next := previewModeLabels[(a.conv.rightPaneMode+1)%len(previewModeLabels)]
			if a.conv.rightPaneMode == previewText {
				h = joinHelpSections(h, interactionHelpText(a.conversationPreviewTextHelpActions(next)...))
			} else {
				h = joinHelpSections(h, interactionHelpText(a.conversationPreviewStructuredHelpActions(next)...))
			}
		} else {
			next := convPaneModeLabels[(a.conv.leftPaneMode+1)%len(convPaneModeLabels)]
			h = joinHelpSections(h, interactionHelpText(a.conversationPreviewUnfocusedHelpActions(next)...))
		}
		h = joinHelpSections(h, interactionHelpText(labelAction("", "esc", "close"), resizeHelpAction(a)))
	} else {
		h = joinHelpSections(h, interactionHelpText(a.conversationPreviewHiddenHelpActions()...))
	}

	if sp.Folds != nil && sp.Folds.BlockFilter != "" {
		vis := countVisibleBlocks(sp.Folds.BlockVisible)
		total := len(sp.Folds.Entry.Content)
		filterInfo := filterBadge.Render(fmt.Sprintf(" [%d/%d] %s", vis, total, sp.Folds.BlockFilter))
		return filterInfo + " " + badges + formatHelp(joinHelpSections(h, "/:search", "esc:back", "q:quit"))
	}
	return badges + formatHelp(joinHelpSections(h, "/:search", "esc:back", "q:quit"))
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

// --- Message-full view help ---

func (a *App) msgFullHelpLine() string {
	if a.msgFull.blockFiltering {
		return "  " + a.msgFull.blockFilterTI.View() + helpStyle.Render("  enter:apply esc:cancel")
	}
	if a.msgFull.searching {
		return "  " + a.msgFull.searchInput.View() + helpStyle.Render("  enter:search esc:cancel")
	}
	if a.msgFull.allMessages {
		if a.copyModeActive {
			return formatHelp(joinHelpSections("all messages", interactionHelpText(a.messageFullCopyModeHelpActions()...)))
		}
		h := joinHelpSections("all messages", interactionHelpText(a.messageFullAllMessagesHelpActions()...))
		if a.msgFull.searchTerm != "" {
			h = joinHelpSections(h, fmt.Sprintf("[%d/%d]", a.msgFull.searchIdx+1, len(a.msgFull.searchLines)), "n/N:match")
		}
		return formatHelp(joinHelpSections(h, "esc:back", "q:quit"))
	}

	pos := fmt.Sprintf("#%d/%d", a.msgFull.idx+1, len(a.msgFull.merged))
	if a.copyModeActive {
		return formatHelp(joinHelpSections(pos, interactionHelpText(a.messageFullCopyModeHelpActions()...)))
	}

	var h string
	selCount := len(a.msgFull.folds.Selected)
	switch {
	case selCount > 0:
		h = joinHelpSections(pos, fmt.Sprintf("[%d sel]", selCount), interactionHelpText(a.messageFullSelectedHelpActions()...))
	case a.msgFull.searchTerm != "":
		h = joinHelpSections(pos, fmt.Sprintf("[%d/%d]", a.msgFull.searchIdx+1, len(a.msgFull.searchLines)), interactionHelpText(a.messageFullSearchHelpActions()...))
	default:
		h = joinHelpSections(pos, interactionHelpText(a.messageFullDetailHelpActions()...))
	}

	if a.msgFull.folds.BlockFilter != "" {
		vis := countVisibleBlocks(a.msgFull.folds.BlockVisible)
		total := len(a.msgFull.folds.Entry.Content)
		filterInfo := filterBadge.Render(fmt.Sprintf(" [%d/%d] %s", vis, total, a.msgFull.folds.BlockFilter))
		return filterInfo + " " + formatHelp(joinHelpSections(h, "esc:back", "q:quit"))
	}
	return formatHelp(joinHelpSections(h, "esc:back", "q:quit"))
}
