package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// View must not write model state. renderSessionSplit used to resize the
// preview viewport while drawing; sizing now happens after Update.
func TestRenderSessionSplitDoesNotResizePreview(t *testing.T) {
	a := newTestApp(fakeSessions())
	a.sessSplit.Show = true
	a.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	w, h := a.sessSplit.Preview.Width, a.sessSplit.Preview.Height
	if w == 0 || h == 0 {
		t.Fatalf("Update did not size the preview: %dx%d", w, h)
	}

	// Change the size behind Update's back: a pure View must not react.
	a.width, a.height = 120, 40
	first := a.View()
	if a.sessSplit.Preview.Width != w || a.sessSplit.Preview.Height != h {
		t.Fatalf("View resized the preview to %dx%d (was %dx%d)",
			a.sessSplit.Preview.Width, a.sessSplit.Preview.Height, w, h)
	}
	if second := a.View(); second != first {
		t.Fatal("rendering twice produced different output")
	}

	// The next Update applies the new size.
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	if a.sessSplit.Preview.Width == w {
		t.Fatal("Update did not apply the new preview width")
	}
}
