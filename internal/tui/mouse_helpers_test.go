package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
)

// testClock is a hand-advanced clock for double-click timing.
type testClock struct{ t time.Time }

func fixedClock(t time.Time) *testClock      { return &testClock{t: t} }
func (c *testClock) now() time.Time          { return c.t }
func (c *testClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func press(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
}

func wheel(x, y int, down bool) tea.MouseMsg {
	b := tea.MouseButtonWheelUp
	if down {
		b = tea.MouseButtonWheelDown
	}
	return tea.MouseMsg{X: x, Y: y, Button: b, Action: tea.MouseActionPress}
}

// find locates text on the rendered screen, so tests click where the user
// would see it instead of recomputing the layout with the code's own math.
func find(t *testing.T, app *App, text string) (x, y int) {
	t.Helper()
	cells := screenCells(app.View())
	for y, row := range cells {
		line := strings.Join(row, "")
		if i := strings.Index(line, text); i >= 0 {
			return runewidth.StringWidth(line[:i]), y
		}
	}
	t.Fatalf("%q not on screen:\n%s", text, stripANSI(app.View()))
	return 0, 0
}

func send(app *App, msg tea.Msg) *App {
	m, _ := app.Update(msg)
	return m.(*App)
}
