package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sendbird/ccx/internal/session"
)

// writeTestScratchpad creates a scratchpad directory for projectPath/sessionID
// under the session package's ScratchpadBase (via its test override) and writes
// the given files. Cleaned up via t.Cleanup.
func writeTestScratchpad(t *testing.T, projectPath, sessionID string, files map[string]string) string {
	t.Helper()
	base := t.TempDir()
	restore := session.SetScratchpadBaseOverride(base)
	t.Cleanup(restore)
	dir := filepath.Join(base, session.EncodeProjectPath(projectPath), sessionID, "scratchpad")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir scratchpad: %v", err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func TestMetaScratchpadEntriesBuildsRowsAndTargets(t *testing.T) {
	root := t.TempDir()
	app := setupConvApp(t, testEntries(), 160, 50)
	app.conv.sess.ProjectPath = root
	app.conv.sess.ID = "scratch-sess"
	writeTestScratchpad(t, root, "scratch-sess", map[string]string{
		"notes.md": "scratch body\n",
		"draft.txt": "draft\n",
	})

	entries := app.metaScratchpadEntries()
	// header + 2 file rows
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want header + 2 rows", len(entries))
	}
	rows := entries[1:]
	for _, e := range rows {
		if e.target.kind != metaTargetScratchpad {
			t.Fatalf("target kind = %v, want metaTargetScratchpad", e.target.kind)
		}
		if e.target.filePath == "" || !strings.HasSuffix(e.target.filePath, filepath.Base(e.target.filePath)) {
			t.Fatalf("target filePath missing/not absolute: %q", e.target.filePath)
		}
		if _, err := os.Stat(e.target.filePath); err != nil {
			t.Fatalf("scratchpad target filePath does not exist on disk: %q (%v)", e.target.filePath, err)
		}
	}
}

func TestHandleMetaEntryEnterScratchpadOpensEditor(t *testing.T) {
	root := t.TempDir()
	app := setupConvApp(t, testEntries(), 160, 50)
	app.conv.sess.ProjectPath = root
	app.conv.sess.ID = "scratch-sess"
	dir := writeTestScratchpad(t, root, "scratch-sess", map[string]string{"a.md": "x\n"})
	target := metaEntryTarget{kind: metaTargetScratchpad, filePath: filepath.Join(dir, "a.md")}
	app.conv.inspector.MetaTargets = []metaEntryTarget{{blockIdx: -1}, target}
	app.conv.split.Folds.Entry = session.Entry{Content: []session.ContentBlock{
		{Type: "text", Text: "h"}, {Type: "text", Text: "a"},
	}}
	app.conv.split.Folds.BlockCursor = 1

	// Use a no-op editor so the test does not actually launch vi.
	t.Setenv("EDITOR", "true")
	handled, _, cmd := app.handleMetaEntryEnter()
	if !handled {
		t.Fatal("handleMetaEntryEnter should handle metaTargetScratchpad")
	}
	if cmd == nil {
		t.Fatal("expected a tea.Cmd (ExecProcess) to open the editor")
	}
}

func TestHandleMetaEntryEnterScratchpadMissingPath(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 50)
	target := metaEntryTarget{kind: metaTargetScratchpad, filePath: ""}
	app.conv.inspector.MetaTargets = []metaEntryTarget{{blockIdx: -1}, target}
	app.conv.split.Folds.Entry = session.Entry{Content: []session.ContentBlock{
		{Type: "text", Text: "h"}, {Type: "text", Text: "a"},
	}}
	app.conv.split.Folds.BlockCursor = 1
	handled, _, _ := app.handleMetaEntryEnter()
	if !handled || app.copiedMsg == "" {
		t.Fatalf("expected handled with an error message, got handled=%v msg=%q", handled, app.copiedMsg)
	}
}

func TestMemoryTargetCarriesFilePath(t *testing.T) {
	root := t.TempDir()
	app := setupConvApp(t, testEntries(), 160, 50)
	app.conv.sess.ProjectPath = root
	writeTestMemoryNotes(t, root, map[string]string{
		"alpha.md": "---\nname: alpha\nmetadata:\n  type: project\n---\nbody\n",
	})
	entries := app.metaMemoryEntries()
	var saw bool
	for _, e := range entries {
		if e.target.kind == metaTargetMemoryFile && e.target.fileName == "alpha.md" {
			want := filepath.Join(homeDir(), ".claude", "projects", session.EncodeProjectPath(root), "memory", "alpha.md")
			if e.target.filePath != want {
				t.Fatalf("memory filePath = %q, want %q", e.target.filePath, want)
			}
			saw = true
		}
	}
	if !saw {
		t.Fatal("no alpha.md memory target found")
	}
}

func TestPlanTargetCarriesFilePath(t *testing.T) {
	// planFilePath prefers PlanFilePath, falling back to the artifact key.
	data := session.PlanData{PlanFilePath: "/repo/plans/foo.md"}
	if got := planFilePath(data, "/repo/.claude/plans/key"); got != "/repo/plans/foo.md" {
		t.Fatalf("planFilePath with PlanFilePath = %q, want /repo/plans/foo.md", got)
	}
	if got := planFilePath(session.PlanData{}, "/repo/.claude/plans/key"); got != "/repo/.claude/plans/key" {
		t.Fatalf("planFilePath fallback = %q, want key", got)
	}
}

func TestOpenEditMenuIncludesFocusedArtifactFile(t *testing.T) {
	root := t.TempDir()
	app := setupConvApp(t, testEntries(), 160, 50)
	app.conv.sess.ProjectPath = root
	app.currentSess = app.conv.sess
	dir := writeTestScratchpad(t, root, app.conv.sess.ID, map[string]string{"a.md": "x\n"})
	fp := filepath.Join(dir, "a.md")
	app.conv.inspector.MetaTargets = []metaEntryTarget{{blockIdx: -1}, {kind: metaTargetScratchpad, filePath: fp}}
	app.conv.split.Folds.Entry = session.Entry{Content: []session.ContentBlock{
		{Type: "text", Text: "h"}, {Type: "text", Text: "a"},
	}}
	app.conv.split.Folds.BlockCursor = 1

	app.openEditMenu(app.currentSess)
	found := false
	for _, c := range app.editChoices {
		if c.path == fp {
			found = true
		}
	}
	if !found {
		t.Fatalf("edit menu did not include focused scratchpad file %q; choices=%+v", fp, app.editChoices)
	}
}

func TestOpenEditMenuSkipsMissingArtifactFile(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 50)
	app.currentSess = app.conv.sess
	app.conv.inspector.MetaTargets = []metaEntryTarget{{blockIdx: -1}, {kind: metaTargetPlan, filePath: "/nonexistent/plan.md"}}
	app.conv.split.Folds.Entry = session.Entry{Content: []session.ContentBlock{
		{Type: "text", Text: "h"}, {Type: "text", Text: "a"},
	}}
	app.conv.split.Folds.BlockCursor = 1
	app.openEditMenu(app.currentSess)
	for _, c := range app.editChoices {
		if c.path == "/nonexistent/plan.md" {
			t.Fatal("edit menu should skip a missing artifact file")
		}
	}
}
