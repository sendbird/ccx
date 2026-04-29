package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/tmux"
)

type interactionActionID string

const (
	interactionActionURLs    interactionActionID = "urls"
	interactionActionFiles   interactionActionID = "files"
	interactionActionChanges interactionActionID = "changes"
	interactionActionCopy    interactionActionID = "copy"
)

type interactionAction struct {
	ID       interactionActionID
	Key      string
	KeyLabel string
	Label    string
	Enabled  bool
}

func bindAction(id interactionActionID, key, label string) interactionAction {
	return interactionAction{
		ID:       id,
		Key:      key,
		KeyLabel: displayKey(key),
		Label:    label,
		Enabled:  key != "",
	}
}

func labelAction(id interactionActionID, keyLabel, label string) interactionAction {
	return interactionAction{
		ID:       id,
		KeyLabel: keyLabel,
		Label:    label,
		Enabled:  keyLabel != "",
	}
}

func interactionHelpText(actions ...interactionAction) string {
	parts := make([]string, 0, len(actions))
	for _, action := range actions {
		if !action.Enabled || action.KeyLabel == "" {
			continue
		}
		if action.Label == "" {
			parts = append(parts, action.KeyLabel)
			continue
		}
		parts = append(parts, action.KeyLabel+":"+action.Label)
	}
	return strings.Join(parts, " ")
}

func joinHelpSections(parts ...string) string {
	sections := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		sections = append(sections, part)
	}
	return strings.Join(sections, " ")
}

func renderInteractionLine(actions ...interactionAction) string {
	hl := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	d := dimStyle
	parts := make([]string, 0, len(actions))
	for _, action := range actions {
		if !action.Enabled || action.KeyLabel == "" {
			continue
		}
		if action.Label == "" {
			parts = append(parts, hl.Render(action.KeyLabel))
			continue
		}
		parts = append(parts, hl.Render(action.KeyLabel)+d.Render(":"+action.Label))
	}
	return strings.Join(parts, "  ")
}

func renderInteractionHintBox(lines [][]interactionAction, footer string) string {
	rendered := make([]string, 0, len(lines)+1)
	for _, line := range lines {
		if text := renderInteractionLine(line...); text != "" {
			rendered = append(rendered, text)
		}
	}
	if footer != "" {
		rendered = append(rendered, dimStyle.Render(footer))
	}
	body := strings.Join(rendered, "\n")
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorDim).
		Padding(0, 1)
	return boxStyle.Render(body)
}

func interactionKeyMatches(actions []interactionAction, key string, id interactionActionID) bool {
	for _, action := range actions {
		if action.ID == id && action.Enabled && action.Key == key {
			return true
		}
	}
	return false
}

func resizeHelpAction(a *App) interactionAction {
	return labelAction("", displayKey(a.keymap.Session.ResizeShrink)+displayKey(a.keymap.Session.ResizeGrow), "resize")
}

func foldAllHelpAction(a *App) interactionAction {
	return labelAction("", displayKey(a.keymap.Preview.FoldAll)+"/"+displayKey(a.keymap.Preview.ExpandAll), "all")
}

func copySelectHelpAction(a *App) interactionAction {
	return labelAction("", displayKey(a.keymap.Preview.CopyMode)+"/sp", "sel")
}

func copyConfirmHelpAction(a *App) interactionAction {
	return labelAction("", displayKey(a.keymap.Preview.CopyAll)+"/↵", "copy")
}

func (a *App) conversationPrimaryHelpActions() []interactionAction {
	actions := []interactionAction{
		bindAction("", a.keymap.Session.Open, "open"),
		bindAction("", a.keymap.Conversation.Edit, "edit"),
		labelAction("", "p", "page"),
		bindAction("", a.keymap.Conversation.Actions, "actions"),
		bindAction("", a.keymap.Conversation.LiveToggle, "live"),
		bindAction("", a.keymap.Session.Refresh, "refresh"),
	}
	if a.config.TmuxEnabled && tmux.InTmux() && a.currentSess.IsLive {
		actions = append(actions,
			bindAction("", a.keymap.Conversation.Input, "input"),
			bindAction("", a.keymap.Conversation.JumpToTree, "jump"),
		)
	}
	return actions
}

func (a *App) conversationPreviewTextHelpActions(next string) []interactionAction {
	return []interactionAction{
		labelAction("", "↑↓", "scroll"),
		bindAction("", a.keymap.Preview.CopyMode, "copy"),
		labelAction("", "tab", next),
	}
}

func (a *App) conversationPreviewStructuredHelpActions(next string) []interactionAction {
	return []interactionAction{
		labelAction("", "↑↓", "blocks"),
		labelAction("", "←→", "fold"),
		foldAllHelpAction(a),
		bindAction("", a.keymap.Preview.Filter, "filter"),
		labelAction("", "tab", next),
	}
}

func (a *App) conversationPreviewUnfocusedHelpActions(next string) []interactionAction {
	return []interactionAction{
		labelAction("", "tab", next),
		bindAction("", a.keymap.Session.Right, "focus"),
	}
}

func (a *App) conversationPreviewHiddenHelpActions() []interactionAction {
	return []interactionAction{
		labelAction("", "tab", "preview"),
		bindAction("", a.keymap.Session.Right, "preview"),
	}
}

func (a *App) conversationActionMenuActions() []interactionAction {
	return []interactionAction{
		bindAction(interactionActionURLs, a.keymap.Actions.URLs, "urls"),
		bindAction(interactionActionFiles, a.keymap.Actions.Files, "files"),
		bindAction(interactionActionChanges, a.keymap.Actions.Changes, "changes"),
		bindAction(interactionActionCopy, a.keymap.Actions.Copy, "copy"),
	}
}

func (a *App) messageFullCopyModeHelpActions() []interactionAction {
	return []interactionAction{
		labelAction("", "↑↓", "move"),
		copySelectHelpAction(a),
		copyConfirmHelpAction(a),
		labelAction("", "home/end", ""),
		labelAction("", "esc", "cancel"),
	}
}

func (a *App) messageFullAllMessagesHelpActions() []interactionAction {
	return []interactionAction{
		labelAction("", "↑↓", "scroll"),
		bindAction("", a.keymap.Preview.CopyMode, "copy"),
		bindAction("", a.keymap.Preview.CopyAll, "all"),
		bindAction("", a.keymap.Conversation.Actions, "actions"),
		labelAction("", "/", "search"),
	}
}

func (a *App) messageFullSelectedHelpActions() []interactionAction {
	return []interactionAction{
		labelAction("", "↑↓", "blocks"),
		labelAction("", "sp", "select"),
		bindAction("", a.keymap.Preview.CopyAll, "copy"),
		labelAction("", "esc", "clear"),
	}
}

func (a *App) messageFullSearchHelpActions() []interactionAction {
	return []interactionAction{
		labelAction("", "n/N", "match"),
		labelAction("", "↑↓", "blocks"),
		labelAction("", "←→", "fold"),
		labelAction("", "sp", "select"),
		foldAllHelpAction(a),
		bindAction("", a.keymap.Preview.CopyMode, "copy"),
		bindAction("", a.keymap.Preview.CopyAll, "all"),
	}
}

func (a *App) messageFullDetailHelpActions() []interactionAction {
	return []interactionAction{
		labelAction("", "↑↓", "blocks"),
		labelAction("", "←→", "fold"),
		labelAction("", "n/N", "msg"),
		foldAllHelpAction(a),
		bindAction("", a.keymap.Preview.CopyMode, "copy"),
		bindAction("", a.keymap.Preview.CopyAll, "all"),
		bindAction("", a.keymap.Conversation.Actions, "actions"),
		bindAction("", a.keymap.Preview.Filter, "filter"),
	}
}

func (a *App) messageFullSearchHintActions() []interactionAction {
	return []interactionAction{
		labelAction("", "n/N", "next/prev match after search"),
	}
}
