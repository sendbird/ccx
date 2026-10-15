package kitty

import (
	"os"
	"strings"
	"testing"

	tk "github.com/keyolk/tuikit/kitty"
)

// fakeTmux answers pane queries from a map and records every call.
type fakeTmux struct {
	replies map[string]string
	calls   []string
}

func (f *fakeTmux) Run(args ...string) (string, error) {
	key := strings.Join(args, " ")
	f.calls = append(f.calls, key)
	return f.replies[key], nil
}

func withCaps(t *testing.T, c tk.Caps, q tk.TmuxQuerier) {
	t.Helper()
	SetCaps(c, q)
	t.Cleanup(func() { SetCaps(tk.Caps{}, nil) })
}

func TestUnsupportedEmitsNothing(t *testing.T) {
	withCaps(t, tk.Caps{}, nil)
	if Supported() || ClearImages() != "" || PlaceImage("/x.png", 1, 1, 4, 4) != "" {
		t.Fatal("unsupported terminal got a sequence")
	}
	if PaneCmd() != nil || WatchCmd(tk.PaneMsg{}) != nil || EnsurePassthroughCmd() != nil {
		t.Fatal("unsupported terminal got a tmux command")
	}
}

func TestDisplayImageMissingFile(t *testing.T) {
	withCaps(t, tk.Caps{Supported: true}, nil)
	if got := DisplayImage("/nonexistent/path.png", 40, 20); got != "" {
		t.Fatalf("expected empty for missing file, got %q", got)
	}
}

func TestPlaceImageTransmitsAndPlaces(t *testing.T) {
	withCaps(t, tk.Caps{Supported: true}, nil)
	f, err := os.CreateTemp(t.TempDir(), "kitty-test-*.png")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	got := PlaceImage(f.Name(), 3, 2, 40, 20)
	for _, want := range []string{"a=t,t=f", "a=p,i=1", "c=40,r=20", "\x1b[3;2H"} {
		if !strings.Contains(got, want) {
			t.Fatalf("PlaceImage missing %q: %q", want, got)
		}
	}
	if PlaceImage("", 1, 1, 40, 20) != "" {
		t.Fatal("expected empty for empty path")
	}
}

// Inside tmux the move is offset by the pane position from the last query
// and the whole thing is wrapped in passthrough.
func TestPlaceImageInTmuxUsesRecordedPane(t *testing.T) {
	withCaps(t, tk.Caps{Supported: true, InTmux: true, NeedsPassthrough: true, Pane: "%3"}, nil)
	SetPane(tk.Pane{Visible: true, Top: 10, Left: 5})
	f, err := os.CreateTemp(t.TempDir(), "kitty-test-*.png")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	got := PlaceImage(f.Name(), 1, 1, 4, 4)
	if !strings.HasPrefix(got, "\x1bPtmux;") || !strings.Contains(got, "[11;6H") {
		t.Fatalf("tmux placement = %q", got)
	}
}

func TestClearImages(t *testing.T) {
	withCaps(t, tk.Caps{Supported: true}, nil)
	if got := ClearImages(); !strings.Contains(got, "a=d") {
		t.Fatalf("expected a delete, got %q", got)
	}
}

// The pane query targets our pane by id through the injected querier.
func TestPaneCmdQueriesOwnPane(t *testing.T) {
	f := &fakeTmux{replies: map[string]string{}}
	withCaps(t, tk.Caps{Supported: true, InTmux: true, Pane: "%7"}, f)
	msg, ok := PaneCmd()().(tk.PaneMsg)
	if !ok {
		t.Fatal("PaneCmd did not report a PaneMsg")
	}
	_ = msg
	if len(f.calls) == 0 || !strings.Contains(f.calls[0], "-t %7") {
		t.Fatalf("calls = %v, want a query targeting %%7", f.calls)
	}
}

func TestFitSizeKeepsAspect(t *testing.T) {
	cols, rows := FitSize(200, 100, 40, 40)
	if cols != 40 || rows != 10 {
		t.Fatalf("FitSize(200x100) = %dx%d, want 40x10", cols, rows)
	}
	if c, r := FitSize(0, 0, 30, 12); c != 30 || r != 12 {
		t.Fatalf("unknown size = %dx%d, want the whole box", c, r)
	}
}
