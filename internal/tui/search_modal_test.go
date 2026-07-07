package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestSearchModalInputFits guards the regression where the inner input box was
// one border-width too wide for the modal, wrapping onto the next line and
// shattering the box layout. The input box's rounded border must open (╭) and
// close (╮) on the SAME line — when it wrapped, the ╮ fell to the next row.
func TestSearchModalInputFits(t *testing.T) {
	for _, w := range []int{80, 120, 200} {
		app := newTestApp(fakeSessions())
		app.width, app.height = w, 50
		app.enterSearchMode()
		out := app.View()

		// The input box is the FIRST inner rounded box after the title. Find a
		// line that contains "╭" nested inside the modal (leading "│ ╭") and
		// require it to also contain the closing "╮".
		foundInputTop := false
		for _, ln := range strings.Split(out, "\n") {
			p := ansi.Strip(ln)
			if strings.Contains(p, "│ ╭") {
				foundInputTop = true
				if !strings.Contains(p, "╮") {
					t.Errorf("w=%d: input box top border opened (╭) but did not close (╮) on the same line — it wrapped:\n%s", w, strings.TrimRight(p, " "))
				}
				break
			}
		}
		if !foundInputTop {
			t.Errorf("w=%d: could not find the nested input box top border", w)
		}
	}
}
