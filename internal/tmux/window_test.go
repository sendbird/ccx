package tmux

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	tk "github.com/keyolk/tuikit/tmux"
)

// TestFindPaneByPID covers the pane lookup CurrentWindowClaudes uses to pin
// "our" window. A zero panePID (process not attributable to any pane) must
// return nil rather than mismatching on PID 0, and an explicit hit returns
// that pane.
func TestFindPaneByPID(t *testing.T) {
	panes := []Pane{
		{PID: 100, Session: "s", Window: "2"},
		{PID: 200, Session: "s", Window: "9"},
	}
	if got := findPaneByPID(panes, 0); got != nil {
		t.Errorf("findPaneByPID(0) = %+v, want nil", got)
	}
	if got := findPaneByPID(panes, 999); got != nil {
		t.Errorf("findPaneByPID(missing) = %+v, want nil", got)
	}
	got := findPaneByPID(panes, 200)
	if got == nil || got.Window != "9" {
		t.Errorf("findPaneByPID(200) = %+v, want Window=9", got)
	}
}

// TestClaudesInWindowFiltersByWindow guards the fix for the bug where
// CurrentWindowClaudes scanned the tmux client's active window instead of the
// window this process lives in: only the named session|window counts, and
// only panes with a claude under them.
func TestClaudesInWindowFiltersByWindow(t *testing.T) {
	withFake(t, tk.NewFake(), "201 200 claude\n401 400 claude\n301 300 vim\n")
	panes := []Pane{
		{PID: 100, Session: "s", Window: "2", Path: "/a"},
		{PID: 200, Session: "s", Window: "9", Path: "/b"},
		{PID: 300, Session: "s", Window: "9", Path: "/c"},
		{PID: 400, Session: "other", Window: "9", Path: "/d"},
	}
	if got := claudesInWindow(panes, "s", "9"); len(got) != 1 || got[0] != "/b" {
		t.Errorf("claudesInWindow(s,9) = %v, want [/b]", got)
	}
	if got := claudesInWindow(nil, "s", "9"); len(got) != 0 {
		t.Errorf("claudesInWindow(nil) = %v, want empty", got)
	}
}

// ListPanes reads fields by length (tuikit/tmux), so a cwd holding the old
// "|" separator or Hangul no longer shifts the columns after it.
func TestListPanesParsesAwkwardCwd(t *testing.T) {
	f := tk.NewFake().On("list-panes", paneRow("%3", "@2", 2, 1, 42, "main", "작업", "/tmp/a|b/한글", "fish"), nil)
	withFake(t, f, "")
	panes, err := ListPanes()
	if err != nil || len(panes) != 1 {
		t.Fatalf("ListPanes = %v, %v", panes, err)
	}
	p := panes[0]
	if p.PaneID != "%3" || p.WindowID != "@2" || p.Window != "2" || p.Pane != "1" || p.PID != 42 ||
		p.Path != "/tmp/a|b/한글" || p.WindowName != "작업" || p.Command != "fish" || p.Session != "main" {
		t.Fatalf("pane = %+v", p)
	}
}

// A Hangul syllable typed into the live preview is sent literally. The old
// string mapper only treated one-byte keys as text and dropped it.
func TestSendKeyMsgTypesHangulLiterally(t *testing.T) {
	f := tk.NewFake().On("send-keys", "", nil)
	withFake(t, f, "")
	if err := SendKeyMsg(Pane{PaneID: "%5"}, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("한")}); err != nil {
		t.Fatal(err)
	}
	if err := SendKeyMsg(Pane{PaneID: "%5"}, tea.KeyMsg{Type: tea.KeyCtrlC}); err != nil {
		t.Fatal(err)
	}
	got := f.CallLines()
	if len(got) != 2 || !strings.Contains(got[0], "-l") || !strings.Contains(got[0], "한") || !strings.Contains(got[1], "C-c") {
		t.Fatalf("send-keys calls = %q", got)
	}
}

// Every call targets the pane ID, never session:window.pane.
func TestSwitchToPaneTargetsIDs(t *testing.T) {
	f := tk.NewFake()
	f.Func = func(_ context.Context, args []string) ([]byte, error) {
		if args[0] == "display-message" {
			if strings.Contains(args[len(args)-1], "pane_id") {
				return []byte("$1\t@1\t%1"), nil
			}
			return []byte("$1"), nil // same session: no switch-client
		}
		return nil, nil
	}
	withFake(t, f, "")
	if err := SwitchToPane(Pane{PaneID: "%9", Session: "s", Window: "1", Pane: "0"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.CallLines() {
		if strings.Contains(c, "s:1") || strings.Contains(c, "switch-client") {
			t.Fatalf("index target or needless switch-client: %q", f.CallLines())
		}
	}
	if last := f.CallLines()[len(f.CallLines())-1]; !strings.Contains(last, "select-pane") || !strings.Contains(last, "%9") {
		t.Fatalf("calls = %q, want select-pane on %%9 last", f.CallLines())
	}
	if err := SwitchToPane(Pane{Session: "s", Window: "1", Pane: "0"}); err == nil {
		t.Fatal("a pane without an ID must not reach tmux")
	}
}
