package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/keyolk/tuikit/prefs"
	"github.com/keyolk/tuikit/split"
	"github.com/sendbird/ccx/internal/session"
)

// cellAt is the text of the one cell at column x of screen row y.
func cellAt(app *App, x, y int) string {
	rows := strings.Split(ansi.Strip(app.View()), "\n")
	if y < 0 || y >= len(rows) {
		return ""
	}
	return ansi.Cut(rows[y], x, x+1)
}

// dividerAt finds the divider on the row of text, a preview line: the cell
// drawn just left of it, which must be the divider glyph. Positions come
// from the frame, never from the layout code under test.
func dividerAt(t *testing.T, app *App, text string) (x, y int) {
	t.Helper()
	px, py := findCell(t, app, text)
	if g := cellAt(app, px-1, py); g != "│" && g != "┃" {
		t.Fatalf("no divider left of %q at (%d,%d): %q\n%s", text, px-1, py, g, ansi.Strip(app.View()))
	}
	return px - 1, py
}

// dividerLeftOf is dividerAt for a preview with a gutter or a cursor mark:
// the divider glyph within a few cells left of text on its row.
func dividerLeftOf(t *testing.T, app *App, text string) (x, y int) {
	t.Helper()
	px, py := findCell(t, app, text)
	for x = px - 1; x >= max(px-6, 0); x-- {
		if g := cellAt(app, x, py); g == "│" || g == "┃" {
			return x, py
		}
	}
	t.Fatalf("no divider left of %q on row %d:\n%s", text, py, ansi.Strip(app.View()))
	return 0, 0
}

func paneRect(t *testing.T, app *App, id string) (x, w int) {
	t.Helper()
	for _, p := range app.selectionPanes() {
		if p.ID == id {
			return p.Rect.X, p.Rect.W
		}
	}
	t.Fatalf("no selection pane %q", id)
	return 0, 0
}

// A drag on the divider found in the frame moves it; View, the selection
// panes and click hit tests all see the new boundary, and nothing selects.
func TestSplitDividerDragMovesBoundaryEverywhere(t *testing.T) {
	app, clock := newSelectionApp(t)
	dx, dy := dividerAt(t, app, "doc-003")

	app = send(app, press(dx, dy))
	if !app.split.Dragging() || app.sel.Active() {
		t.Fatalf("divider press: dragging %t, selection %t; want a drag and no selection", app.split.Dragging(), app.sel.Active())
	}
	app = send(app, motionAt(dx+12, dy+1))
	if g := cellAt(app, dx+12, dy); g != "┃" {
		t.Fatalf("divider during the drag = %q at %d, want the heavy glyph", g, dx+12)
	}
	app = send(app, releaseAt(dx+12, dy+1))
	if app.split.Dragging() || app.sel.Active() {
		t.Fatalf("after release: dragging %t, selection %t", app.split.Dragging(), app.sel.Active())
	}

	nx, _ := dividerAt(t, app, "doc-003")
	if nx != dx+12 {
		t.Fatalf("divider at %d after dragging %d → %d", nx, dx, dx+12)
	}
	if x, w := paneRect(t, app, "sessions/list"); x != 0 || w != nx {
		t.Fatalf("list selection pane x=%d w=%d, want the %d columns left of the divider", x, w, nx)
	}
	if x, _ := paneRect(t, app, "sessions/preview"); x != nx+1 {
		t.Fatalf("preview selection pane starts at %d, want %d", x, nx+1)
	}

	// Old preview columns are list now: a click there focuses the list.
	_, ly := findCell(t, app, "s00  just now")
	app.sessSplit.Focus = true
	clock.advance(time.Second)
	app = send(app, press(dx+5, ly))
	if app.sessSplit.Focus {
		t.Fatalf("click at %d (left of the new divider %d) focused the preview", dx+5, nx)
	}
	clock.advance(time.Second)
	app = send(app, press(nx+3, dy))
	if !app.sessSplit.Focus {
		t.Fatalf("click at %d (right of the new divider %d) did not focus the preview", nx+3, nx)
	}
}

// A press in a pane is never the split's: it still starts a selection.
func TestSplitPanePressStillSelects(t *testing.T) {
	app, _ := newSelectionApp(t)
	px, py := findCell(t, app, "doc-003")
	before := app.split.Ratio()
	app = send(app, press(px, py))
	app = send(app, motionAt(px+6, py))
	if app.split.Dragging() || app.split.Ratio() != before || !app.sel.Active() {
		t.Fatalf("preview drag: split dragging %t ratio %v→%v, selection %t", app.split.Dragging(), before, app.split.Ratio(), app.sel.Active())
	}
}

func TestSplitDoubleClickOnDividerResets(t *testing.T) {
	app, clock := newSelectionApp(t)
	dx, dy := dividerAt(t, app, "doc-003")
	app = send(app, press(dx, dy))
	app = send(app, motionAt(dx+15, dy))
	app = send(app, releaseAt(dx+15, dy))
	nx, _ := dividerAt(t, app, "doc-003")
	if nx == dx {
		t.Fatal("drag did not move the divider")
	}
	clock.advance(time.Second)
	app = send(app, press(nx, dy))
	app = send(app, releaseAt(nx, dy))
	clock.advance(100 * time.Millisecond)
	app = send(app, press(nx, dy))
	app = send(app, releaseAt(nx, dy))
	if got, _ := dividerAt(t, app, "doc-003"); got != dx {
		t.Fatalf("double click on the divider left it at %d, want the default %d", got, dx)
	}
}

// > grows the list, < shrinks it, = resets, each visible in the frame.
func TestSplitKeysMoveDivider(t *testing.T) {
	app, _ := newSelectionApp(t)
	dx, _ := dividerAt(t, app, "doc-003")
	app = send(app, keyRunes(">"))
	gx, _ := dividerAt(t, app, "doc-003")
	if gx <= dx {
		t.Fatalf("> left the divider at %d (was %d)", gx, dx)
	}
	app = send(app, keyRunes("<"))
	app = send(app, keyRunes("<"))
	sx, _ := dividerAt(t, app, "doc-003")
	if sx >= dx {
		t.Fatalf("<< left the divider at %d (default %d)", sx, dx)
	}
	app = send(app, keyRunes("="))
	if rx, _ := dividerAt(t, app, "doc-003"); rx != dx {
		t.Fatalf("= left the divider at %d, want the default %d", rx, dx)
	}
	// The older keys still resize where they always did.
	app = send(app, keyRunes("]"))
	if bx, _ := dividerAt(t, app, "doc-003"); bx <= dx {
		t.Fatalf("] left the divider at %d (was %d)", bx, dx)
	}
}

// Pressing > past the bound and then < once moves the divider at once: no
// dead zone of presses that change nothing.
func TestSplitClampHasNoDeadZone(t *testing.T) {
	app, _ := newSelectionApp(t)
	for range 40 {
		app = send(app, keyRunes(">"))
	}
	maxX, _ := dividerAt(t, app, "doc-003")
	if prev := app.width - maxX - 1; prev < 20 {
		t.Fatalf("preview is %d columns at the bound, want at least 20", prev)
	}
	app = send(app, keyRunes("<"))
	if x, _ := dividerAt(t, app, "doc-003"); x >= maxX {
		t.Fatalf("one < after the bound left the divider at %d (bound %d)", x, maxX)
	}
	for range 40 {
		app = send(app, keyRunes("<"))
	}
	minX, _ := dividerAt(t, app, "doc-003")
	if minX < 30 {
		t.Fatalf("list is %d columns at the low bound, want at least 30", minX)
	}
	app = send(app, keyRunes(">"))
	if x, _ := dividerAt(t, app, "doc-003"); x <= minX {
		t.Fatalf("one > after the low bound left the divider at %d (bound %d)", x, minX)
	}
}

func TestSplitKeysShowInHelpAndFooter(t *testing.T) {
	app, _ := newSelectionApp(t)
	app = send(app, keyRunes("?"))
	if out := ansi.Strip(app.View()); !strings.Contains(out, "< / > / =") {
		t.Fatalf("? overlay does not list the split keys:\n%s", out)
	}

	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	conv := setupConvApp(t, []session.Entry{
		makeTextEntry("user", base, "Hello"),
		makeTextEntry("assistant", base.Add(time.Second), "Hi"),
	}, 260, 40)
	conv.conv.split.Show = true
	if out := ansi.Strip(conv.View()); !strings.Contains(out, "<>= resize") {
		t.Fatalf("conversation footer does not show the split keys:\n%s", out)
	}
}

// The split keys are free in every scope: nothing else is bound to < > =.
func TestSplitKeysDoNotShadowBindings(t *testing.T) {
	km := DefaultKeymap()
	if c := km.Conflicts(); len(c) != 0 {
		t.Fatalf("Conflicts() = %v", c)
	}
	own := map[string]bool{"split_shrink": true, "split_grow": true, "split_reset": true}
	for _, b := range bindingTable {
		k := km.bindingKey(b.scope, b.name)
		if (k == "<" || k == ">" || k == "=") && !own[b.name] {
			t.Fatalf("%s.%s is bound to %q, which the split uses", b.scope, b.name, k)
		}
	}
	for _, sb := range split.Bindings("session") {
		if got := splitKey(sb.Action); got != sb.Keys[0] {
			t.Fatalf("default for %s = %q, want the kit's %q", sb.Action, got, sb.Keys[0])
		}
	}
}

// In the config view [ and ] keep cycling pages; < and > resize.
func TestSplitKeysInConfigViewKeepPageCycling(t *testing.T) {
	app := setupCfgApp(t)
	app.cfgTree = &session.ConfigTree{}
	app.plgTree = &session.PluginTree{}
	before, page := app.split.Ratio(), app.currentCfgPage()
	app = send(app, keyRunes("]"))
	if app.split.Ratio() != before || app.currentCfgPage() == page {
		t.Fatalf("] in config: ratio %v→%v, page %d→%d; want a page change only", before, app.split.Ratio(), page, app.currentCfgPage())
	}
	app = send(app, keyRunes(">"))
	if app.split.Ratio() <= before {
		t.Fatalf("> in config left the ratio at %v (was %v)", app.split.Ratio(), before)
	}
}

func TestSetRatioCommandIsAPercentOfTheSplit(t *testing.T) {
	app, _ := newSelectionApp(t)
	app.executeCmdSetRatio("set:ratio 50")
	if got := app.split.Ratio(); got != 0.5 {
		t.Fatalf("set:ratio 50 → %v, want 0.5", got)
	}
	// 99 is past the 85% bound, and at 120 columns 85% would leave the
	// preview under its 20 columns: the preview keeps exactly 20.
	app.executeCmdSetRatio("set:ratio 99")
	if dx, _ := dividerAt(t, app, "doc-003"); app.width-dx-1 != 20 {
		t.Fatalf("set:ratio 99 left a %d-column preview, want the 20-column minimum", app.width-dx-1)
	}
}

func TestSplitNarrowTerminal(t *testing.T) {
	app, _ := newSelectionApp(t)
	for _, size := range [][2]int{{60, 24}, {80, 24}, {45, 24}} {
		app = send(app, tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for y, line := range strings.Split(app.View(), "\n") {
			if w := ansi.StringWidth(line); w > size[0] {
				t.Fatalf("%dx%d row %d is %d cells wide", size[0], size[1], y, w)
			}
		}
	}
	// 45 columns cannot fit a 30-column list, the divider and a 20-column
	// preview: the list is shown alone, full width.
	if out := ansi.Strip(app.View()); strings.Contains(out, "doc-") {
		t.Fatalf("45 columns still shows the preview:\n%s", out)
	}
	if w := app.sessionList.Width(); w != 45 {
		t.Fatalf("collapsed list is %d wide, want 45", w)
	}
	// 60 columns fits both, with the minimums held.
	app = send(app, tea.WindowSizeMsg{Width: 60, Height: 24})
	dx, _ := dividerAt(t, app, "doc-003")
	if dx < 30 || 60-dx-1 < 20 {
		t.Fatalf("60 columns: divider at %d breaks the 30/20 minimums", dx)
	}
}

func writeUIJSON(t *testing.T, body string) {
	t.Helper()
	p := prefs.Path(prefsApp)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readUIJSON(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(prefs.Path(prefsApp))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSplitRatioSavedAndRestored(t *testing.T) {
	withUIStateDir(t)
	a := NewApp(fakeSessions(), Config{})
	a = send(a, tea.WindowSizeMsg{Width: 120, Height: 24})
	a.state, a.sessSplit.Show = viewSessions, true
	a = send(a, keyRunes(">"))
	want := a.split.Ratio()
	if want == defaultSplitRatio {
		t.Fatal("> did not change the ratio")
	}
	quitAndWait(t, a)
	if got := readUIJSON(t); !strings.Contains(got, `"split": {`) || !strings.Contains(got, `"ratio"`) || strings.Contains(got, "split_ratio") {
		t.Fatalf("ui.json = %s", got)
	}
	if got := NewApp(fakeSessions(), Config{}).split.Ratio(); got != want {
		t.Fatalf("restored ratio %v, want %v", got, want)
	}
}

func TestSplitGarbageInUIJSONFallsBackToDefault(t *testing.T) {
	for _, body := range []string{
		`{"split": {"ratio": 40}}`,  // a percentage in the fraction field
		`{"split": {"ratio": -1}}`,  // out of range
		`{"split": {"ratio": 1.5}}`, // out of range
		`{"split_ratio": 99}`,       // an old percentage outside 15-85
	} {
		t.Run(body, func(t *testing.T) {
			withUIStateDir(t)
			writeUIJSON(t, body)
			if got := NewApp(fakeSessions(), Config{}).split.Ratio(); got != defaultSplitRatio {
				t.Fatalf("ratio %v from %s, want the default %v", got, body, defaultSplitRatio)
			}
		})
	}
}

// split_ratio (a percentage) from an older ui.json or config.yaml is read
// once; the next save writes split.State and drops it.
func TestSplitMigratesOldPercent(t *testing.T) {
	t.Run("ui.json", func(t *testing.T) {
		withUIStateDir(t)
		writeUIJSON(t, `{"split_ratio": 50}`)
		a := NewApp(fakeSessions(), Config{})
		if got := a.split.Ratio(); got != 0.5 {
			t.Fatalf("migrated ratio %v, want 0.5", got)
		}
		quitAndWait(t, a)
		if got := readUIJSON(t); strings.Contains(got, "split_ratio") || !strings.Contains(got, `"ratio": 0.5`) {
			t.Fatalf("ui.json after migration = %s", got)
		}
	})
	t.Run("config.yaml", func(t *testing.T) {
		cfg := withUIStateDir(t)
		if err := os.WriteFile(cfg, []byte("preferences:\n  split_ratio: 60\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := NewApp(fakeSessions(), Config{}).split.Ratio(); got != 0.6 {
			t.Fatalf("migrated ratio %v, want 0.6", got)
		}
	})
	t.Run("new field wins", func(t *testing.T) {
		withUIStateDir(t)
		writeUIJSON(t, `{"split_ratio": 50, "split": {"ratio": 0.7}}`)
		if got := NewApp(fakeSessions(), Config{}).split.Ratio(); got != 0.7 {
			t.Fatalf("ratio %v, want 0.7 from split.ratio", got)
		}
	})
}

// A resize key re-wraps the conversation preview at the new width: every
// word stays on screen, in order, none cut at the right edge.
func TestSplitKeysRewrapConversationPreview(t *testing.T) {
	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	var words []string
	for i := range 60 {
		words = append(words, fmt.Sprintf("w%03d", i))
	}
	app := setupConvApp(t, []session.Entry{
		makeTextEntry("user", base, strings.Join(words, " ")),
	}, 120, 40)
	app.convList.Select(0)
	app.conv.split.Show = true
	app.updateConvPreview()
	app = send(app, tea.WindowSizeMsg{Width: 120, Height: 40})
	for range 4 {
		app = send(app, keyRunes(">"))
	}
	assertAllWords(t, app, "keys")
	// A drag re-wraps it too.
	dx, dy := dividerLeftOf(t, app, "usr  09:00:00")
	app = send(app, press(dx, dy))
	app = send(app, motionAt(dx-25, dy))
	app = send(app, releaseAt(dx-25, dy))
	if nx, _ := dividerLeftOf(t, app, "usr  09:00:00"); nx != dx-25 {
		t.Fatalf("drag left the divider at %d, want %d", nx, dx-25)
	}
	assertAllWords(t, app, "drag")
}

func assertAllWords(t *testing.T, app *App, after string) {
	t.Helper()
	re := regexp.MustCompile(`w\d+`)
	var seen []string
	for _, line := range strings.Split(ansi.Strip(app.View()), "\n") {
		if i := strings.IndexAny(line, "│┃"); i >= 0 {
			seen = append(seen, re.FindAllString(line[i:], -1)...)
		}
	}
	if len(seen) == 0 || seen[len(seen)-1] != "w059" {
		t.Fatalf("preview after %s shows %v, want every word through w059", after, seen)
	}
	for i := 1; i < len(seen); i++ {
		if seen[i] <= seen[i-1] || len(seen[i]) != 4 {
			t.Fatalf("preview lost or cut a word after %s: %v", after, seen)
		}
	}
}
