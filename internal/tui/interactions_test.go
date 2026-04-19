package tui

import (
	"strings"
	"testing"

	"github.com/sendbird/ccx/internal/session"
)

func TestConversationActionMenuUsesKeymapBindings(t *testing.T) {
	app := newTestApp(fakeSessions())
	app.keymap.Actions.URLs = "U"
	app.keymap.Actions.Files = "F"
	app.keymap.Actions.Changes = "G"

	hint := stripANSI(app.renderConvActionsHintBox())
	if !strings.Contains(hint, "U:urls") {
		t.Fatalf("expected URLs binding in actions hint, got %q", hint)
	}
	if !strings.Contains(hint, "F:files") {
		t.Fatalf("expected Files binding in actions hint, got %q", hint)
	}
	if !strings.Contains(hint, "G:changes") {
		t.Fatalf("expected Changes binding in actions hint, got %q", hint)
	}
}

func TestConversationHelpUsesConfigurablePreviewCopyBinding(t *testing.T) {
	app := setupConvApp(t, testEntries(), 120, 30)
	app.conv.split.Show = true
	app.conv.split.Focus = true
	app.conv.rightPaneMode = previewText
	app.keymap.Preview.CopyMode = "ctrl+c"
	app.updateConvPreview()

	help := stripANSI(app.convHelpLine(""))
	if !strings.Contains(help, "ctrl+c:copy") {
		t.Fatalf("expected custom preview copy binding in help, got %q", help)
	}
}

func TestMessageFullHelpUsesConfigurableActionBindings(t *testing.T) {
	app := setupConvApp(t, testEntries(), 120, 30)
	item, ok := app.convList.SelectedItem().(convItem)
	if !ok {
		t.Fatal("expected selected conversation item")
	}
	m, _ := app.openMsgFullForEntry(item.merged)
	app = m.(*App)
	app.keymap.Conversation.Actions = "A"
	app.keymap.Preview.CopyAll = "Y"

	help := stripANSI(app.msgFullHelpLine())
	if !strings.Contains(help, "A:actions") {
		t.Fatalf("expected configurable actions binding in message-full help, got %q", help)
	}
	if !strings.Contains(help, "Y:all") {
		t.Fatalf("expected configurable copy-all binding in message-full help, got %q", help)
	}
}

func TestHandleConvActionsMenuUsesConfigurableChangeBinding(t *testing.T) {
	base := testEntries()
	base = append(base, session.Entry{
		Role: "assistant",
		Content: []session.ContentBlock{{
			Type:      "tool_use",
			ToolName:  "Edit",
			ToolInput: `{"file_path":"/tmp/x.go","old_string":"a","new_string":"b"}`,
		}},
	})
	app := setupConvApp(t, base, 120, 30)
	selectConvItemBy(t, app, func(ci convItem) bool {
		if ci.kind != convMsg {
			return false
		}
		for _, block := range ci.merged.entry.Content {
			if block.Type == "tool_use" && block.ToolName == "Edit" {
				return true
			}
		}
		return false
	})
	m, _ := app.openMsgFullForEntry(app.convList.SelectedItem().(convItem).merged)
	app = m.(*App)
	app.keymap.Actions.Changes = "G"

	m, _ = app.handleConvActionsMenu("G")
	app = m.(*App)
	if !app.urlMenu {
		t.Fatal("expected actions menu to open scoped menu for configurable changes binding")
	}
	if !strings.Contains(app.urlScope, "message") && !strings.Contains(app.urlScope, "block") {
		t.Fatalf("expected change scope label, got %q", app.urlScope)
	}
}
