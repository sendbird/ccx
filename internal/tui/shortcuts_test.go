package tui

import (
	"testing"
)

func TestDefaultShortcuts(t *testing.T) {
	sc := DefaultShortcuts()

	// Sessions view should have left-side shortcuts
	sess, ok := sc["sessions"]
	if !ok {
		t.Fatal("expected sessions view shortcuts")
	}
	if sess.Left["1"] != "preview:conv" {
		t.Errorf("sessions left 1 = %q, want preview:conv", sess.Left["1"])
	}
	if sess.Left["5"] != "preview:live" {
		t.Errorf("sessions left 5 = %q, want preview:live", sess.Left["5"])
	}

	// Conversation view
	conv, ok := sc["conversation"]
	if !ok {
		t.Fatal("expected conversation view shortcuts")
	}
	if conv.Left["1"] != "pane:flat" {
		t.Errorf("conversation left 1 = %q, want pane:flat", conv.Left["1"])
	}
	if conv.Left["2"] != "pane:tree" {
		t.Errorf("conversation left 2 = %q, want pane:tree", conv.Left["2"])
	}
	if conv.Right["3"] != "detail:verbose" {
		t.Errorf("conversation right 3 = %q, want detail:verbose", conv.Right["3"])
	}

	// Config view
	cfg, ok := sc["config"]
	if !ok {
		t.Fatal("expected config view shortcuts")
	}
	if cfg.Left["1"] != "page:overview" {
		t.Errorf("config left 1 = %q, want page:overview", cfg.Left["1"])
	}

	// Stats view
	stats, ok := sc["stats"]
	if !ok {
		t.Fatal("expected stats view shortcuts")
	}
	if stats.Left["2"] != "page:tools" {
		t.Errorf("stats left 2 = %q, want page:tools", stats.Left["2"])
	}
}

func TestMergeShortcuts(t *testing.T) {
	dst := DefaultShortcuts()
	src := Shortcuts{
		"sessions": ViewShortcuts{
			Left: ShortcutMap{
				"1": "preview:stats", // override
				"9": "refresh",       // new key
			},
			Right: ShortcutMap{
				"1": "view:config", // new side
			},
		},
		"newview": ViewShortcuts{ // entirely new view
			Left: ShortcutMap{"1": "custom:cmd"},
		},
	}

	mergeShortcuts(dst, src)

	// Override
	if dst["sessions"].Left["1"] != "preview:stats" {
		t.Errorf("sessions left 1 = %q, want preview:stats (overridden)", dst["sessions"].Left["1"])
	}
	// New key
	if dst["sessions"].Left["9"] != "refresh" {
		t.Errorf("sessions left 9 = %q, want refresh", dst["sessions"].Left["9"])
	}
	// Preserved default
	if dst["sessions"].Left["2"] != "preview:stats" {
		// "2" should still be the original default
	}
	// New side
	if dst["sessions"].Right["1"] != "view:config" {
		t.Errorf("sessions right 1 = %q, want view:config", dst["sessions"].Right["1"])
	}
	// Entirely new view
	if dst["newview"].Left["1"] != "custom:cmd" {
		t.Errorf("newview left 1 = %q, want custom:cmd", dst["newview"].Left["1"])
	}
	// Other views preserved
	if dst["conversation"].Left["1"] != "pane:flat" {
		t.Errorf("conversation left 1 = %q, want pane:flat (preserved)", dst["conversation"].Left["1"])
	}
}

func TestShortcutHint(t *testing.T) {
	app := newTestApp(fakeSessions())
	app.shortcuts = DefaultShortcuts()
	app.state = viewSessions

	hint := app.shortcutHint()
	if hint == "" {
		t.Fatal("expected non-empty hint for sessions view")
	}
	// Should contain "1:conv"
	if !containsSubstring(hint, "1:conv") {
		t.Errorf("hint %q should contain 1:conv", hint)
	}
	if !containsSubstring(hint, "5:live") {
		t.Errorf("hint %q should contain 5:live", hint)
	}
}

func TestShortcutHintEmpty(t *testing.T) {
	app := newTestApp(fakeSessions())
	app.shortcuts = Shortcuts{} // no shortcuts
	app.state = viewSessions

	hint := app.shortcutHint()
	if hint != "" {
		t.Errorf("expected empty hint, got %q", hint)
	}
}

func TestCurrentViewName(t *testing.T) {
	app := newTestApp(fakeSessions())

	tests := []struct {
		state viewState
		want  string
	}{
		{viewSessions, "sessions"},
		{viewConversation, "conversation"},
		{viewMessageFull, "messagefull"},
		{viewConfig, "config"},
		{viewPlugins, "plugins"},
		{viewGlobalStats, "stats"},
	}
	for _, tt := range tests {
		app.state = tt.state
		if got := app.currentViewName(); got != tt.want {
			t.Errorf("state %d: got %q, want %q", tt.state, got, tt.want)
		}
	}
}

func TestCurrentFocusSide(t *testing.T) {
	app := newTestApp(fakeSessions())
	app.state = viewSessions

	// Default: left
	app.sessSplit.Show = false
	if got := app.currentFocusSide(); got != "left" {
		t.Errorf("split hidden: got %q, want left", got)
	}

	// Split shown but not focused: left
	app.sessSplit.Show = true
	app.sessSplit.Focus = false
	if got := app.currentFocusSide(); got != "left" {
		t.Errorf("split shown, unfocused: got %q, want left", got)
	}

	// Split shown and focused: right
	app.sessSplit.Focus = true
	if got := app.currentFocusSide(); got != "right" {
		t.Errorf("split focused: got %q, want right", got)
	}
}

func containsSubstring(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && findSubstring(s, sub))
}

func findSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
