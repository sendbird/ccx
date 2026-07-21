package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
)

// TestHelpOverlayOpensAndClosesGlobally verifies the "?" help overlay opens
// from a non-sessions view (conversation) and any key closes it — it is no
// longer sessions-only.
func TestHelpOverlayOpensAndClosesGlobally(t *testing.T) {
	app := newConfiguredTestApp([]session.Session{{ID: "s1", ShortID: "s1", ProjectName: "p"}}, Config{})
	app.state = viewConversation

	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	app = m.(*App)
	if !app.showHelp {
		t.Fatal("? did not open the help overlay in the conversation view")
	}

	// Overlay renders a context section for the conversation view.
	overlay := stripANSI(app.renderHelpModal("", 120, 80))
	if !strings.Contains(overlay, "Conversation") || !strings.Contains(overlay, "Common keys") {
		t.Fatalf("conversation help overlay missing expected sections: %q", overlay)
	}

	// Any key closes it.
	m, _ = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	app = m.(*App)
	if app.showHelp {
		t.Fatal("help overlay did not close on keypress")
	}
}

// TestFootersEndWithHelpAffordance verifies each view's concise footer advertises
// the ?:help key.
func TestFootersEndWithHelpAffordance(t *testing.T) {
	app := newConfiguredTestApp([]session.Session{{ID: "s1", ShortID: "s1", ProjectName: "p"}}, Config{})

	if got := stripANSI(app.sessHelpLine()); !strings.Contains(got, "?:help") {
		t.Errorf("sessions footer missing ?:help: %q", got)
	}
	if got := stripANSI(app.convHelpLine("")); !strings.Contains(got, "?:help") {
		t.Errorf("conversation footer missing ?:help: %q", got)
	}
	if got := stripANSI(app.configHelpLine()); !strings.Contains(got, "?:help") {
		t.Errorf("config footer missing ?:help: %q", got)
	}
	if got := stripANSI(app.pluginsHelpLine()); !strings.Contains(got, "?:help") {
		t.Errorf("plugins footer missing ?:help: %q", got)
	}
}

// TestTruncateFooterFitsWidth verifies the footer never exceeds the terminal
// width (the clipping bug that hid tmux hints).
func TestTruncateFooterFitsWidth(t *testing.T) {
	app := newConfiguredTestApp(nil, Config{})
	app.width = 40
	long := ""
	for i := 0; i < 30; i++ {
		long += "key:action "
	}
	out := app.truncateFooter(formatHelp(long))
	if w := lipgloss.Width(out); w > 40 {
		t.Fatalf("truncated footer width = %d, want <= 40", w)
	}
}
