package tui

import (
	"strings"
	"testing"
)

func TestRenderSessPageHintBoxShowsContexts(t *testing.T) {
	app := newTestApp(fakeSessions())
	hint := stripANSI(app.renderSessPageHintBox())
	for _, expected := range []string{"v:conv", "s:stats", "m:mem", "t:tasks", "a:agents", "l:live", "c:contexts"} {
		if !strings.Contains(hint, expected) {
			t.Fatalf("session page hint missing %q in %q", expected, hint)
		}
	}
}
