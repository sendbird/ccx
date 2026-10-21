package cli

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/keyolk/tuikit/selection"
	"github.com/muesli/termenv"
)

func drag(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion}
}

func letGo(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonNone, Action: tea.MouseActionRelease}
}

func feedCmd(m pickerModel, msg tea.Msg) (pickerModel, tea.Cmd) {
	mm, cmd := m.Update(msg)
	return mm.(pickerModel), cmd
}

// whereCell is where measured the way lipgloss lays the picker out and the
// selection reads it (x/ansi). where counts with go-runewidth, which under a
// CJK locale widens ambiguous glyphs (row marks, the border) and shifts every
// column after them.
func whereCell(t *testing.T, m pickerModel, text string) (x, y int) {
	t.Helper()
	for y, line := range strings.Split(ansi.Strip(m.View()), "\n") {
		if i := strings.Index(line, text); i >= 0 {
			return ansi.StringWidth(line[:i]), y
		}
	}
	t.Fatalf("%q not on screen:\n%s", text, ansi.Strip(m.View()))
	return 0, 0
}

func (m pickerModel) selText() string {
	frame, _ := m.screen()
	return m.sel.Text(m.selectionPanes(), frame)
}

// markPickerSelection swaps the highlight for a background the test can find
// (the default test renderer drops styling).
func markPickerSelection(t *testing.T) string {
	t.Helper()
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.ANSI256)
	prev := pickerSelectionStyle
	pickerSelectionStyle = r.NewStyle().Background(lipgloss.Color("201"))
	t.Cleanup(func() { pickerSelectionStyle = prev })
	return "\x1b[48;5;201m"
}

func previewPicker(t *testing.T) (pickerModel, *pickerClock) {
	t.Helper()
	items := linkItems(3)
	var sb strings.Builder
	for i := range 80 {
		fmt.Fprintf(&sb, "ctx-%02d alpha ~/src/beta\n", i)
	}
	items[0].Refs[0].Preview = sb.String()
	return mousePicker(t, "urls", items, 100, 24)
}

func TestPickerDragFromListStopsAtPreview(t *testing.T) {
	m, _ := previewPicker(t)
	mark := markPickerSelection(t)
	x, y := whereCell(t, m, "link-01")
	px, py := whereCell(t, m, "ctx-03")
	m = feed(m, click(x, y))
	m = feed(m, drag(px+5, py))
	m, cmd := feedCmd(m, letGo(px+5, py))
	if cmd == nil {
		t.Fatal("no copy command after a drag")
	}
	got := m.selText()
	if strings.Contains(got, "ctx-") || strings.Contains(got, "│") || !strings.Contains(got, "link-01") {
		t.Fatalf("list drag copied %q; want list text only", got)
	}
	for _, row := range strings.Split(m.View(), "\n") {
		if i := strings.Index(row, mark); i >= 0 {
			rest := row[i+len(mark):]
			cells := rest[:strings.Index(rest, "\x1b[")]
			if ansi.StringWidth(ansi.Strip(row[:i]))+ansi.StringWidth(cells) > m.listWidth() || strings.Contains(cells, "│") {
				t.Fatalf("highlight %q crosses the list edge %d", cells, m.listWidth())
			}
		}
	}
}

func TestPickerDragFromPreviewTowardListStaysInPreview(t *testing.T) {
	m, _ := previewPicker(t)
	// The pane must start where the preview's text is drawn, past the
	// border and padding.
	if hx, _ := whereCell(t, m, "OTHER  link-00"); hx != m.previewTextRect().X {
		t.Fatalf("preview text drawn at column %d, pane starts at %d", hx, m.previewTextRect().X)
	}
	px, py := whereCell(t, m, "ctx-02")
	m = feed(m, click(px+7, py))
	m = feed(m, drag(0, py+1))
	m = feed(m, letGo(0, py+1))
	// The next preview line starts with indentation, so the drag clamped to
	// the preview's first column copies nothing more from it.
	if got := m.selText(); got != "alpha ~/src/beta\n" {
		t.Fatalf("copy = %q; want the preview's own columns, no border or list", got)
	}
}

func TestPickerDragPastPreviewBottomScrolls(t *testing.T) {
	m, _ := previewPicker(t)
	px, py := whereCell(t, m, "ctx-04")
	m = feed(m, click(px, py))
	for range 40 {
		m = feed(m, drag(px+2, m.height-1))
	}
	if m.preview.YOffset == 0 {
		t.Fatal("a drag past the bottom did not scroll the preview")
	}
	m, cmd := feedCmd(m, letGo(px+2, m.height-1))
	got := m.selText()
	if cmd == nil || !strings.HasPrefix(got, "ctx-04") || !strings.Contains(got, "ctx-45") {
		t.Fatalf("copy after scrolling = %q; want ctx-04 through the scrolled-in lines", got)
	}
}

func TestPickerClickWithoutDragStillSelects(t *testing.T) {
	m, clock := previewPicker(t)
	x, y := whereCell(t, m, "link-02")
	m = feed(m, click(x, y))
	m = feed(m, letGo(x, y))
	if m.cursor != 2 || m.sel.Active() {
		t.Fatalf("cursor=%d active=%t; a click must stay a click", m.cursor, m.sel.Active())
	}
	clock.t = clock.t.Add(80 * time.Millisecond)
	m = feed(m, click(x, y))
	if m.result == nil {
		t.Fatal("second click on the selected row is no longer Enter")
	}
}

func TestPickerPreviewDoubleClickSelectsWord(t *testing.T) {
	m, clock := previewPicker(t)
	px, py := whereCell(t, m, "~/src/beta")
	m = feed(m, click(px+3, py))
	clock.t = clock.t.Add(80 * time.Millisecond)
	m, cmd := feedCmd(m, click(px+3, py))
	if got := m.selText(); got != "~/src/beta" || cmd == nil {
		t.Fatalf("double click selected %q", got)
	}
	if m.result != nil {
		t.Fatal("a double click in the preview ran Enter")
	}
}

func TestPickerKeyClearsSelectionAndNotice(t *testing.T) {
	m, _ := previewPicker(t)
	px, py := whereCell(t, m, "alpha")
	m = feed(m, click(px, py))
	m = feed(m, drag(px+4, py))
	m = feed(m, letGo(px+4, py))
	m = feed(m, selection.CopiedMsg{Text: "alpha"})
	if !m.sel.Active() || !strings.Contains(ansi.Strip(m.View()), "copied 5 chars") {
		t.Fatal("drag left no selection or no copy notice")
	}
	m = feed(m, tea.KeyMsg{Type: tea.KeyF12})
	if m.sel.Active() || strings.Contains(ansi.Strip(m.View()), "copied 5 chars") {
		t.Fatal("a key did not clear the selection and notice")
	}
	m = feed(m, selection.CopiedMsg{Text: "x", Err: fmt.Errorf("no clipboard")})
	if !strings.Contains(ansi.Strip(m.View()), "copy failed") {
		t.Fatal("a failed copy is not reported")
	}
}

func TestPickerRefPickerOwnsSelection(t *testing.T) {
	items := linkItems(3)
	items[1].Refs = append(items[1].Refs, ItemRef{EntryUUID: "extra", Role: "user"})
	m, _ := mousePicker(t, "urls", items, 100, 24)
	m.cursor = 1
	m = feed(m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.refPicking {
		t.Fatal("ref picker did not open")
	}
	x, y := whereCell(t, m, "link-00")
	m = feed(m, click(x, y))
	m = feed(m, drag(x+5, y))
	if m.sel.Active() {
		t.Fatal("the list behind the ref picker was selectable")
	}
}

func TestPickerDragSelectsWhileSearching(t *testing.T) {
	m, _ := previewPicker(t)
	m = feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	m = feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("li")})
	term := m.searchInput.Value()
	x, y := whereCell(t, m, "link-00")
	m = feed(m, click(x, y))
	m = feed(m, drag(x+4, y))
	m = feed(m, letGo(x+4, y))
	if m.searchInput.Value() != term || !m.searching || m.cursor != 0 {
		t.Fatalf("a drag edited the search (%q→%q) or moved the cursor (%d)", term, m.searchInput.Value(), m.cursor)
	}
	if got := m.selText(); got != "link-" {
		t.Fatalf("drag while searching selected %q", got)
	}
}

// Under go test the default lipgloss renderer is on the Ascii profile, as
// NO_COLOR makes it; the highlight must still draw.
func TestPickerHighlightSurvivesNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m, _ := previewPicker(t)
	px, py := whereCell(t, m, "alpha")
	m = feed(m, click(px, py))
	m = feed(m, drag(px+4, py))
	if !strings.Contains(m.View(), "\x1b[7malpha") {
		t.Fatal("the picker selection is not drawn in reverse video without color")
	}
}
