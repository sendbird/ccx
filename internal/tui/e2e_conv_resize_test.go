package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
)

// TestE2EConvCursorSurvivesResize verifies that resizing the terminal does NOT
// reset the conversation preview cursor to 0 or len-1.
func TestE2EConvCursorSurvivesResize(t *testing.T) {
	sessions, err := session.ScanSessions("")
	if err != nil || len(sessions) == 0 {
		t.Skip("no sessions available")
	}

	// Find a session with enough conversation entries
	var targetIdx int
	found := false
	for i, s := range sessions {
		if s.ID == "aee0fbf4-7aa4-47e0-9ae3-b05e44e78c06" {
			targetIdx = i
			found = true
			break
		}
	}
	if !found {
		t.Skip("target session not found")
	}

	app := NewApp(sessions, Config{})

	// Init at small size
	m, _ := app.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app = m.(*App)

	// Select session
	app.sessionList.Select(targetIdx)

	// Open preview with Tab
	m, _ = app.Update(tea.KeyMsg{Type: tea.KeyTab})
	app = m.(*App)
	_ = app.View() // triggers updateSessionConvPreview

	t.Logf("After Tab+View: sessSplit.Show=%v sessPreviewMode=%d convEntries=%d convCursor=%d",
		app.sessSplit.Show, app.sessPreviewMode, len(app.sessConvEntries), app.sessConvCursor)

	// Ensure we're in conversation mode
	for app.sessPreviewMode != sessPreviewConversation {
		m, _ = app.Update(tea.KeyMsg{Type: tea.KeyTab})
		app = m.(*App)
		_ = app.View()
	}

	// Focus the preview
	if !app.sessSplit.Focus {
		m, _ = app.Update(tea.KeyMsg{Type: tea.KeyRight})
		app = m.(*App)
		_ = app.View()
	}

	if len(app.sessConvEntries) < 3 {
		t.Skip("not enough conversation entries")
	}

	// Navigate cursor to a middle entry
	app.sessConvCursor = 2
	app.refreshConvPreview()
	t.Logf("Set cursor to 2, convCursor=%d", app.sessConvCursor)

	// Now resize — this should NOT reset the cursor
	m, _ = app.Update(tea.WindowSizeMsg{Width: 200, Height: 80})
	app = m.(*App)
	afterResizeUpdate := app.sessConvCursor
	t.Logf("After resize Update: convCursor=%d", afterResizeUpdate)

	// Call View — this triggers renderSessionSplit which detects size change
	_ = app.View()
	afterResizeView := app.sessConvCursor
	t.Logf("After resize View: convCursor=%d", afterResizeView)

	if afterResizeView != 2 {
		t.Errorf("BUG: Resize changed convCursor from 2 to %d", afterResizeView)
	}

	// Resize back
	m, _ = app.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app = m.(*App)
	_ = app.View()
	afterShrink := app.sessConvCursor
	t.Logf("After shrink View: convCursor=%d", afterShrink)

	if afterShrink != 2 {
		t.Errorf("BUG: Shrink changed convCursor from 2 to %d", afterShrink)
	}

	// Multiple resizes
	for _, size := range []struct{ w, h int }{
		{150, 50}, {200, 80}, {100, 30}, {120, 40},
	} {
		m, _ = app.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		app = m.(*App)
		_ = app.View()
		if app.sessConvCursor != 2 {
			t.Errorf("BUG: Resize %dx%d changed convCursor from 2 to %d", size.w, size.h, app.sessConvCursor)
			break
		}
	}
	t.Logf("After multiple resizes: convCursor=%d (stable=%v)", app.sessConvCursor, app.sessConvCursor == 2)
}
