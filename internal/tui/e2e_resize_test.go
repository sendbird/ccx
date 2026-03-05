package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
)

// TestE2EResizePreservesSelection verifies that resizing the terminal
// (WindowSizeMsg) does not change the selected session.
func TestE2EResizePreservesSelection(t *testing.T) {
	sessions, err := session.ScanSessions("")
	if err != nil || len(sessions) == 0 {
		t.Skip("no sessions available")
	}

	app := NewApp(sessions, Config{})

	// Initial small size (simulates split tmux pane)
	m, _ := app.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app = m.(*App)
	t.Logf("After init: state=%d items=%d Index()=%d", app.state, len(app.sessionList.Items()), app.sessionList.Index())

	// Select a session near the top (index 3)
	targetIdx := 3
	if len(app.sessionList.Items()) <= targetIdx {
		t.Skip("not enough sessions")
	}
	app.sessionList.Select(targetIdx)
	t.Logf("After Select(%d): Index()=%d", targetIdx, app.sessionList.Index())

	// Call View() to trigger renderSessionSplit
	_ = app.View()
	afterView := app.sessionList.Index()
	t.Logf("After View(): Index()=%d", afterView)
	if afterView != targetIdx {
		t.Errorf("BUG: View() changed selection from %d to %d", targetIdx, afterView)
	}

	// Resize to larger (simulate tmux zoom)
	m, _ = app.Update(tea.WindowSizeMsg{Width: 200, Height: 80})
	app = m.(*App)
	afterResize := app.sessionList.Index()
	t.Logf("After resize to 200x80: Index()=%d PerPage=%d Page=%d",
		afterResize, app.sessionList.Paginator.PerPage, app.sessionList.Paginator.Page)
	if afterResize != targetIdx {
		t.Errorf("BUG: Resize changed selection from %d to %d", targetIdx, afterResize)
	}

	// Call View() after resize
	_ = app.View()
	afterView2 := app.sessionList.Index()
	t.Logf("After View() post-resize: Index()=%d", afterView2)
	if afterView2 != targetIdx {
		t.Errorf("BUG: View() after resize changed selection from %d to %d", targetIdx, afterView2)
	}

	// Resize back to small (simulate unzoom)
	m, _ = app.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app = m.(*App)
	afterShrink := app.sessionList.Index()
	t.Logf("After resize back to 120x40: Index()=%d PerPage=%d Page=%d",
		afterShrink, app.sessionList.Paginator.PerPage, app.sessionList.Paginator.Page)
	if afterShrink != targetIdx {
		t.Errorf("BUG: Shrink resize changed selection from %d to %d", targetIdx, afterShrink)
	}

	_ = app.View()
	afterView3 := app.sessionList.Index()
	t.Logf("After View() post-shrink: Index()=%d", afterView3)
	if afterView3 != targetIdx {
		t.Errorf("BUG: View() after shrink changed selection from %d to %d", targetIdx, afterView3)
	}

	// Test with a session further down the list (different page)
	farIdx := 50
	if len(app.sessionList.Items()) <= farIdx {
		farIdx = len(app.sessionList.Items()) - 5
	}
	if farIdx < 0 {
		t.Skip("not enough sessions for far index test")
	}
	app.sessionList.Select(farIdx)
	t.Logf("\n--- Far index test: Select(%d) ---", farIdx)
	t.Logf("Index()=%d PerPage=%d Page=%d", app.sessionList.Index(), app.sessionList.Paginator.PerPage, app.sessionList.Paginator.Page)

	// Zoom
	m, _ = app.Update(tea.WindowSizeMsg{Width: 200, Height: 80})
	app = m.(*App)
	afterFarResize := app.sessionList.Index()
	t.Logf("After zoom: Index()=%d PerPage=%d Page=%d", afterFarResize, app.sessionList.Paginator.PerPage, app.sessionList.Paginator.Page)
	if afterFarResize != farIdx {
		t.Errorf("BUG: Zoom changed far selection from %d to %d", farIdx, afterFarResize)
	}

	_ = app.View()
	afterFarView := app.sessionList.Index()
	t.Logf("After View(): Index()=%d", afterFarView)
	if afterFarView != farIdx {
		t.Errorf("BUG: View() changed far selection from %d to %d", farIdx, afterFarView)
	}

	// Unzoom
	m, _ = app.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app = m.(*App)
	afterFarShrink := app.sessionList.Index()
	t.Logf("After unzoom: Index()=%d PerPage=%d Page=%d", afterFarShrink, app.sessionList.Paginator.PerPage, app.sessionList.Paginator.Page)
	if afterFarShrink != farIdx {
		t.Errorf("BUG: Unzoom changed far selection from %d to %d", farIdx, afterFarShrink)
	}

	_ = app.View()
	afterFarView2 := app.sessionList.Index()
	t.Logf("After View(): Index()=%d", afterFarView2)
	if afterFarView2 != farIdx {
		t.Errorf("BUG: View() after unzoom changed far selection from %d to %d", farIdx, afterFarView2)
	}

	// Multiple rapid resizes (simulate dragging tmux pane border)
	t.Logf("\n--- Rapid resize test ---")
	app.sessionList.Select(10)
	for _, size := range []struct{ w, h int }{
		{130, 45}, {150, 50}, {180, 60}, {200, 80}, {160, 55}, {120, 40},
	} {
		m, _ = app.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		app = m.(*App)
		_ = app.View()
		idx := app.sessionList.Index()
		if idx != 10 {
			t.Errorf("BUG: Rapid resize %dx%d changed selection from 10 to %d", size.w, size.h, idx)
		}
	}
	t.Logf("After rapid resizes: Index()=%d (stable=%v)", app.sessionList.Index(), app.sessionList.Index() == 10)

	// Test with preview pane open
	t.Logf("\n--- Preview open + resize test ---")
	app.sessionList.Select(5)
	// Open preview with Tab
	m, _ = app.Update(tea.KeyMsg{Type: tea.KeyTab})
	app = m.(*App)
	_ = app.View()
	t.Logf("After Tab: sessSplit.Show=%v Index()=%d", app.sessSplit.Show, app.sessionList.Index())
	if app.sessionList.Index() != 5 {
		t.Errorf("BUG: Tab changed selection from 5 to %d", app.sessionList.Index())
	}

	// Resize with preview open
	m, _ = app.Update(tea.WindowSizeMsg{Width: 200, Height: 80})
	app = m.(*App)
	afterPreviewResize := app.sessionList.Index()
	t.Logf("After resize with preview: Index()=%d", afterPreviewResize)
	if afterPreviewResize != 5 {
		t.Errorf("BUG: Resize with preview changed selection from 5 to %d", afterPreviewResize)
	}

	_ = app.View()
	afterPreviewView := app.sessionList.Index()
	t.Logf("After View() with preview: Index()=%d", afterPreviewView)
	if afterPreviewView != 5 {
		t.Errorf("BUG: View() with preview changed selection from 5 to %d", afterPreviewView)
	}

	// Tick after resize with preview
	m, _ = app.Update(tickMsg{})
	app = m.(*App)
	afterPreviewTick := app.sessionList.Index()
	t.Logf("After tick with preview: Index()=%d", afterPreviewTick)
	if afterPreviewTick != 5 {
		t.Errorf("BUG: Tick with preview changed selection from 5 to %d", afterPreviewTick)
	}

	_ = app.View()
	afterPreviewTickView := app.sessionList.Index()
	t.Logf("After View() post-tick: Index()=%d", afterPreviewTickView)
	if afterPreviewTickView != 5 {
		t.Errorf("BUG: View() post-tick changed selection from 5 to %d", afterPreviewTickView)
	}

	// Shrink back with preview
	m, _ = app.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	app = m.(*App)
	_ = app.View()
	afterShrinkPreview := app.sessionList.Index()
	t.Logf("After shrink with preview: Index()=%d", afterShrinkPreview)
	if afterShrinkPreview != 5 {
		t.Errorf("BUG: Shrink with preview changed selection from 5 to %d", afterShrinkPreview)
	}

	// Simulate resize + tick interleaving (real scenario)
	t.Logf("\n--- Resize + tick interleave test ---")
	app.sessionList.Select(8)
	for i := 0; i < 3; i++ {
		m, _ = app.Update(tea.WindowSizeMsg{Width: 200, Height: 80})
		app = m.(*App)
		m, _ = app.Update(tickMsg{})
		app = m.(*App)
		_ = app.View()
		m, _ = app.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
		app = m.(*App)
		m, _ = app.Update(tickMsg{})
		app = m.(*App)
		_ = app.View()
		if app.sessionList.Index() != 8 {
			t.Errorf("BUG: Cycle %d changed selection from 8 to %d", i, app.sessionList.Index())
			break
		}
	}
	t.Logf("After resize+tick cycles: Index()=%d (stable=%v)", app.sessionList.Index(), app.sessionList.Index() == 8)
}
