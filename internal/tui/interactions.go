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

func (a *App) conversationEnterHelpAction() interactionAction {
	action := bindAction("", a.keymap.Session.Open, "open")
	item, ok := a.selectedConversationItem()
	if !ok {
		action.Enabled = false
		return action
	}
	if !a.conv.split.Show || !a.conv.split.Focus {
		return action
	}

	action.Enabled = false
	if item.kind == convSessionMeta {
		if target, ok := a.currentMetaTarget(); ok {
			switch target.kind {
			case metaTargetMemoryFile:
				if a.conv.inspector.MetaDrill == "" {
					action.Enabled = target.fileName != ""
					action.Label = "open"
				} else {
					action.Enabled = target.messageUUID != "" || target.entryIndex >= 0
					action.Label = "jump"
				}
			case metaTargetTodo:
				action.Enabled = target.messageUUID != "" || target.entryIndex >= 0
				action.Label = "jump"
			case metaTargetTask:
				action.Enabled = target.messageUUID != "" || target.entryIndex >= 0
				if task, ok := a.taskByID(target.taskID); ok {
					if _, _, visible := a.taskConversationData(task); visible {
						action.Enabled = true
						action.Label = "open"
					} else {
						action.Label = "jump"
					}
				} else {
					action.Label = "jump"
				}
			case metaTargetPlan:
				action.Enabled = a.conv.inspector.MetaPlanDrill == "" && target.planKey != ""
				action.Label = "open"
			case metaTargetScratchpad:
				action.Enabled = target.filePath != ""
				action.Label = "open"
			case metaTargetDecision:
				action.Enabled = target.messageUUID != "" || target.entryIndex >= 0
				action.Label = "jump"
			}
		}
		return action
	}
	if a.conv.split.Folds == nil {
		return action
	}
	bc := a.conv.split.Folds.BlockCursor
	entry := a.conv.split.Folds.Entry
	if bc < 0 || bc >= len(entry.Content) {
		return action
	}
	block := entry.Content[bc]
	action.Enabled = block.Type == "image" && block.ImagePasteID > 0
	if block.Type == "tool_use" && (block.ToolName == "Agent" || block.ToolName == "Task") {
		_, action.Enabled = a.findAgentForToolUse(block.ID)
		action.Label = "open"
	}
	return action
}

func (a *App) conversationPrimaryHelpActions() []interactionAction {
	regionLabel := "resources"
	if a.conv.contextActive {
		regionLabel = "conversation"
	}
	actions := []interactionAction{
		a.conversationEnterHelpAction(),
		bindAction("", a.keymap.Conversation.SwitchRegion, regionLabel),
		bindAction("", a.keymap.Conversation.ExecutionContexts, "contexts"),
		bindAction("", a.keymap.Conversation.Edit, "edit"),
		labelAction("", "p", "page"),
		bindAction("", a.keymap.Conversation.Actions, "actions"),
		bindAction("", a.keymap.Conversation.LiveToggle, "live"),
		bindAction("", a.keymap.Session.Refresh, "refresh"),
	}
	item, isMeta := a.selectedConversationItem()
	if isMeta && item.kind == convSessionMeta {
		if target, ok := a.currentMetaTarget(); ok && target.kind.jumpable() && (target.messageUUID != "" || target.entryIndex >= 0) {
			actions = append(actions, bindAction("", a.keymap.Conversation.JumpToTree, "jump"))
		}
	}
	if a.config.TmuxEnabled && tmux.InTmux() && a.currentSess.IsLive {
		actions = append(actions, bindAction("", a.keymap.Conversation.Input, "input"))
		if !isMeta || item.kind != convSessionMeta {
			actions = append(actions, bindAction("", a.keymap.Conversation.JumpToTree, "jump"))
		}
	}
	return actions
}

func (a *App) conversationPreviewTextHelpActions(next string) []interactionAction {
	return []interactionAction{
		labelAction("", "↑↓", "scroll"),
		labelAction("", "[]", "facet"),
		labelAction("", "s", "scope"),
		labelAction("", "z", "zoom"),
		bindAction("", a.keymap.Preview.CopyMode, "copy"),
		labelAction("", "tab", next),
	}
}

func (a *App) conversationPreviewStructuredHelpActions(next string) []interactionAction {
	return []interactionAction{
		labelAction("", "↑↓", "blocks"),
		labelAction("", "←→", "fold"),
		labelAction("", "[]", "facet"),
		labelAction("", "s", "scope"),
		labelAction("", "z", "zoom"),
		foldAllHelpAction(a),
		bindAction("", a.keymap.Preview.Filter, "filter"),
		bindAction("", a.keymap.Preview.CopyMode, "copy"),
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
