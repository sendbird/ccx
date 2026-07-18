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

func TestZoomedInspectorHelpUsesConfigurableActionBinding(t *testing.T) {
	app := setupConvApp(t, testEntries(), 120, 30)
	app.keymap.Conversation.Actions = "A"
	app.openInspector(inspectorConversation, session.ScopeNode, true)

	help := stripANSI(app.convHelpLine(""))
	if !strings.Contains(help, "A:actions") {
		t.Fatalf("expected configurable actions binding in inspector help, got %q", help)
	}
	if !strings.Contains(help, "z:zoom") {
		t.Fatalf("expected zoom control in inspector help, got %q", help)
	}
}

func TestHandleConvActionsMenuUsesConfigurableChangeBinding(t *testing.T) {
	app, _, _ := setupInspectorFlowApp(t)
	selectInspectorItem(t, app, func(item convItem) bool {
		return item.kind == convMsg && item.merged.entry.UUID == "a1"
	})
	app.keymap.Actions.Changes = "G"
	app.conv.inspector.Scope = session.ScopeNode

	m, _ := app.handleConvActionsMenu("G")
	app = m.(*App)
	if app.conv.inspector.Tab != inspectorChanges {
		t.Fatalf("action tab = %v, want Changes", app.conv.inspector.Tab)
	}
	if app.conv.inspector.Scope != session.ScopeNode {
		t.Fatalf("action scope = %v, want Node", app.conv.inspector.Scope)
	}
	if app.urlMenu {
		t.Fatal("conversation changes must not open the legacy URL menu")
	}
}

func TestHandleConvActionsMenuRoutesFilesToFilesFacet(t *testing.T) {
	app, _, _ := setupInspectorFlowApp(t)
	selectInspectorItem(t, app, func(item convItem) bool {
		return item.kind == convMsg && item.merged.entry.UUID == "a1"
	})
	app.keymap.Actions.Files = "F"
	app.conv.inspector.Scope = session.ScopeNode

	m, _ := app.handleConvActionsMenu("F")
	app = m.(*App)
	if app.conv.inspector.Tab != inspectorFiles {
		t.Fatalf("action tab = %v, want Files", app.conv.inspector.Tab)
	}
	if content := app.conv.inspector.Rendered; !strings.Contains(content, "/repo/parent.go") || !strings.Contains(content, "origin:") {
		t.Fatalf("files facet missing path/provenance: %q", content)
	}
}

func TestHandleConvActionsMenuKeepsEmptyFilesFacet(t *testing.T) {
	app, _, _ := setupInspectorFlowApp(t)
	selectInspectorItem(t, app, func(item convItem) bool {
		return item.kind == convMsg && item.merged.entry.UUID == "a2"
	})
	app.keymap.Actions.Files = "F"
	app.conv.inspector.Scope = session.ScopeNode

	m, _ := app.handleConvActionsMenu("F")
	app = m.(*App)
	if app.conv.inspector.Tab != inspectorFiles {
		t.Fatalf("empty action tab = %v, want Files", app.conv.inspector.Tab)
	}
	if !strings.Contains(app.conv.inspector.Rendered, "No files in this node scope") {
		t.Fatalf("empty files action rendered %q", app.conv.inspector.Rendered)
	}

	app.conv.split.Focus = true
	app = pressKey(app, "s")
	if app.conv.inspector.Tab != inspectorFiles || app.conv.inspector.Scope != session.ScopeSubtree {
		t.Fatalf("scope expansion lost files facet: tab=%v scope=%v", app.conv.inspector.Tab, app.conv.inspector.Scope)
	}
	if !strings.Contains(app.conv.inspector.Rendered, "/repo/agent.go") {
		t.Fatalf("subtree files missing agent file: %q", app.conv.inspector.Rendered)
	}
}

func TestHandleConvActionsMenuCopyCopiesRenderedFacet(t *testing.T) {
	app, _, _ := setupInspectorFlowApp(t)
	selectInspectorItem(t, app, func(item convItem) bool {
		return item.kind == convMsg && item.merged.entry.UUID == "a2"
	})
	app.openInspector(inspectorRefs, session.ScopeSession, false)
	app.keymap.Actions.Copy = "C"

	var copied string
	oldClipboardWrite := clipboardWrite
	clipboardWrite = func(text string) error {
		copied = text
		return nil
	}
	t.Cleanup(func() { clipboardWrite = oldClipboardWrite })

	m, _ := app.handleConvActionsMenu("C")
	app = m.(*App)
	if !strings.Contains(copied, "acme/repo#42") || !strings.Contains(copied, "origin:") {
		t.Fatalf("action copy payload is not rendered facet: %q", copied)
	}
	if strings.Contains(copied, "delegate image inspection") {
		t.Fatalf("action copy payload leaked selected message: %q", copied)
	}
	if app.copiedMsg != "Copied inspector!" {
		t.Fatalf("copy confirmation = %q", app.copiedMsg)
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

func TestHandleSessionPreviewActionsMenuCopyCopiesPreviewMessage(t *testing.T) {
	entries := testEntries()
	app := newTestApp(fakeSessions())
	app.sessSplit.Show = true
	app.sessSplit.Focus = true
	app.sessPreviewMode = sessPreviewConversation
	app.sessConvEntries = filterConversation(mergeConversationTurns(entries))
	app.sessConvCursor = 0
	app.keymap.Session.Actions = "x"
	app.keymap.Actions.Copy = "z"

	m, _, _ := app.handleConvPreviewKeys(&app.sessSplit, "x")
	app = m.(*App)
	if !app.actionsMenu {
		t.Fatal("expected session preview actions menu to open")
	}

	m, _ = app.handleActionsMenu("z")
	app = m.(*App)
	if app.actionsMenu {
		t.Fatal("expected actions menu to close after copy")
	}
	if !strings.Contains(app.copiedMsg, "Copied message") {
		t.Fatalf("expected preview copy confirmation, got %q", app.copiedMsg)
	}
}

func TestHandleSessionPreviewActionsMenuIgnoresExistingMultiSelection(t *testing.T) {
	entries := testEntries()
	app := newTestApp(fakeSessions())
	app.sessSplit.Show = true
	app.sessSplit.Focus = true
	app.sessPreviewMode = sessPreviewConversation
	app.sessConvEntries = filterConversation(mergeConversationTurns(entries))
	app.sessConvCursor = 0
	app.selectedSet = map[string]bool{"bbb": true}
	app.keymap.Session.Actions = "x"

	m, _, _ := app.handleConvPreviewKeys(&app.sessSplit, "x")
	app = m.(*App)
	if !app.actionsMenu {
		t.Fatal("expected session preview actions menu to open")
	}

	hint := stripANSI(app.renderActionsHintBox())
	if strings.Contains(hint, "selected") {
		t.Fatalf("expected preview actions menu, got bulk hint %q", hint)
	}
	if !strings.Contains(hint, "contexts") {
		t.Fatalf("expected contexts action hint, got %q", hint)
	}
}

func TestHandleBulkActionsMenuCopyCopiesSelectedSessionPaths(t *testing.T) {
	sessions := fakeSessions()
	sessions[0].FilePath = "/tmp/a.jsonl"
	sessions[1].FilePath = "/tmp/b.jsonl"
	app := newTestApp(sessions)
	app.selectedSet = map[string]bool{"aaa": true, "bbb": true}
	app.actionsMenu = true
	app.keymap.Actions.Copy = "c"

	m, _ := app.handleActionsMenu("c")
	app = m.(*App)
	if app.actionsMenu {
		t.Fatal("expected bulk actions menu to close after copy")
	}
	if !strings.Contains(app.copiedMsg, "Copied 2 session paths") {
		t.Fatalf("expected bulk copy confirmation, got %q", app.copiedMsg)
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
