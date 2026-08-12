package tui

import (
	"os"
	"testing"
)

// TestMain isolates the whole package from the developer's real
// ~/.config/ccx.
//
// NewApp restores persisted preferences (keymap, group mode, preview mode,
// startup filter) from config.yaml, so tests that drive key input or assert on
// visible items behaved differently on a machine with a customized config than
// they did in CI with a clean HOME. Concretely: a local `keymaps.session.select: ' '`
// / `open: enter` remapping made TestRefsPreviewEnterOpensURL fail locally
// while passing in CI, because Enter no longer reached the refs handler.
//
// NewApp also reads remote-sessions.yaml and injects saved remote sessions at
// the *head* of the session list, so a test that builds an App and then
// Select(0) landed on the developer's leftover remote session instead of its
// own fixture. Worse than the wrong result: cleanupStaleRemoteSessions shells
// out to kubectl/ssh once per saved session, which is what made this package
// take ~180s locally versus ~15s with a clean HOME.
//
// newTestApp had been papering over individual symptoms of this (clearing the
// startup filter, the refs preview mode, the group mode). Pointing HOME at a
// temp dir fixes the class of problem at the source: every test in this package
// now sees default preferences regardless of the developer's setup.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "ccx-tui-test-home-")
	if err != nil {
		// Without isolation the suite is non-hermetic; fail loudly rather than
		// silently reading the developer's config.
		panic("tui tests: cannot create temp HOME: " + err.Error())
	}
	// Set both HOME and the XDG base dir: os.UserHomeDir uses HOME on unix,
	// and any XDG-aware lookup should follow the temp dir too.
	os.Setenv("HOME", home)
	os.Setenv("XDG_CONFIG_HOME", home+"/.config")

	code := m.Run()

	os.RemoveAll(home)
	os.Exit(code)
}
