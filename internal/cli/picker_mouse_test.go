package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
	"github.com/sendbird/ccx/internal/extract"
	"github.com/sendbird/ccx/internal/opener"
)

type pickerClock struct{ t time.Time }

func (c *pickerClock) now() time.Time { return c.t }

func mousePicker(t *testing.T, kind string, items []PickerItem, w, h int) (pickerModel, *pickerClock) {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // newPickerModel reads ~/.config/ccx/config.yaml
	m := newPickerModel(kind, items, opener.Config{}, pickerContext{command: kind})
	mm, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m = mm.(pickerModel)
	clock := &pickerClock{t: time.Now()}
	m.clicks.now = clock.now
	return m, clock
}

func linkItems(n int) []PickerItem {
	var items []PickerItem
	for i := 0; i < n; i++ {
		items = append(items, PickerItem{
			Item:      extract.Item{URL: fmt.Sprintf("https://example.com/%d", i), Label: fmt.Sprintf("link-%02d", i), Category: "other"},
			SessionID: "s1",
			Refs:      []ItemRef{{EntryUUID: fmt.Sprintf("u%d", i), Role: "user"}},
		})
	}
	return items
}

// where finds text on the rendered screen, so a test clicks where the user
// would, not where the code's own arithmetic says the row is.
func where(t *testing.T, m pickerModel, text string) (x, y int) {
	t.Helper()
	for y, line := range strings.Split(ansi.Strip(m.View()), "\n") {
		if i := strings.Index(line, text); i >= 0 {
			return runewidth.StringWidth(line[:i]), y
		}
	}
	t.Fatalf("%q not on screen:\n%s", text, ansi.Strip(m.View()))
	return 0, 0
}

func click(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
}

func notch(x, y int, down bool) tea.MouseMsg {
	b := tea.MouseButtonWheelUp
	if down {
		b = tea.MouseButtonWheelDown
	}
	return tea.MouseMsg{X: x, Y: y, Button: b, Action: tea.MouseActionPress}
}

func feed(m pickerModel, msg tea.Msg) pickerModel {
	mm, _ := m.Update(msg)
	return mm.(pickerModel)
}

func TestPickerClickSelectsAndSecondClickIsEnter(t *testing.T) {
	m, clock := mousePicker(t, "urls", linkItems(10), 100, 24)
	x, y := where(t, m, "link-04")
	m = feed(m, click(x, y))
	if m.cursor != 4 || m.result != nil {
		t.Fatalf("cursor=%d result=%v; first click must only select row 4", m.cursor, m.result)
	}
	clock.t = clock.t.Add(100 * time.Millisecond)
	m = feed(m, click(x, y))
	if m.result == nil || m.result.EntryUUID != "u4" {
		t.Fatalf("second click did not run Enter on row 4: %+v", m.result)
	}
}

func TestPickerSecondClickOnAnotherRowOnlySelects(t *testing.T) {
	m, clock := mousePicker(t, "urls", linkItems(10), 100, 24)
	x, y := where(t, m, "link-02")
	m = feed(m, click(x, y))
	clock.t = clock.t.Add(50 * time.Millisecond)
	x, y = where(t, m, "link-05")
	m = feed(m, click(x, y))
	if m.cursor != 5 || m.result != nil {
		t.Fatalf("cursor=%d result=%v; rows 2 then 5 must not activate", m.cursor, m.result)
	}
}

func TestPickerWheelMovesThree(t *testing.T) {
	m, _ := mousePicker(t, "urls", linkItems(10), 100, 24)
	x, y := where(t, m, "link-00")
	m = feed(m, notch(x, y, true))
	if m.cursor != 3 {
		t.Fatalf("cursor=%d after one notch down, want 3", m.cursor)
	}
	m = feed(m, notch(x, y, false))
	if m.cursor != 0 {
		t.Fatalf("cursor=%d after one notch up, want 0", m.cursor)
	}
}

func TestPickerWheelOverPreviewScrollsItThree(t *testing.T) {
	items := linkItems(3)
	for i := range items[0].Refs {
		items[0].Refs[i].Preview = strings.Repeat("context line\n", 60)
	}
	m, _ := mousePicker(t, "urls", items, 100, 24)
	px := m.listWidth() + 5
	m = feed(m, notch(px, 5, true))
	if m.preview.YOffset != 3 || m.cursor != 0 {
		t.Fatalf("preview offset=%d cursor=%d after a notch over the preview, want 3 and 0", m.preview.YOffset, m.cursor)
	}
}

func TestPickerEmptyAreaAndOtherGesturesDoNothing(t *testing.T) {
	m, clock := mousePicker(t, "urls", linkItems(3), 100, 24)
	for _, msg := range []tea.MouseMsg{
		click(3, 15), // empty rows below three items
		click(3, 15),
		click(3, 0),  // title
		click(3, 23), // footer
		{X: 3, Y: 2, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease},
		{X: 3, Y: 2, Button: tea.MouseButtonNone, Action: tea.MouseActionMotion},
		{X: 3, Y: 2, Button: tea.MouseButtonRight, Action: tea.MouseActionPress},
		click(m.listWidth()+5, 2), // preview pane
	} {
		m = feed(m, msg)
		clock.t = clock.t.Add(10 * time.Millisecond)
		if m.cursor != 0 || m.result != nil || m.quit {
			t.Fatalf("%+v changed state: cursor=%d result=%v", msg, m.cursor, m.result)
		}
	}
}

func TestPickerIgnoresMouseWhileSearching(t *testing.T) {
	m, _ := mousePicker(t, "urls", linkItems(10), 100, 24)
	m = feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	x, y := where(t, m, "link-03")
	m = feed(m, click(x, y))
	m = feed(m, notch(x, y, true))
	if m.cursor != 0 || !m.searching {
		t.Fatalf("cursor=%d searching=%t; the search line owns input", m.cursor, m.searching)
	}
}

func TestPickerRefPickerOwnsTheMouse(t *testing.T) {
	items := linkItems(3)
	items[1].Refs = append(items[1].Refs, ItemRef{EntryUUID: "extra", Role: "user"}, ItemRef{EntryUUID: "extra2", Role: "user"}, ItemRef{EntryUUID: "extra3", Role: "user"}, ItemRef{EntryUUID: "extra4", Role: "user"})
	m, _ := mousePicker(t, "urls", items, 100, 24)
	m.cursor = 1
	m = feed(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.refPicking {
		t.Fatal("Enter on a multi-ref item did not open the ref picker")
	}
	px := m.listWidth() + 5
	m = feed(m, notch(px, 5, true))
	if m.refCursor != 3 {
		t.Fatalf("refCursor=%d after one notch, want 3", m.refCursor)
	}
	x, y := where(t, m, "link-00")
	m = feed(m, click(x, y)) // a row behind the ref picker
	if m.refPicking || m.cursor != 1 {
		t.Fatalf("outside click: refPicking=%t cursor=%d; want Esc, and row 0 left unselected", m.refPicking, m.cursor)
	}
}

func TestPickerConversationRowsHitMultiLineItems(t *testing.T) {
	var items []PickerItem
	for i := 0; i < 8; i++ {
		items = append(items, PickerItem{
			Item:             extract.Item{URL: fmt.Sprintf("conversation:%d", i), Label: fmt.Sprintf("turn-%02d first line", i), Category: "conversation"},
			SessionID:        "s1",
			Refs:             []ItemRef{{EntryUUID: fmt.Sprintf("u%d", i), Role: "user"}},
			ConversationText: "body line one\nbody line two",
		})
	}
	m, _ := mousePicker(t, "conversation", items, 120, 30)
	x, y := where(t, m, "turn-03")
	// The row's last line belongs to the same item as its header.
	_, heights := m.renderListLines(m.height-2, m.listWidth(), lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle())
	if heights[3] < 2 {
		t.Fatalf("test needs multi-line conversation rows, got heights %v", heights)
	}
	m = feed(m, click(x, y+heights[3]-1))
	if m.cursor != 3 {
		t.Fatalf("click on the last line of item 3 selected %d", m.cursor)
	}
}

// With more items than rows the list scrolls; a click must map through the
// first visible item, not row 0.
func TestPickerClickRespectsScrollOffset(t *testing.T) {
	m, _ := mousePicker(t, "urls", linkItems(60), 100, 24)
	for range 45 {
		m = feed(m, tea.KeyMsg{Type: tea.KeyDown})
	}
	if m.listFirstVisible(m.height-2) == 0 {
		t.Fatal("list did not scroll")
	}
	x, y := where(t, m, "link-40")
	m = feed(m, click(x, y))
	if m.cursor != 40 {
		t.Fatalf("click on link-40 in a scrolled list selected %d", m.cursor)
	}
}
