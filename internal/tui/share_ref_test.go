package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sendbird/ccx/internal/session"
)

// TestOpenShareRef_NoShareable verifies the picker refuses to open when the
// selected session has no memory/scratchpad/plan artifacts.
func TestOpenShareRef_NoShareable(t *testing.T) {
	app := newTestApp(fakeSessions())
	app.sessionList.Select(0) // "aaa" /tmp/proj-a, live, no artifacts

	model, _ := app.openShareRef()
	a := model.(*App)
	if a.shareRefMenu {
		t.Fatalf("expected menu closed when nothing shareable, got open")
	}
	if a.copiedMsg == "" {
		t.Errorf("expected a toast explaining nothing to share")
	}
}

// TestShareRefPicker_Stages verifies the two-stage picker: opening with a
// scratchpad file shows it in stage 0; enter advances to target selection
// (live sessions excluding the source); esc closes.
func TestShareRefPicker_Stages(t *testing.T) {
	// Hermetic scratchpad base so gatherShareRefItems finds a file without
	// touching /tmp or the developer's ~/.claude.
	restore := session.SetScratchpadBaseOverride(t.TempDir())
	defer restore()

	// Also neutralize ~/.claude/projects memory reads by pointing HOME at a
	// temp dir, so the only shareable is the scratchpad file we write.
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	srcProject := "/tmp/proj-a"
	srcSession := "aaa"
	scratchDir := filepath.Join(session.ScratchpadBase(), session.EncodeProjectPath(srcProject), srcSession, "scratchpad")
	if err := os.MkdirAll(scratchDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(scratchDir, "notes.md"), []byte("# plan\nscratch text\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	app := newTestApp(fakeSessions())
	app.sessionList.Select(0) // aaa, IsLive=true

	model, _ := app.openShareRef()
	a := model.(*App)
	if !a.shareRefMenu {
		t.Fatalf("expected shareRefMenu open")
	}
	if a.shareRefStage != shareRefStageItem {
		t.Errorf("expected stage item, got %d", a.shareRefStage)
	}
	if len(a.shareRefItems) == 0 {
		t.Fatalf("expected at least one shareable item")
	}
	if a.shareRefItems[0].Kind != "scratchpad" {
		t.Errorf("expected first item kind scratchpad, got %q", a.shareRefItems[0].Kind)
	}
	if a.shareRefItems[0].Path == "" {
		t.Errorf("expected non-empty path")
	}

	// Confirm the artifact → advance to target stage.
	a2, _ := a.handleShareRefKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !a2.(*App).shareRefMenu {
		t.Fatalf("menu should stay open after picking artifact")
	}
	if a2.(*App).shareRefStage != shareRefStageTarget {
		t.Errorf("expected stage target, got %d", a2.(*App).shareRefStage)
	}
	// Targets = live sessions excluding source (aaa). fakeSessions has aaa+ccc live.
	if len(a2.(*App).shareRefTargets) != 1 {
		t.Fatalf("expected 1 target (ccc), got %d", len(a2.(*App).shareRefTargets))
	}
	if a2.(*App).shareRefTargets[0].ID != "ccc" {
		t.Errorf("expected target ccc, got %s", a2.(*App).shareRefTargets[0].ID)
	}

	// Esc closes without injecting.
	a3, _ := a2.(*App).handleShareRefKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if a3.(*App).shareRefMenu {
		t.Errorf("expected menu closed after q")
	}
}

// TestShareRefPicker_Navigation verifies up/down clamps within the item list.
func TestShareRefPicker_Navigation(t *testing.T) {
	restore := session.SetScratchpadBaseOverride(t.TempDir())
	defer restore()
	t.Setenv("HOME", t.TempDir())

	srcProject := "/tmp/proj-a"
	scratchDir := filepath.Join(session.ScratchpadBase(), session.EncodeProjectPath(srcProject), "aaa", "scratchpad")
	if err := os.MkdirAll(scratchDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if err := os.WriteFile(filepath.Join(scratchDir, name), []byte("x\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	app := newTestApp(fakeSessions())
	app.sessionList.Select(0)
	model, _ := app.openShareRef()
	a := model.(*App)
	if len(a.shareRefItems) != 3 {
		t.Fatalf("expected 3 items, got %d", len(a.shareRefItems))
	}

	// Down twice → cursor 2.
	m, _ := a.handleShareRefKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	a = m.(*App)
	m, _ = a.handleShareRefKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	a = m.(*App)
	if a.shareRefCursor != 2 {
		t.Errorf("expected cursor 2, got %d", a.shareRefCursor)
	}
	// Down past end → clamp at 2.
	m, _ = a.handleShareRefKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	a = m.(*App)
	if a.shareRefCursor != 2 {
		t.Errorf("expected cursor clamped at 2, got %d", a.shareRefCursor)
	}
	// Up once → cursor 1.
	m, _ = a.handleShareRefKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	a = m.(*App)
	if a.shareRefCursor != 1 {
		t.Errorf("expected cursor 1, got %d", a.shareRefCursor)
	}
}
