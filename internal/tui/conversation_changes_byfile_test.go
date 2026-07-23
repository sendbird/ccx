package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
)

func changeArtifactAt(t time.Time, toolName, toolInput, filePath string) session.Artifact {
	return session.Artifact{
		Kind:   session.ArtifactChange,
		Key:    filePath,
		Origin: session.ArtifactOrigin{Timestamp: t},
		Data:   session.ChangeData{ToolName: toolName, ToolInput: toolInput},
	}
}

func TestReconstructFileChangesWriteThenEdit(t *testing.T) {
	base := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	occ := []session.Artifact{
		changeArtifactAt(base, "Write", `{"file_path":"/repo/svc.go","content":"a\nb\nc\n"}`, "/repo/svc.go"),
		changeArtifactAt(base.Add(10 * time.Second), "Edit", `{"file_path":"/repo/svc.go","old_string":"b","new_string":"B"}`, "/repo/svc.go"),
	}
	initial, final, ok := reconstructFileChanges(occ)
	if !ok {
		t.Fatal("expected reconstruction to succeed for Write+Edit")
	}
	if initial != "a\nb\nc\n" {
		t.Fatalf("initial = %q, want %q", initial, "a\nb\nc\n")
	}
	if final != "a\nB\nc\n" {
		t.Fatalf("final = %q, want %q", final, "a\nB\nc\n")
	}
}

func TestReconstructFileChangesEditOnlyBails(t *testing.T) {
	occ := []session.Artifact{
		changeArtifactAt(time.Now(), "Edit", `{"file_path":"/repo/svc.go","old_string":"b","new_string":"B"}`, "/repo/svc.go"),
	}
	if _, _, ok := reconstructFileChanges(occ); ok {
		t.Fatal("Edit-only history must bail (no Write baseline)")
	}
}

func TestReconstructFileChangesMissingOldStringBails(t *testing.T) {
	base := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	occ := []session.Artifact{
		changeArtifactAt(base, "Write", `{"file_path":"/repo/svc.go","content":"a\nb\n"}`, "/repo/svc.go"),
		changeArtifactAt(base.Add(10 * time.Second), "Edit", `{"file_path":"/repo/svc.go","old_string":"zzz","new_string":"B"}`, "/repo/svc.go"),
	}
	if _, _, ok := reconstructFileChanges(occ); ok {
		t.Fatal("Edit with absent old_string must bail")
	}
}

func TestReconstructFileChangesMultiEditBails(t *testing.T) {
	base := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	occ := []session.Artifact{
		changeArtifactAt(base, "Write", `{"file_path":"/repo/svc.go","content":"a\n"}`, "/repo/svc.go"),
		changeArtifactAt(base.Add(10 * time.Second), "MultiEdit", `{"file_path":"/repo/svc.go","edits":[{"old_string":"a","new_string":"b"}]}`, "/repo/svc.go"),
	}
	if _, _, ok := reconstructFileChanges(occ); ok {
		t.Fatal("MultiEdit in chain must bail to per-occurrence fallback")
	}
}

func TestRenderCumulativeFileDiff(t *testing.T) {
	got := renderCumulativeFileDiff("/repo/svc.go", "a\nb\nc\n", "a\nB\nc\n", 80)
	bare := stripANSI(got)
	if bare == "" {
		t.Fatal("expected a non-empty diff for changed content")
	}
	for _, want := range []string{"@@", "- b", "+ B"} {
		if !strings.Contains(bare, want) {
			t.Fatalf("cumulative diff missing %q:\n%s", want, bare)
		}
	}
	if renderCumulativeFileDiff("/repo/svc.go", "same\n", "same\n", 80) != "" {
		t.Fatal("identical content should produce an empty diff")
	}
}

// setupChangesByFileApp builds a session that Writes svc.go then Edits it (a
// reconstructable net diff), and Edits other.go once (Edit-only → fallback).
func setupChangesByFileApp(t *testing.T) *App {
	t.Helper()
	root := t.TempDir()
	sessID := "changes-byfile"
	sessPath := filepath.Join(root, sessID+".jsonl")
	transcript := `{"type":"user","uuid":"u1","timestamp":"2026-07-01T10:00:00Z","message":{"role":"user","content":"edit the files"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-07-01T10:00:10Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"w1","name":"Write","input":{"file_path":"/repo/svc.go","content":"package main\n\nfunc a() {}\n"}}]}}
{"type":"user","uuid":"u2","timestamp":"2026-07-01T10:00:20Z","message":{"role":"user","content":"again"}}
{"type":"assistant","uuid":"a2","timestamp":"2026-07-01T10:00:30Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"e1","name":"Edit","input":{"file_path":"/repo/svc.go","old_string":"func a() {}","new_string":"func a() {\n\treturn\n}"}}]}}
{"type":"user","uuid":"u3","timestamp":"2026-07-01T10:00:40Z","message":{"role":"user","content":"and other"}}
{"type":"assistant","uuid":"a3","timestamp":"2026-07-01T10:00:50Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"e2","name":"Edit","input":{"file_path":"/repo/other.go","old_string":"x","new_string":"y"}}]}}
`
	if err := os.WriteFile(sessPath, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}
	sess := session.Session{ID: sessID, ShortID: "cbf", FilePath: sessPath, ProjectPath: root, ProjectName: "cbf"}
	app := NewApp([]session.Session{sess}, Config{})
	model, _ := app.Update(tea.WindowSizeMsg{Width: 150, Height: 45})
	app = model.(*App)
	app.openConversation(sess)
	return app
}

func TestRenderInspectorChangesByFileNetAndFallback(t *testing.T) {
	app := setupChangesByFileApp(t)
	app.conv.inspector.Scope = session.ScopeSession
	app.conv.inspector.ChangesByFile = true

	out := stripANSI(app.renderInspectorChanges(app.conv.flow.RootID))
	if !strings.Contains(out, "Changes (by file)") {
		t.Fatalf("missing by-file header:\n%s", out)
	}
	// svc.go has a Write baseline → cumulative net diff (func a() {} replaced).
	if !strings.Contains(out, "svc.go") {
		t.Fatalf("missing svc.go section:\n%s", out)
	}
	if !strings.Contains(out, "+ func a() {") || !strings.Contains(out, "- func a() {}") {
		t.Fatalf("missing svc.go net diff lines:\n%s", out)
	}
	// other.go is Edit-only → per-occurrence fallback marker + the Edit diff.
	if !strings.Contains(out, "other.go") {
		t.Fatalf("missing other.go section:\n%s", out)
	}
	if !strings.Contains(out, "cumulative diff unavailable") {
		t.Fatalf("missing fallback marker for Edit-only file:\n%s", out)
	}
	if !strings.Contains(out, "Edit") {
		t.Fatalf("missing per-occurrence Edit in fallback:\n%s", out)
	}
}
