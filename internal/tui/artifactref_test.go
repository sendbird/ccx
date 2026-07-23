package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
)

// setupArtifactFlowApp builds a session whose transcript publishes one Claude
// Artifact so the "Session Refs & URLs" row lists it.
func setupArtifactFlowApp(t *testing.T) *App {
	t.Helper()
	root := t.TempDir()
	sessID := "artifact-flow"
	sessPath := filepath.Join(root, sessID+".jsonl")
	body := `{"type":"user","uuid":"u1","timestamp":"2026-07-01T10:00:00Z","message":{"role":"user","content":"draw the architecture"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-07-01T10:00:10Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_art1","name":"Artifact","input":{"label":"cohome-architecture-v1","description":"cohome cell sandbox platform architecture explorer","favicon":"🧩","file_path":"/tmp/scratchpad/cohome-arch.html"}}]}}
{"type":"user","uuid":"r1","timestamp":"2026-07-01T10:00:12Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_art1","content":"Published /tmp/scratchpad/cohome-arch.html at https://claude.ai/code/artifact/d248181d-78cb-471d-9d94-56bea9242b23"}]}}
`
	if err := os.WriteFile(sessPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	sess := session.Session{ID: sessID, ShortID: "art", FilePath: sessPath, ProjectPath: root, ProjectName: "art", HasRefs: true}
	app := NewApp([]session.Session{sess}, Config{})
	model, _ := app.Update(tea.WindowSizeMsg{Width: 150, Height: 45})
	app = model.(*App)
	app.openConversation(sess)
	return app
}

func TestSessionMetaRefsRowIncludesArtifactAndEnterOpensURL(t *testing.T) {
	app := setupArtifactFlowApp(t)
	selectMetaContextRow(t, app, "refs")

	targets := app.conv.inspector.MetaTargets
	artCursor := -1
	for i, tg := range targets {
		if tg.kind == metaTargetRef && strings.Contains(tg.url, "claude.ai/code/artifact/") {
			artCursor = i
			break
		}
	}
	if artCursor < 0 {
		t.Fatalf("no artifact ref target in refs row: %+v", targets)
	}

	// The rendered preview should show the artifact title (from the tool_use label).
	view := stripANSI(app.conv.split.Preview.View())
	if !strings.Contains(view, "cohome-architecture-v1") {
		t.Errorf("refs preview missing artifact title: %q", view)
	}

	var opened string
	app.openURL = func(u string) error { opened = u; return nil }
	app.conv.split.Focus = true
	app.conv.split.Folds.BlockCursor = artCursor
	app = pressKey(app, "enter")
	if opened == "" {
		t.Fatalf("enter on artifact row did not open URL; copiedMsg=%q", app.copiedMsg)
	}
	if !strings.Contains(opened, "claude.ai/code/artifact/") {
		t.Errorf("opened URL %q is not the artifact URL", opened)
	}
}
