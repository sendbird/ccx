package tmux

import (
	"os"
	"testing"
)

// The tmux queries that mean "the window ccx runs in" must resolve against our
// own pane. Without that, tmux answers about whichever window the attached
// client is currently looking at, so `ccx sessions` run from a background pane
// reports the sessions of an unrelated window.
//
// CurrentWindowClaudes resolves this via the PPID walk (see walk_test.go);
// CurrentWindowKey must agree with it rather than falling back to the
// client-active window whenever our pane is resolvable.
func TestCurrentWindowKeyPrefersOwnPane(t *testing.T) {
	if !InTmux() {
		t.Skip("not running inside tmux")
	}
	panes, err := ListPanes()
	if err != nil || len(panes) == 0 {
		t.Skip("cannot list tmux panes")
	}
	own := findPaneByPID(panes, ownPanePID(panes))
	if own == nil {
		t.Skip("own pane not resolvable (wrapped beyond the visible process tree)")
	}
	want := own.Session + "|" + own.Window
	if got := CurrentWindowKey(); got != want {
		t.Fatalf("CurrentWindowKey() = %q, want %q (our own pane's window, not the client's active one)", got, want)
	}
}

func TestCurrentWindowKeyEmptyOutsideTmux(t *testing.T) {
	t.Setenv("TMUX", "")
	if got := CurrentWindowKey(); got != "" {
		t.Fatalf("CurrentWindowKey() = %q outside tmux, want empty", got)
	}
}

// findPaneByPID underpins every "which window am I in" answer; a zero PID
// (PPID walk found no pane) must not match a pane that happens to record 0.
func TestFindPaneByPIDRejectsZero(t *testing.T) {
	panes := []Pane{{PID: 0, Session: "s", Window: "1"}, {PID: 42, Session: "s", Window: "2"}}
	if got := findPaneByPID(panes, 0); got != nil {
		t.Fatalf("findPaneByPID(_, 0) = %+v, want nil", got)
	}
	if got := findPaneByPID(panes, 42); got == nil || got.Window != "2" {
		t.Fatalf("findPaneByPID(_, 42) = %+v, want the window-2 pane", got)
	}
}

// ownPanePID must not report a pane when this process isn't under any of them.
func TestOwnPanePIDNoMatch(t *testing.T) {
	if got := ownPanePID([]Pane{{PID: 999999}}); got != 0 {
		t.Fatalf("ownPanePID with an unrelated pane = %d, want 0", got)
	}
	_ = os.Getpid()
}
