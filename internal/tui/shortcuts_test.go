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
	if sess.Left["0"] != "preview:live" {
		t.Errorf("sessions left 0 = %q, want preview:live", sess.Left["0"])
	}
	if sess.Left["1"] != "preview:conv" {
		t.Errorf("sessions left 1 = %q, want preview:conv", sess.Left["1"])
	}
	if sess.Left["2"] != "preview:contexts" {
		t.Errorf("sessions left 2 = %q, want preview:contexts", sess.Left["2"])
	}
	if sess.Left["3"] != "preview:agents" {
		t.Errorf("sessions left 3 = %q, want preview:agents", sess.Left["3"])
	}

	// Conversation view
	conv, ok := sc["conversation"]
	if !ok {
		t.Fatal("expected conversation view shortcuts")
	}
	if len(conv.Left) != 0 {
		t.Errorf("conversation left shortcuts = %v, want none for unified flow", conv.Left)
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
	if len(dst["conversation"].Left) != 0 {
		t.Errorf("conversation left shortcuts = %v, want none", dst["conversation"].Left)
	}
}

// TestMigrateShortcutsFromOldDefault verifies a persisted config still on the
// pre-0-key default layout is migrated wholesale to the flow-ordered layout
// (0=live), while a user-customized layout is left untouched.
func TestMigrateShortcutsFromOldDefault(t *testing.T) {
	// Old default: 1conv 2stats 3mem 4tasks 5agents 6live 7contexts 8refs.
	old := Shortcuts{"sessions": ViewShortcuts{Left: ShortcutMap{
		"1": "preview:conv", "2": "preview:stats", "3": "preview:mem",
		"4": "preview:tasks", "5": "preview:agents", "6": "preview:live",
		"7": "preview:contexts", "8": "preview:refs",
	}}}
	migrateShortcuts(old)
	got := old["sessions"].Left
	if got["0"] != "preview:live" {
		t.Errorf("migrated 0 = %q, want preview:live", got["0"])
	}
	if got["1"] != "preview:conv" || got["2"] != "preview:contexts" || got["3"] != "preview:agents" {
		t.Errorf("migration did not apply flow order: %v", got)
	}

	// A customized layout (already has 0, or 1 is not preview:conv) is untouched.
	custom := Shortcuts{"sessions": ViewShortcuts{Left: ShortcutMap{
		"1": "preview:refs", "2": "preview:stats",
	}}}
	migrateShortcuts(custom)
	if custom["sessions"].Left["1"] != "preview:refs" {
		t.Errorf("customized layout was clobbered: %v", custom["sessions"].Left)
	}
	if _, hasZero := custom["sessions"].Left["0"]; hasZero {
		t.Errorf("customized layout should not gain a 0 key: %v", custom["sessions"].Left)
	}
}

// TestMergeShortcutsMigratesStaleUserConfig is the real-world path: the user's
// persisted pre-0 layout is merged onto the new default (which already has a 0
// key). Without migrating off the USER src, the merge produces a frankenstein
// with duplicate mappings (0 and 6 both live). This asserts the merged result
// is the clean flow layout with no duplicates.
func TestMergeShortcutsMigratesStaleUserConfig(t *testing.T) {
	dst := DefaultShortcuts()
	user := Shortcuts{"sessions": ViewShortcuts{Left: ShortcutMap{
		"1": "preview:conv", "2": "preview:stats", "3": "preview:mem",
		"4": "preview:tasks", "5": "preview:agents", "6": "preview:live",
	}}}
	mergeShortcuts(dst, user)
	sl := dst["sessions"].Left

	// No command appears on more than one key.
	seen := map[string]string{}
	for k, v := range sl {
		if prev, dup := seen[v]; dup {
			t.Errorf("duplicate mapping: %q on both %q and %q", v, prev, k)
		}
		seen[v] = k
	}
	// And it is the flow layout.
	if sl["0"] != "preview:live" || sl["1"] != "preview:conv" || sl["6"] != "preview:mem" {
		t.Errorf("merged layout is not flow order: %v", sl)
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
	// Should contain "1:conv" and the new 0:live quick-key
	if !containsSubstring(hint, "0:live") {
		t.Errorf("hint %q should contain 0:live", hint)
	}
	if !containsSubstring(hint, "1:conv") {
		t.Errorf("hint %q should contain 1:conv", hint)
	}
	if !containsSubstring(hint, "3:agents") {
		t.Errorf("hint %q should contain 3:agents", hint)
	}
	if !containsSubstring(hint, "2:contexts") {
		t.Errorf("hint %q should contain 2:contexts", hint)
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
