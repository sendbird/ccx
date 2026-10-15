package tui

import (
	"testing"

	tk "github.com/keyolk/tuikit/kitty"

	"github.com/sendbird/ccx/internal/kitty"
)

func TestKittyPaneMsgTogglesFocusAndRearmsOnlyTheWatcher(t *testing.T) {
	// Supported outside tmux: the watcher probe answers without running tmux.
	kitty.SetCaps(tk.Caps{Supported: true}, nil)
	t.Cleanup(func() { kitty.SetCaps(tk.Caps{}, nil) })
	a := newTestApp(fakeSessions())
	a.termFocused = true

	hidden := tk.PaneMsg{Pane: tk.Pane{Visible: false, Top: 2, Left: 3}}
	if cmd := a.handleKittyPane(kittyPaneMsg{PaneMsg: hidden, watch: true}); cmd == nil {
		t.Fatal("watcher must be re-armed")
	}
	if a.termFocused {
		t.Fatal("hidden pane should clear termFocused")
	}
	if p := kitty.Pane(); p.Top != 2 || p.Left != 3 {
		t.Fatalf("pane geometry not recorded: %+v", p)
	}

	shown := tk.PaneMsg{Pane: tk.Pane{Visible: true}}
	if cmd := a.handleKittyPane(kittyPaneMsg{PaneMsg: shown}); cmd != nil {
		t.Fatal("a one-off query must not start a second watcher")
	}
	if a.termFocused {
		t.Fatal("a one-off query should not restore focus; FocusMsg does")
	}
	a.handleKittyPane(kittyPaneMsg{PaneMsg: shown, watch: true})
	if !a.termFocused {
		t.Fatal("visible pane from the watcher should restore termFocused")
	}
}

// Without Init (tests, unsupported terminals) nothing polls tmux.
func TestKittyCommandsAreNilWhenUnsupported(t *testing.T) {
	if kittyWatchCmd(tk.PaneMsg{}) != nil || kittyPaneQueryCmd() != nil {
		t.Fatal("unsupported terminal armed a tmux query")
	}
}
