package tui

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
	"github.com/sendbird/ccx/internal/session"
)

func motionAt(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion}
}

func releaseAt(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonNone, Action: tea.MouseActionRelease}
}

// sendCmd is send that keeps the command, so a test can assert a copy was
// constructed without running it (running it would write the clipboard).
func sendCmd(app *App, msg tea.Msg) (*App, tea.Cmd) {
	m, cmd := app.Update(msg)
	return m.(*App), cmd
}

// markSelection swaps the highlight for a style the test can find in the
// frame: a 256-color background no other ccx style uses, on an ANSI renderer
// (the default renderer in tests drops all styling).
func markSelection(t *testing.T) string {
	t.Helper()
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.ANSI256)
	prev := selectionStyle
	selectionStyle = r.NewStyle().Background(lipgloss.Color("201"))
	t.Cleanup(func() { selectionStyle = prev })
	return "\x1b[48;5;201m"
}

// highlighted returns, per screen row, the cells drawn with the selection
// mark and the column the first one starts at.
func highlighted(frame, mark string) map[int]struct {
	x    int
	text string
} {
	out := map[int]struct {
		x    int
		text string
	}{}
	for y, row := range strings.Split(frame, "\n") {
		i := strings.Index(row, mark)
		if i < 0 {
			continue
		}
		rest := row[i+len(mark):]
		end := strings.Index(rest, "\x1b[")
		if end < 0 {
			end = len(rest)
		}
		out[y] = struct {
			x    int
			text string
		}{ansi.StringWidth(ansi.Strip(row[:i])), rest[:end]}
	}
	return out
}

// newSelectionApp is the sessions view with the preview open on a long plain
// document, cached under the selected session so clicks do not reload it.
func newSelectionApp(t *testing.T) (*App, *testClock) {
	t.Helper()
	app, clock := newMouseApp(t, 4)
	app.sessSplit.Show = true
	app = send(app, tea.WindowSizeMsg{Width: 120, Height: 24})
	app.sessSplit.CacheKey = fmt.Sprintf("%d:%s", sessPreviewConversation, selectedID(t, app))
	var sb strings.Builder
	for i := range 100 {
		fmt.Fprintf(&sb, "doc-%03d alpha ~/src/beta\n", i)
	}
	app.sessSplit.Preview.SetContent(strings.TrimSuffix(sb.String(), "\n"))
	app.sessSplit.Preview.GotoTop()
	return app, clock
}

// findCell is find measured the way the selection and the terminal place
// cells. find counts with go-runewidth, which under a CJK locale makes the
// ambiguous-width divider "│" two cells wide; the split draws the preview at
// a fixed column (CHA) whatever the locale, and selection measures with
// x/ansi, which does not depend on it.
func findCell(t *testing.T, app *App, text string) (x, y int) {
	t.Helper()
	for y, line := range strings.Split(ansi.Strip(app.View()), "\n") {
		if i := strings.Index(line, text); i >= 0 {
			return ansi.StringWidth(line[:i]), y
		}
	}
	t.Fatalf("%q not on screen:\n%s", text, ansi.Strip(app.View()))
	return 0, 0
}

func (a *App) selectedText() string { return a.sel.Text(a.selectionPanes(), a.renderScreen()) }

func TestDragFromListTowardPreviewStaysInList(t *testing.T) {
	app, _ := newSelectionApp(t)
	mark := markSelection(t)
	x, y := findCell(t, app, "s00  just now") // the list row, not the title
	px, py := findCell(t, app, "doc-002")
	app = send(app, press(x, y))
	app = send(app, motionAt(px+10, py))
	app, cmd := sendCmd(app, releaseAt(px+10, py))
	if cmd == nil {
		t.Fatal("release after a drag built no copy command")
	}
	got := app.selectedText()
	if strings.Contains(got, "doc-") || strings.Contains(got, "│") {
		t.Fatalf("copy crossed into the preview or the border:\n%q", got)
	}
	if !strings.Contains(got, "s00  just now") {
		t.Fatalf("copy %q lost the list text under the press", got)
	}
	listW := app.sessSplit.ListWidth(app.width, app.split)
	frame := app.View()
	rows := highlighted(frame, mark)
	if len(rows) == 0 {
		t.Fatal("no highlight drawn")
	}
	for y, h := range rows {
		if h.x+ansi.StringWidth(h.text) > listW || strings.Contains(h.text, "│") {
			t.Fatalf("row %d highlight %q at %d crosses the list edge %d", y, h.text, h.x, listW)
		}
	}
}

func TestDragFromPreviewTowardListStaysInPreview(t *testing.T) {
	app, _ := newSelectionApp(t)
	mark := markSelection(t)
	px, py := findCell(t, app, "doc-001")
	lx, _ := findCell(t, app, "s00  just now")
	// px is the preview's first column, inside the divider's resize grab
	// zone, so the press starts on the word after it.
	app = send(app, press(px+8, py))
	app = send(app, motionAt(lx, py+1))
	app = send(app, releaseAt(lx, py+1))
	got := app.selectedText()
	if got != "alpha ~/src/beta\nd" {
		t.Fatalf("copy = %q, want the preview columns only", got)
	}
	for y, h := range highlighted(app.View(), mark) {
		if h.x < px || strings.Contains(h.text, "│") || strings.Contains(h.text, "proj") {
			t.Fatalf("row %d highlight %q at %d leaves the preview (starts at %d)", y, h.text, h.x, px)
		}
	}
}

func TestDragPastPreviewBottomScrollsAndKeepsLines(t *testing.T) {
	app, _ := newSelectionApp(t)
	px, py := findCell(t, app, "doc-005")
	px += 4 // past the divider's resize grab zone
	// The footer row is below the preview; dragging there scrolls one line
	// per motion event.
	below := app.height - 1
	for range 30 {
		app = send(app, motionAt(px, py)) // no-op until the press
	}
	app = send(app, press(px, py))
	for range 30 {
		app = send(app, motionAt(px+3, below))
	}
	if app.sessSplit.Preview.YOffset == 0 {
		t.Fatal("dragging past the bottom did not scroll the preview")
	}
	app, cmd := sendCmd(app, releaseAt(px+3, below))
	if cmd == nil {
		t.Fatal("no copy command after the drag")
	}
	got := app.selectedText()
	lines := strings.Split(got, "\n")
	if !strings.HasPrefix(lines[0], "005 alpha") || len(lines) < 40 {
		t.Fatalf("copy kept %d lines starting %q; want doc-005 through the scrolled-in lines", len(lines), lines[0])
	}
	if !strings.Contains(got, "doc-030") {
		t.Fatalf("copy lost a line that scrolled through the pane:\n%s", got)
	}
}

func TestPressWithoutMotionIsStillAClick(t *testing.T) {
	app, _ := newSelectionApp(t)
	x, y := findCell(t, app, "proj-s02")
	app = send(app, press(x, y))
	app, cmd := sendCmd(app, releaseAt(x, y))
	if got := selectedID(t, app); got != "s02" {
		t.Fatalf("plain click selected %q, want s02", got)
	}
	if app.sel.Active() {
		t.Fatal("a click without motion left a selection")
	}
	_ = cmd
}

func TestListSecondClickIsStillEnterWithReleases(t *testing.T) {
	app, clock := newMouseApp(t, 6)
	x, y := findCell(t, app, "proj-s03")
	app = send(app, press(x, y))
	app = send(app, releaseAt(x, y))
	clock.advance(100 * time.Millisecond)
	app = send(app, press(x, y))
	if app.state != viewConversation || app.currentSess.ID != "s03" {
		t.Fatalf("second click on a list row did not run Enter: state=%d", app.state)
	}
}

func TestPreviewDoubleClickSelectsWordNotEnter(t *testing.T) {
	app, clock := newSelectionApp(t)
	px, py := findCell(t, app, "~/src/beta")
	app = send(app, press(px+2, py))
	app = send(app, releaseAt(px+2, py))
	clock.advance(100 * time.Millisecond)
	app, cmd := sendCmd(app, press(px+2, py))
	if app.state != viewSessions {
		t.Fatalf("double click in a text preview ran Enter (state=%d)", app.state)
	}
	if got := app.selectedText(); got != "~/src/beta" || cmd == nil {
		t.Fatalf("double click selected %q (cmd %v), want the path word", got, cmd != nil)
	}
}

func TestKeyClearsSelection(t *testing.T) {
	app, _ := newSelectionApp(t)
	mark := markSelection(t)
	px, py := findCell(t, app, "alpha")
	app = send(app, press(px, py))
	app = send(app, motionAt(px+6, py))
	app = send(app, releaseAt(px+6, py))
	if !app.sel.Active() || len(highlighted(app.View(), mark)) == 0 {
		t.Fatal("drag left no highlight")
	}
	app = send(app, tea.KeyMsg{Type: tea.KeyF12})
	if app.sel.Active() || len(highlighted(app.View(), mark)) != 0 {
		t.Fatal("a key did not clear the selection")
	}
}

func TestOverlayOwnsSelection(t *testing.T) {
	app, _ := newSelectionApp(t)
	app.showHelp = true
	panes := app.selectionPanes()
	if len(panes) != 1 || panes[0].ID != "overlay/help" {
		t.Fatalf("panes under the help overlay = %+v, want only the overlay", panes)
	}
	// A drag across the base list, left of the overlay, selects nothing.
	_, ly := findCell(t, app, "s01  1m")
	r := panes[0].Rect
	if r.Contains(0, ly) {
		t.Skip("overlay covers the list row; layout changed")
	}
	// Inside the overlay a drag selects overlay text only.
	ox, oy := r.X+1, r.Y+1
	app = send(app, press(ox, oy))
	app = send(app, motionAt(ox+200, oy+1))
	app = send(app, releaseAt(ox+200, oy+1))
	if !app.showHelp {
		t.Fatal("a drag inside the help overlay closed it")
	}
	if got := app.selectedText(); got == "" || strings.Contains(got, "proj-s0") || strings.Contains(got, "doc-0") {
		t.Fatalf("overlay selection = %q; want overlay text only", got)
	}
	// A press on the base outside the overlay is the overlay's Esc, never the
	// start of a selection underneath.
	app = send(app, press(0, ly))
	app = send(app, motionAt(8, ly))
	if app.sel.Active() {
		t.Fatal("the base under an open overlay was selectable")
	}
}

func TestCopiedMsgShowsToast(t *testing.T) {
	app, _ := newSelectionApp(t)
	app = send(app, selection.CopiedMsg{Text: "héllo"})
	if tt, ok := app.statusToast(); !ok || tt.Text != "Copied 5 chars" {
		t.Fatalf("toast = %+v, want Copied 5 chars", tt)
	}
	app = send(app, selection.CopiedMsg{Text: "x", Err: fmt.Errorf("no clipboard")})
	if tt, _ := app.statusToast(); !strings.Contains(tt.Text, "Copy failed") {
		t.Fatalf("toast = %q, want an error", tt.Text)
	}
}

// The conversation preview's blocks fold on a double click (activateClicked);
// that is the preview's Enter, so the pane is a List and the second press
// must reach the app instead of becoming a word pick. A drag in the same pane
// still selects transcript text.
func TestConversationPreviewKeepsFoldDoubleClickAndSelects(t *testing.T) {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	app := setupConvApp(t, []session.Entry{
		makeTextEntry("user", base, "Hello"),
		makeGrowingEntry(base.Add(time.Second), 3),
	}, 160, 40)
	app.convList.Select(1)
	app.updateConvPreview()
	clock := fixedClock(time.Now())
	app.clicks.now = clock.now

	x, y := findCell(t, app, "Block 1:")
	x += 2 // inside the block text
	app = send(app, press(x, y))
	app = send(app, releaseAt(x, y))
	clock.advance(100 * time.Millisecond)
	app, _ = sendCmd(app, press(x, y))
	if app.sel.Active() {
		t.Fatalf("double click in the fold preview selected %q; it belongs to the fold toggle", app.selectedText())
	}

	clock.advance(time.Second)
	x, y = findCell(t, app, "Block 0:")
	x += 2
	app = send(app, press(x, y))
	app = send(app, motionAt(x+12, y))
	app, cmd := sendCmd(app, releaseAt(x+12, y))
	if got := app.selectedText(); !strings.Contains(got, "ck 0:") || cmd == nil {
		t.Fatalf("drag in the conversation preview copied %q (cmd %v)", got, cmd != nil)
	}
}

// The divider's cells are the split's and no pane's: the cell just left of
// it is the list's (the old resize grabbed it too), so a drag from there
// selects list text and leaves the ratio alone.
func TestDragNextToDividerSelectsInsteadOfResizing(t *testing.T) {
	app, _ := newSelectionApp(t)
	px, py := findCell(t, app, "doc-003")
	border := px - 1 // the column drawn just left of the preview text
	before := app.split.Ratio()
	app = send(app, press(border-1, py))
	// Down a row as well: on the press row the selection would clamp the
	// motion back onto the press cell and never count it as a drag.
	app = send(app, motionAt(border+15, py+2))
	app = send(app, releaseAt(border+15, py+2))
	if app.split.Ratio() != before || !app.sel.Active() {
		t.Fatalf("press beside the divider: ratio %v→%v, selection %t; want a selection", before, app.split.Ratio(), app.sel.Active())
	}
}

// The real highlight must show without color. Under go test the default
// lipgloss renderer is on the Ascii profile, exactly as NO_COLOR makes it,
// so a style from that renderer would draw nothing here.
func TestSelectionHighlightSurvivesNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	app, _ := newSelectionApp(t)
	px, py := findCell(t, app, "alpha")
	app = send(app, press(px, py))
	app = send(app, motionAt(px+4, py))
	if !strings.Contains(app.View(), "\x1b[7malpha") {
		t.Fatal("the selection is not drawn in reverse video without color")
	}
}

// A preview the wheel already scrolled maps rows through its offset: the
// copy is the text under the pointer, not the line that many rows from the
// top of the document.
func TestDragInScrolledPreviewCopiesWhatIsShown(t *testing.T) {
	app, _ := newSelectionApp(t)
	px, py := findCell(t, app, "doc-001")
	app = send(app, wheel(px+4, py, true))
	app = send(app, wheel(px+4, py, true))
	if app.sessSplit.Preview.YOffset == 0 {
		t.Fatal("wheel did not scroll the preview")
	}
	px, py = findCell(t, app, "doc-010")
	app = send(app, press(px+4, py))
	app = send(app, motionAt(px+6, py+1))
	app = send(app, releaseAt(px+6, py+1))
	if got := app.selectedText(); got != "010 alpha ~/src/beta\ndoc-011" {
		t.Fatalf("copy in a scrolled preview = %q", got)
	}
}
