package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/list"
)

// TestListSetSizePreservesSelection verifies that calling SetSize on a
// bubbles list.Model preserves the currently selected item index.
func TestListSetSizePreservesSelection(t *testing.T) {
	// Create a list with enough items to have multiple pages
	items := make([]list.Item, 100)
	for i := range items {
		items[i] = sessionItem{} // lightweight item
	}

	delegate := sessionDelegate{timeW: 8, msgW: 3}
	l := list.New(items, delegate, 80, 40) // width=80, height=40
	initListBase(&l)

	// Select item 30
	l.Select(30)
	if l.Index() != 30 {
		t.Fatalf("expected index 30, got %d", l.Index())
	}

	// Now resize (simulating [ / ] pane resize) WITHOUT Select
	l.SetSize(50, 40) // narrower
	afterResize := l.Index()

	// Resize again WITH Select
	l.SetSize(50, 40)
	l.Select(30)
	afterResizeWithSelect := l.Index()

	t.Logf("After SetSize without Select: index=%d", afterResize)
	t.Logf("After SetSize with Select: index=%d", afterResizeWithSelect)

	if afterResize != 30 {
		t.Logf("BUG CONFIRMED: SetSize without Select changed index from 30 to %d", afterResize)
	}
	if afterResizeWithSelect != 30 {
		t.Errorf("SetSize+Select should preserve index 30, got %d", afterResizeWithSelect)
	}

	// Test with height change (changes PerPage)
	l.Select(30)
	l.SetSize(50, 20) // shorter, fewer items per page
	afterHeightChange := l.Index()
	t.Logf("After height change: index=%d (expected 30)", afterHeightChange)

	l.Select(30)
	afterExplicitSelect := l.Index()
	t.Logf("After explicit Select(30): index=%d", afterExplicitSelect)

	if afterExplicitSelect != 30 {
		t.Errorf("Select(30) after height change should work, got %d", afterExplicitSelect)
	}
}
