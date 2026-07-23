package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestDefaultKeymap(t *testing.T) {
	km := DefaultKeymap()

	// Verify all session keys are non-empty. Edit/Live/Switch are intentionally
	// empty by default (Edit moved into the actions menu as x→e; Live is reached
	// via the page menu p→l; Switch has no default top-level binding).
	checks := []struct {
		name, val string
	}{
		{"Quit", km.Session.Quit},
		{"Escape", km.Session.Escape},
		{"Open", km.Session.Open},
		{"Actions", km.Session.Actions},
		{"Views", km.Session.Views},
		{"Refresh", km.Session.Refresh},
		{"Help", km.Session.Help},
		{"Search", km.Session.Search},
		{"Select", km.Session.Select},
		{"Preview", km.Session.Preview},
		{"PreviewBack", km.Session.PreviewBack},
		{"Left", km.Session.Left},
		{"Right", km.Session.Right},
		{"ResizeShrink", km.Session.ResizeShrink},
		{"ResizeGrow", km.Session.ResizeGrow},
		{"Command", km.Session.Command},
	}
	for _, c := range checks {
		if c.val == "" {
			t.Errorf("DefaultKeymap().Session.%s is empty", c.name)
		}
	}
	if km.Session.Edit != "" || km.Session.Live != "" || km.Session.Switch != "" {
		t.Errorf("Edit/Live/Switch should be empty by default, got Edit=%q Live=%q Switch=%q", km.Session.Edit, km.Session.Live, km.Session.Switch)
	}
	if km.Actions.Edit != "e" {
		t.Errorf("Actions.Edit = %q, want e (actions-menu edit)", km.Actions.Edit)
	}
	if km.Session.Views != "V" {
		t.Errorf("Session.Views = %q, want V", km.Session.Views)
	}

	// Verify actions keys
	if km.Actions.Delete == "" || km.Actions.Move == "" || km.Actions.Resume == "" ||
		km.Actions.Worktree == "" || km.Actions.Kill == "" || km.Actions.Input == "" || km.Actions.Jump == "" {
		t.Error("DefaultKeymap() has empty Actions fields")
	}

	// Verify views and conversation keys
	if km.Views.Stats == "" || km.Views.Config == "" {
		t.Error("DefaultKeymap() has empty Views fields")
	}
	if km.Conversation.SwitchRegion != "P" {
		t.Errorf("DefaultKeymap().Conversation.SwitchRegion=%q, want P", km.Conversation.SwitchRegion)
	}
	if km.Conversation.ExecutionContexts != "A" {
		t.Errorf("DefaultKeymap().Conversation.ExecutionContexts=%q, want A", km.Conversation.ExecutionContexts)
	}
}

func TestLoadKeymapConversationSwitchRegionOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("conversation:\n  switch_region: ctrl+p\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	km, err := LoadKeymap(path)
	if err != nil {
		t.Fatal(err)
	}
	if km.Conversation.SwitchRegion != "ctrl+p" {
		t.Fatalf("Conversation.SwitchRegion=%q, want ctrl+p", km.Conversation.SwitchRegion)
	}
	if km.Conversation.JumpToTree != DefaultKeymap().Conversation.JumpToTree {
		t.Fatal("partial conversation override changed unrelated defaults")
	}
}

func TestLoadKeymap_FileNotExist(t *testing.T) {
	km, err := LoadKeymap("/nonexistent/path/config.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	def := DefaultKeymap()
	if km.Session.Quit != def.Session.Quit {
		t.Errorf("got Quit=%q, want %q", km.Session.Quit, def.Session.Quit)
	}
	if km.Actions.Delete != def.Actions.Delete {
		t.Errorf("got Actions.Delete=%q, want %q", km.Actions.Delete, def.Actions.Delete)
	}
}

func TestLoadKeymap_PartialOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `session:
  quit: "Q"
  actions: "a"
actions:
  delete: "D"
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	km, err := LoadKeymap(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Overridden keys
	if km.Session.Quit != "Q" {
		t.Errorf("Quit=%q, want %q", km.Session.Quit, "Q")
	}
	if km.Session.Actions != "a" {
		t.Errorf("Actions=%q, want %q", km.Session.Actions, "a")
	}
	if km.Actions.Delete != "D" {
		t.Errorf("Actions.Delete=%q, want %q", km.Actions.Delete, "D")
	}

	// Non-overridden keys should keep defaults
	def := DefaultKeymap()
	if km.Session.Open != def.Session.Open {
		t.Errorf("Open=%q, want default %q", km.Session.Open, def.Session.Open)
	}
	if km.Session.Refresh != def.Session.Refresh {
		t.Errorf("Refresh=%q, want default %q", km.Session.Refresh, def.Session.Refresh)
	}
	if km.Actions.Move != def.Actions.Move {
		t.Errorf("Actions.Move=%q, want default %q", km.Actions.Move, def.Actions.Move)
	}
	if km.Views.Stats != def.Views.Stats {
		t.Errorf("Views.Stats=%q, want default %q", km.Views.Stats, def.Views.Stats)
	}
}

func TestLoadKeymap_FullOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `session:
  quit: "Q"
  escape: "backspace"
  open: "o"
  edit: "E"
  actions: "a"
  views: "V"
  refresh: "r"
  group: "g"
  help: "h"
  search: "s"
  live: "l"
  select: "."
  preview: "p"
  preview_back: "P"
  left: "H"
  right: "L"
  resize_shrink: "{"
  resize_grow: "}"
actions:
  delete: "D"
  move: "M"
  resume: "R"
  worktree: "W"
  kill: "K"
  input: "I"
  jump: "J"
views:
  stats: "S"
  config: "C"
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	km, err := LoadKeymap(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if km.Session.Quit != "Q" {
		t.Errorf("Quit=%q, want Q", km.Session.Quit)
	}
	if km.Session.Escape != "backspace" {
		t.Errorf("Escape=%q, want backspace", km.Session.Escape)
	}
	if km.Session.ResizeGrow != "}" {
		t.Errorf("ResizeGrow=%q, want }", km.Session.ResizeGrow)
	}
	if km.Actions.Jump != "J" {
		t.Errorf("Actions.Jump=%q, want J", km.Actions.Jump)
	}
	if km.Views.Config != "C" {
		t.Errorf("Views.Config=%q, want C", km.Views.Config)
	}
}

func TestDisplayKey(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{" ", "sp"},
		{"enter", "↵"},
		{"esc", "esc"},
		{"tab", "tab"},
		{"shift+tab", "S-tab"},
		{"left", "←"},
		{"right", "→"},
		{"q", "q"},
		{"R", "R"},
		{"?", "?"},
		{"/", "/"},
	}
	for _, c := range cases {
		got := displayKey(c.in)
		if got != c.want {
			t.Errorf("displayKey(%q)=%q, want %q", c.in, got, c.want)
		}
	}
}

func TestFmtKey(t *testing.T) {
	cases := []struct {
		key, desc, want string
	}{
		{"enter", "open", "↵:open"},
		{" ", "select", "sp:select"},
		{"q", "quit", "q:quit"},
		{"R", "refresh", "R:refresh"},
	}
	for _, c := range cases {
		got := fmtKey(c.key, c.desc)
		if got != c.want {
			t.Errorf("fmtKey(%q,%q)=%q, want %q", c.key, c.desc, got, c.want)
		}
	}
}

func TestDefaultKeymap_NavigationVim(t *testing.T) {
	km := DefaultKeymap()
	if len(km.Navigation.Up) == 0 || km.Navigation.Up[0] != "k" {
		t.Errorf("default Navigation.Up should include 'k', got %v", km.Navigation.Up)
	}
	if len(km.Navigation.Down) == 0 || km.Navigation.Down[0] != "j" {
		t.Errorf("default Navigation.Down should include 'j', got %v", km.Navigation.Down)
	}
	if len(km.Navigation.Home) == 0 || km.Navigation.Home[0] != "g" {
		t.Errorf("default Navigation.Home should include 'g', got %v", km.Navigation.Home)
	}
	if len(km.Navigation.End) == 0 || km.Navigation.End[0] != "G" {
		t.Errorf("default Navigation.End should include 'G', got %v", km.Navigation.End)
	}
}

func TestTranslateNav_NoAliases(t *testing.T) {
	km := DefaultKeymap()
	km.Navigation = NavigationKeymap{} // clear all nav aliases
	origMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")}

	nav, msg := km.TranslateNav("j", origMsg)
	if nav != "" {
		t.Errorf("expected no translation, got nav=%q", nav)
	}
	if msg.Type != origMsg.Type {
		t.Errorf("expected original msg unchanged")
	}
}

func TestTranslateNav_VimKeys(t *testing.T) {
	km := DefaultKeymap()
	km.Navigation.Up = []string{"k"}
	km.Navigation.Down = []string{"j"}
	km.Navigation.PageUp = []string{"ctrl+u"}
	km.Navigation.PageDown = []string{"ctrl+d"}

	cases := []struct {
		key      string
		wantNav  string
		wantType tea.KeyType
	}{
		{"j", "down", tea.KeyDown},
		{"k", "up", tea.KeyUp},
		{"ctrl+u", "pgup", tea.KeyPgUp},
		{"ctrl+d", "pgdown", tea.KeyPgDown},
	}
	for _, c := range cases {
		origMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(c.key)}
		nav, msg := km.TranslateNav(c.key, origMsg)
		if nav != c.wantNav {
			t.Errorf("TranslateNav(%q): nav=%q, want %q", c.key, nav, c.wantNav)
		}
		if msg.Type != c.wantType {
			t.Errorf("TranslateNav(%q): msg.Type=%v, want %v", c.key, msg.Type, c.wantType)
		}
	}
}

func TestTranslateNav_EmacsKeys(t *testing.T) {
	km := DefaultKeymap()
	km.Navigation.Up = []string{"ctrl+p"}
	km.Navigation.Down = []string{"ctrl+n"}
	km.Navigation.Left = []string{"ctrl+b"}
	km.Navigation.Right = []string{"ctrl+f"}

	nav, msg := km.TranslateNav("ctrl+n", tea.KeyMsg{})
	if nav != "down" {
		t.Errorf("ctrl+n: nav=%q, want down", nav)
	}
	if msg.Type != tea.KeyDown {
		t.Errorf("ctrl+n: msg.Type=%v, want KeyDown", msg.Type)
	}

	nav, msg = km.TranslateNav("ctrl+b", tea.KeyMsg{})
	if nav != "left" {
		t.Errorf("ctrl+b: nav=%q, want left", nav)
	}
	if msg.Type != tea.KeyLeft {
		t.Errorf("ctrl+b: msg.Type=%v, want KeyLeft", msg.Type)
	}
}

func TestTranslateNav_NonAlias(t *testing.T) {
	km := DefaultKeymap()
	km.Navigation.Down = []string{"j"}

	// "x" is not a nav alias
	nav, _ := km.TranslateNav("x", tea.KeyMsg{})
	if nav != "" {
		t.Errorf("expected no translation for 'x', got %q", nav)
	}

	// standard "down" is not an alias (it's the canonical key, handled natively)
	nav, _ = km.TranslateNav("down", tea.KeyMsg{Type: tea.KeyDown})
	if nav != "" {
		t.Errorf("expected no translation for 'down', got %q", nav)
	}
}

func TestLoadKeymap_WithNavigation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `navigation:
  up: ["k", "ctrl+p"]
  down: ["j", "ctrl+n"]
  page_up: ["ctrl+u"]
  page_down: ["ctrl+d"]
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	km, err := LoadKeymap(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(km.Navigation.Up) != 2 || km.Navigation.Up[0] != "k" || km.Navigation.Up[1] != "ctrl+p" {
		t.Errorf("Navigation.Up=%v, want [k ctrl+p]", km.Navigation.Up)
	}
	if len(km.Navigation.Down) != 2 || km.Navigation.Down[0] != "j" {
		t.Errorf("Navigation.Down=%v, want [j ctrl+n]", km.Navigation.Down)
	}
	if len(km.Navigation.PageUp) != 1 || km.Navigation.PageUp[0] != "ctrl+u" {
		t.Errorf("Navigation.PageUp=%v, want [ctrl+u]", km.Navigation.PageUp)
	}

	// Unspecified nav keys keep defaults
	def := DefaultKeymap()
	if len(km.Navigation.Home) != len(def.Navigation.Home) {
		t.Errorf("Navigation.Home should keep default %v, got %v", def.Navigation.Home, km.Navigation.Home)
	}
}

// TestMigrateKeymapDefaults verifies old default keymap values are rewritten to
// the new defaults, while user-customized values are preserved.
func TestMigrateKeymapDefaults(t *testing.T) {
	cfg := &CCXConfig{}
	cfg.Keymaps.Session.Edit = "e"
	cfg.Keymaps.Session.Views = "v"
	cfg.Keymaps.Session.Live = "L"
	cfg.Keymaps.Session.Switch = "s"
	cfg.Keymaps.Session.Refresh = "R" // unchanged default
	cfg.Keymaps.Session.Help = "H"    // user-customized, must survive
	migrateKeymapDefaults(cfg)

	if cfg.Keymaps.Session.Edit != "" {
		t.Errorf("Edit = %q, want empty (moved to actions menu)", cfg.Keymaps.Session.Edit)
	}
	if cfg.Keymaps.Session.Views != "V" {
		t.Errorf("Views = %q, want V", cfg.Keymaps.Session.Views)
	}
	if cfg.Keymaps.Session.Live != "" {
		t.Errorf("Live = %q, want empty (removed)", cfg.Keymaps.Session.Live)
	}
	if cfg.Keymaps.Session.Switch != "" {
		t.Errorf("Switch = %q, want empty (removed)", cfg.Keymaps.Session.Switch)
	}
	if cfg.Keymaps.Actions.Edit != "e" {
		t.Errorf("Actions.Edit = %q, want e", cfg.Keymaps.Actions.Edit)
	}
	if cfg.Keymaps.Session.Help != "H" {
		t.Errorf("Help = %q, want H preserved (user override)", cfg.Keymaps.Session.Help)
	}
}
