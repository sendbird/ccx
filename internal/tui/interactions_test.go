package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

func writeChangeSession(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".jsonl")
	body := `{"type":"assistant","timestamp":"2025-01-01T00:00:00Z","message":{"role":"assistant","content":[{"type":"tool_use","name":"Edit","input":{"file_path":"/tmp/` + name + `.go","old_string":"a","new_string":"b"}}]}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write change session: %v", err)
	}
	return path
}

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

func TestHandleActionsMenuOpensSessionChanges(t *testing.T) {
	path := writeChangeSession(t, "single")
	sessions := []session.Session{{
		ID: "aaa", ShortID: "aaa",
		FilePath:    path,
		ProjectPath: "/tmp/proj-a", ProjectName: "proj-a",
		ModTime: time.Now(), MsgCount: 1,
	}}
	app := newTestApp(sessions)
	app.actionsSess = sessions[0]
	app.actionsMenu = true
	app.keymap.Actions.Changes = "g"

	m, _ := app.handleActionsMenu("g")
	app = m.(*App)
	if !app.urlMenu {
		t.Fatal("expected actions menu to open URL menu for session changes")
	}
	if !strings.Contains(app.urlScope, "changes") {
		t.Fatalf("expected changes scope, got %q", app.urlScope)
	}
	if len(app.urlChangeMap) == 0 {
		t.Fatal("expected change map populated for diff preview")
	}
}

func TestHandleBulkActionsMenuOpensBulkChanges(t *testing.T) {
	pathA := writeChangeSession(t, "bulk-a")
	pathB := writeChangeSession(t, "bulk-b")
	sessions := []session.Session{
		{ID: "aaa", ShortID: "aaa", FilePath: pathA, ProjectPath: "/tmp/proj-a", ProjectName: "proj-a"},
		{ID: "bbb", ShortID: "bbb", FilePath: pathB, ProjectPath: "/tmp/proj-b", ProjectName: "proj-b"},
	}
	app := newTestApp(sessions)
	app.selectedSet = map[string]bool{"aaa": true, "bbb": true}
	app.actionsMenu = true
	app.keymap.Actions.Changes = "g"

	m, _ := app.handleActionsMenu("g")
	app = m.(*App)
	if !app.urlMenu {
		t.Fatal("expected bulk actions menu to open URL menu for changes")
	}
	if !strings.Contains(app.urlScope, "changes") {
		t.Fatalf("expected bulk changes scope, got %q", app.urlScope)
	}
	if len(app.urlChangeMap) < 2 {
		t.Fatalf("expected change map populated for both sessions, got %d", len(app.urlChangeMap))
	}
}

func TestHandleConvActionsMenuCopyCopiesSelectedBlock(t *testing.T) {
	app := setupConvApp(t, testEntries(), 120, 30)
	app.conv.split.Show = true
	app.conv.split.Focus = true
	app.keymap.Actions.Copy = "c"

	selectConvItemBy(t, app, func(ci convItem) bool {
		return ci.kind == convMsg && ci.merged.entry.Role == "assistant"
	})
	app.updateConvPreview()
	if app.conv.split.Folds == nil || len(app.conv.split.Folds.Entry.Content) == 0 {
		t.Fatal("expected fold state after preview update")
	}
	app.conv.split.Folds.BlockCursor = 0
	app.copiedMsg = ""

	m, _ := app.handleConvActionsMenu("c")
	app = m.(*App)
	if app.convActionsMenu {
		t.Fatal("expected actions menu to close after handling copy")
	}
	if !strings.Contains(app.copiedMsg, "Copied") {
		t.Fatalf("expected copy confirmation, got %q", app.copiedMsg)
	}
}

func TestRefreshConversationPreservesFoldSelection(t *testing.T) {
	entries := testEntries()
	app := setupConvApp(t, entries, 120, 30)
	app.conv.split.Show = true

	selectConvItemBy(t, app, func(ci convItem) bool {
		return ci.kind == convMsg && ci.merged.entry.Role == "assistant"
	})
	app.updateConvPreview()

	if app.conv.split.Folds == nil || len(app.conv.split.Folds.Entry.Content) == 0 {
		t.Fatal("expected fold state populated before refresh")
	}
	prevCursor := 1
	if prevCursor >= len(app.conv.split.Folds.Entry.Content) {
		prevCursor = len(app.conv.split.Folds.Entry.Content) - 1
	}
	app.conv.split.Folds.BlockCursor = prevCursor
	app.conv.split.Folds.Selected = foldSet{prevCursor: true}
	prevListIdx := app.convList.Index()
	prevCacheKey := app.conv.split.CacheKey

	// Simulate refreshConversation's rebuild step (no file I/O)
	app.conv.items = buildConvItems(app.conv.sess, app.conv.merged, nil, nil, nil)
	prevYOffset := app.conv.split.Preview.YOffset
	app.rebuildConversationList(prevListIdx)
	app.conv.split.CacheKey = prevCacheKey
	app.updateConvPreview()
	if app.conv.split.Folds != nil {
		app.conv.split.Preview.YOffset = prevYOffset
	}

	if app.convList.Index() != prevListIdx {
		t.Fatalf("list cursor should be preserved across refresh: got %d want %d", app.convList.Index(), prevListIdx)
	}
	if app.conv.split.Folds == nil {
		t.Fatal("fold state should remain after refresh")
	}
	if app.conv.split.Folds.BlockCursor != prevCursor {
		t.Fatalf("block cursor should be preserved: got %d want %d", app.conv.split.Folds.BlockCursor, prevCursor)
	}
	if !app.conv.split.Folds.Selected[prevCursor] {
		t.Fatal("block selection should be preserved across refresh")
	}
}
