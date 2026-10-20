package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
)

// manySessions builds n sessions backed by a one-line transcript each, so
// Enter (opening a conversation) has a real file to load.
func manySessions(t *testing.T, n int) []session.Session {
	t.Helper()
	dir := t.TempDir()
	line := `{"type":"user","uuid":"u1","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":"hi"}}` + "\n"
	var out []session.Session
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("s%02d", i)
		path := filepath.Join(dir, id+".jsonl")
		if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
		out = append(out, session.Session{
			ID: id, ShortID: id, FilePath: path, ProjectPath: "/tmp/p" + id, ProjectName: "proj-" + id,
			ModTime: time.Now().Add(-time.Duration(i) * time.Minute), MsgCount: 3,
		})
	}
	return out
}

func newMouseApp(t *testing.T, n int) (*App, *testClock) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	app := newTestApp(manySessions(t, n))
	clock := fixedClock(time.Now())
	app.clicks.now = clock.now
	return app, clock
}

func selectedID(t *testing.T, app *App) string {
	t.Helper()
	s, ok := app.selectedSession()
	if !ok {
		t.Fatal("no selected session")
	}
	return s.ID
}

func TestClickSelectsRowWithoutOpening(t *testing.T) {
	app, _ := newMouseApp(t, 6)
	x, y := find(t, app, "proj-s03")
	app = send(app, press(x, y))
	if got := selectedID(t, app); got != "s03" {
		t.Fatalf("selected %q, want s03", got)
	}
	if app.state != viewSessions {
		t.Fatalf("single click left view %d; a click must only select", app.state)
	}
}

func TestSecondClickOnSelectedRowIsEnter(t *testing.T) {
	app, clock := newMouseApp(t, 6)
	x, y := find(t, app, "proj-s03")
	app = send(app, press(x, y))
	clock.advance(100 * time.Millisecond)
	app = send(app, press(x, y))
	if app.state != viewConversation {
		t.Fatalf("second click did not run Enter: state=%d", app.state)
	}
	if got := app.currentSess.ID; got != "s03" {
		t.Fatalf("Enter opened %q, want s03", got)
	}
}

func TestSecondClickOnDifferentRowOnlySelects(t *testing.T) {
	app, clock := newMouseApp(t, 6)
	x, y := find(t, app, "proj-s02")
	app = send(app, press(x, y))
	clock.advance(50 * time.Millisecond)
	x, y = find(t, app, "proj-s04")
	app = send(app, press(x, y))
	if app.state != viewSessions || selectedID(t, app) != "s04" {
		t.Fatalf("state=%d selected=%s; two rows in a row must not activate", app.state, selectedID(t, app))
	}
}

func TestSlowSecondClickDoesNotOpen(t *testing.T) {
	app, clock := newMouseApp(t, 6)
	x, y := find(t, app, "proj-s03")
	app = send(app, press(x, y))
	clock.advance(time.Second)
	app = send(app, press(x, y))
	if app.state != viewSessions {
		t.Fatalf("a click after the double-click window opened the session")
	}
}

func TestEmptyAreaClicksDoNothing(t *testing.T) {
	app, clock := newMouseApp(t, 3)
	before := selectedID(t, app)
	// Row 40 is far below the three short rows.
	for range 2 {
		app = send(app, press(5, 40))
		clock.advance(50 * time.Millisecond)
	}
	if app.state != viewSessions || selectedID(t, app) != before {
		t.Fatalf("empty-area double click changed state=%d selected=%s", app.state, selectedID(t, app))
	}
}

func TestWheelMovesSelectionByThree(t *testing.T) {
	app, _ := newMouseApp(t, 12)
	x, y := find(t, app, "proj-s00")
	app = send(app, wheel(x, y, true))
	if got := selectedID(t, app); got != "s03" {
		t.Fatalf("one wheel notch down selected %q, want s03", got)
	}
	app = send(app, wheel(x, y, false))
	if got := selectedID(t, app); got != "s00" {
		t.Fatalf("one wheel notch up selected %q, want s00", got)
	}
}

func TestReleaseMotionAndOtherButtonsAreIgnored(t *testing.T) {
	app, _ := newMouseApp(t, 6)
	x, y := find(t, app, "proj-s03")
	for _, msg := range []tea.MouseMsg{
		{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease},
		{X: x, Y: y, Button: tea.MouseButtonNone, Action: tea.MouseActionMotion},
		{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion},
		{X: x, Y: y, Button: tea.MouseButtonRight, Action: tea.MouseActionPress},
		{X: x, Y: y, Button: tea.MouseButtonMiddle, Action: tea.MouseActionPress},
	} {
		app = send(app, msg)
		if got := selectedID(t, app); got != "s00" {
			t.Fatalf("%v selected %q; only a left press may select", msg, got)
		}
	}
}

func TestClickHitsRowWithPreviewOpen(t *testing.T) {
	for _, width := range []int{160, 70} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			app, _ := newMouseApp(t, 8)
			app = send(app, tea.WindowSizeMsg{Width: width, Height: 30})
			app.sessSplit.Show = true
			app = send(app, tea.WindowSizeMsg{Width: width, Height: 30})
			x, y := find(t, app, "s05  ")
			app = send(app, press(x, y))
			if got := selectedID(t, app); got != "s05" {
				t.Fatalf("width %d: selected %q, want s05", width, got)
			}
		})
	}
}

// A list longer than one page paginates; clicks map through the page start.
func TestClickOnSecondPageSelectsThatRow(t *testing.T) {
	app, _ := newMouseApp(t, 60)
	perPage := app.sessionList.Paginator.PerPage
	if perPage >= 60 {
		t.Fatalf("test needs a paginated list, per page = %d", perPage)
	}
	for range perPage + 2 {
		app = send(app, tea.KeyMsg{Type: tea.KeyDown})
	}
	if app.sessionList.Paginator.Page == 0 {
		t.Fatal("did not reach the second page")
	}
	want := fmt.Sprintf("s%02d", perPage+4)
	x, y := find(t, app, "proj-"+want)
	app = send(app, press(x, y))
	if got := selectedID(t, app); got != want {
		t.Fatalf("click on %s selected %q", want, got)
	}
}

func TestKeyBetweenClicksBreaksTheDoubleClick(t *testing.T) {
	app, clock := newMouseApp(t, 6)
	x, y := find(t, app, "proj-s03")
	app = send(app, press(x, y))
	clock.advance(50 * time.Millisecond)
	app = send(app, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	app = send(app, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	clock.advance(50 * time.Millisecond)
	app = send(app, press(x, y))
	if app.state != viewSessions || selectedID(t, app) != "s03" {
		t.Fatalf("click, j, k, click: state=%d selected=%s; a key between clicks must break the pair", app.state, selectedID(t, app))
	}
}
