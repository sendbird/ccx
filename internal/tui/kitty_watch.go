package tui

import (
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sendbird/ccx/internal/kitty"
)

// kittyVisibilityMsg reports that our tmux pane's visibility changed.
type kittyVisibilityMsg struct{ visible bool }

// kittyWatchInterval is how often the watcher polls tmux. tmux does not
// forward focus-out events on window switch, so we have to poll; 250ms feels
// responsive without thrashing the tmux control channel.
const kittyWatchInterval = 250 * time.Millisecond

// kittyWatchCmd polls probe off the UI loop and returns only when the
// visibility differs from last. The previous design delivered a tick message
// to Update every 250ms and ran `tmux display-message` synchronously there,
// which blocked input handling and forced a full View() four times a second
// even when nothing changed. Here the steady state produces no messages.
func kittyWatchCmd(last bool, probe func() bool, every time.Duration) tea.Cmd {
	return func() tea.Msg {
		for {
			time.Sleep(every)
			if v := probe(); v != last {
				return kittyVisibilityMsg{visible: v}
			}
		}
	}
}

// startKittyWatch arms the watcher against the real tmux probe.
func startKittyWatch(last bool) tea.Cmd {
	return kittyWatchCmd(last, kitty.PaneVisible, kittyWatchInterval)
}

// handleKittyVisibility applies a visibility transition and re-arms the
// watcher with the newly observed value.
func (a *App) handleKittyVisibility(msg kittyVisibilityMsg) tea.Cmd {
	if !msg.visible && a.termFocused {
		a.termFocused = false
		// tmux stops forwarding our pane's passthrough output once the
		// window switches away, so clear immediately instead of waiting
		// for the next render.
		if kitty.Supported() {
			fmt.Fprint(os.Stdout, kitty.ClearImages())
			_ = os.Stdout.Sync()
		}
	} else if msg.visible && !a.termFocused {
		a.termFocused = true
		kitty.InvalidatePaneOffset()
	}
	return startKittyWatch(msg.visible)
}
