package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
)

// setupRefsFlowApp builds a session whose transcript contains a PR URL and
// flags HasRefs so the "Session Refs & URLs" context row appears in the flow.
func setupRefsFlowApp(t *testing.T) *App {
	t.Helper()
	root := t.TempDir()
	sessID := "refs-flow"
	sessPath := filepath.Join(root, sessID+".jsonl")
	body := `{"type":"user","uuid":"u1","timestamp":"2026-07-01T10:00:00Z","message":{"role":"user","content":"review the PR"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-07-01T10:00:10Z","message":{"role":"assistant","content":[{"type":"text","text":"see https://github.com/sendbird/ccx/pull/106 for context"}]}}
`
	if err := os.WriteFile(sessPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	sess := session.Session{ID: sessID, ShortID: "refs", FilePath: sessPath, ProjectPath: root, ProjectName: "refs", HasRefs: true}
	app := NewApp([]session.Session{sess}, Config{})
	model, _ := app.Update(tea.WindowSizeMsg{Width: 150, Height: 45})
	app = model.(*App)
	app.openConversation(sess)
	return app
}

func TestSessionMetaRefsRowListsRefAndEnterOpensURL(t *testing.T) {
	app := setupRefsFlowApp(t)
	selectMetaContextRow(t, app, "refs")

	targets := app.conv.inspector.MetaTargets
	if len(targets) < 2 {
		t.Fatalf("expected at least header + 1 ref target, got %d: %+v", len(targets), targets)
	}
	refCursor := -1
	for i, tg := range targets {
		if tg.kind == metaTargetRef {
			refCursor = i
			break
		}
	}
	if refCursor < 0 {
		t.Fatalf("no metaTargetRef in refs row targets: %+v", targets)
	}
	if targets[refCursor].url == "" {
		t.Errorf("ref target has empty url: %+v", targets[refCursor])
	}

	view := stripANSI(app.conv.split.Preview.View())
	if !strings.Contains(view, "pull/106") && !strings.Contains(view, "ccx") {
		t.Errorf("refs preview missing the PR ref: %q", view)
	}

	var opened string
	app.openURL = func(u string) error { opened = u; return nil }
	app.conv.split.Focus = true
	app.conv.split.Folds.BlockCursor = refCursor
	app = pressKey(app, "enter")
	if opened == "" {
		t.Fatalf("enter on ref row did not open a URL; copiedMsg=%q", app.copiedMsg)
	}
	if !strings.Contains(opened, "pull/106") {
		t.Errorf("opened URL %q does not contain the PR path", opened)
	}
}
