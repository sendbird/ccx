package tui

import (
	"testing"
	"unicode/utf8"

	"github.com/keyolk/tuikit/render"
)

// truncName sliced bytes: a Hangul name came back as invalid UTF-8, and a
// budget under 1 panicked (name[:-1]).
func TestTruncNameIsCellAware(t *testing.T) {
	got := truncName("세션이름입니다", 5)
	if !utf8.ValidString(got) || render.Width(got) > 5 {
		t.Fatalf("truncName = %q (width %d)", got, render.Width(got))
	}
	if got := truncName("abc", 0); got != "" {
		t.Fatalf("truncName(abc, 0) = %q", got)
	}
}

// overlayCenter keeps the frame size even when a wide character sits on the
// modal's edge.
func TestOverlayCenterKeepsWidthOnWideSeam(t *testing.T) {
	bg := "가나다라마바사아자차\n가나다라마바사아자차\n가나다라마바사아자차"
	out := overlayCenter(bg, "[x]", 20, 3)
	for i, l := range splitLines(out) {
		if w := render.Width(l); w != 20 {
			t.Fatalf("line %d width %d: %q", i, w, l)
		}
	}
}
