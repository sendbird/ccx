package tui

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	tk "github.com/keyolk/tuikit/kitty"

	"github.com/sendbird/ccx/internal/kitty"
)

// kittyPaneMsg carries a tmux pane query result. watch marks the visibility
// watcher's own answer, which is re-armed; one-off queries (resize, focus,
// blur) only update the recorded pane, so they never start a second watcher.
type kittyPaneMsg struct {
	tk.PaneMsg
	watch bool
}

// kittyWatchCmd arms tuikit's pane watcher, which polls off the UI loop and
// returns only when the pane moved or was hidden or shown; an idle app gets
// no messages (ccx#175, now kitty.WatchPane).
func kittyWatchCmd(last tk.PaneMsg) tea.Cmd {
	return wrapPaneCmd(kitty.WatchCmd(last), true)
}

// kittyPaneQueryCmd asks tmux about our pane once, off the UI loop.
func kittyPaneQueryCmd() tea.Cmd {
	return wrapPaneCmd(kitty.PaneCmd(), false)
}

func wrapPaneCmd(cmd tea.Cmd, watch bool) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		if m, ok := cmd().(tk.PaneMsg); ok {
			return kittyPaneMsg{PaneMsg: m, watch: watch}
		}
		return nil
	}
}

// handleKittyPane records the pane geometry for image placement and applies
// a visibility transition; the watcher is re-armed with its own answer.
func (a *App) handleKittyPane(msg kittyPaneMsg) tea.Cmd {
	kitty.SetPane(msg.Pane)
	if !msg.Pane.Visible && a.termFocused {
		a.termFocused = false
		// tmux stops forwarding our pane's passthrough output once the
		// window switches away, so clear immediately instead of waiting
		// for the next render.
		if s := kitty.ClearImages(); s != "" {
			fmt.Fprint(os.Stdout, s)
			_ = os.Stdout.Sync()
		}
	} else if msg.Pane.Visible && !a.termFocused && msg.watch {
		a.termFocused = true
	}
	if msg.watch {
		return kittyWatchCmd(msg.PaneMsg)
	}
	return nil
}
