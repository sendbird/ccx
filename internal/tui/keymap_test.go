package tui

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
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
	if km.Session.Views != "v" {
		t.Errorf("Session.Views = %q, want v", km.Session.Views)
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
	if km.Conversation.ExecutionContexts != "a" {
		t.Errorf("DefaultKeymap().Conversation.ExecutionContexts=%q, want a", km.Conversation.ExecutionContexts)
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
	cfg.Keymaps.Session.Views = "V"
	cfg.Keymaps.Session.Live = "L"
	cfg.Keymaps.Session.Switch = "s"
	cfg.Keymaps.Session.Refresh = "R" // unchanged default
	cfg.Keymaps.Session.Help = "H"    // user-customized, must survive
	cfg.Keymaps.Actions.Fork = "F"
	cfg.Keymaps.Conversation.RegionUp = "K"
	cfg.Keymaps.Preview.ExpandAll = "F"
	cfg.Keymaps.Session.DailyView = "D"
	migrateKeymapDefaults(cfg)

	if cfg.Keymaps.Session.Edit != "" {
		t.Errorf("Edit = %q, want empty (moved to actions menu)", cfg.Keymaps.Session.Edit)
	}
	if cfg.Keymaps.Session.Views != "v" {
		t.Errorf("Views = %q, want v (CJK-reachable rebind)", cfg.Keymaps.Session.Views)
	}
	if cfg.Keymaps.Actions.Fork != "b" {
		t.Errorf("Actions.Fork = %q, want b", cfg.Keymaps.Actions.Fork)
	}
	if cfg.Keymaps.Conversation.RegionUp != "ctrl+p" {
		t.Errorf("Conversation.RegionUp = %q, want ctrl+p", cfg.Keymaps.Conversation.RegionUp)
	}
	if cfg.Keymaps.Preview.ExpandAll != "u" {
		t.Errorf("Preview.ExpandAll = %q, want u", cfg.Keymaps.Preview.ExpandAll)
	}
	if cfg.Keymaps.Session.DailyView != "d" {
		t.Errorf("Session.DailyView = %q, want d", cfg.Keymaps.Session.DailyView)
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

// TestDefaultKeymapIsReachableUnderCJK is the guard for a whole bug class: a
// default bound to an uppercase letter that a 2-set Korean layout cannot
// produce is dead for anyone typing under that input source, and when a
// lowercase action shares the letter (Files "f" vs Fork "F", Move "m" vs
// ImportMem "M") the switch silently resolves every press to whichever case it
// tests first. Reflection over the whole struct means a newly added field is
// covered without anyone remembering to extend this list.
func TestDefaultKeymapIsReachableUnderCJK(t *testing.T) {
	reachable := cjkReachableUpper()
	km := DefaultKeymap()
	v := reflect.ValueOf(km)
	for i := 0; i < v.NumField(); i++ {
		sec, secName := v.Field(i), v.Type().Field(i).Name
		if sec.Kind() != reflect.Struct {
			continue
		}
		for j := 0; j < sec.NumField(); j++ {
			f := sec.Field(j)
			if f.Kind() != reflect.String {
				continue
			}
			key := f.String()
			// Only bare single letters are at risk; "ctrl+…"/"shift+tab"/"enter"
			// and friends carry a modifier or name that survives the IME.
			if len(key) != 1 || key[0] < 'A' || key[0] > 'Z' {
				continue
			}
			if !reachable[key] {
				t.Errorf("%s.%s = %q is unreachable under a 2-set Korean input source; "+
					"use a lowercase or ctrl+ binding (reachable uppercase: %v)",
					secName, sec.Type().Field(j).Name, key, reachable)
			}
		}
	}
}

// TestActionsMenuKeysAreUnique guards the other half of the same failure: two
// actions in one menu answering to the same key, where only the first case in
// the switch can ever run.
func TestActionsMenuKeysAreUnique(t *testing.T) {
	akm := DefaultKeymap().Actions
	v := reflect.ValueOf(akm)
	seen := map[string]string{}
	for i := 0; i < v.NumField(); i++ {
		key := v.Field(i).String()
		name := v.Type().Field(i).Name
		if key == "" {
			continue
		}
		if prev, dup := seen[key]; dup {
			t.Errorf("Actions.%s and Actions.%s both bind %q; the later case is dead", prev, name, key)
		}
		seen[key] = name
	}
}

// TestSessionTopLevelKeysAreUnique does the same for the session list. Only the
// bindings that dispatch from its one top-level switch are listed — Left/Right
// and the preview keys are handled in separate, mutually exclusive branches, so
// sharing a key with them is not a conflict.
func TestSessionTopLevelKeysAreUnique(t *testing.T) {
	km := DefaultKeymap().Session
	seen := map[string]string{}
	for _, b := range []struct{ name, key string }{
		{"Quit", km.Quit}, {"Escape", km.Escape}, {"Open", km.Open},
		{"Actions", km.Actions}, {"Views", km.Views}, {"Refresh", km.Refresh},
		{"Help", km.Help}, {"Search", km.Search}, {"GlobalSearch", km.GlobalSearch},
		{"Select", km.Select}, {"Preview", km.Preview}, {"PreviewBack", km.PreviewBack},
		{"ResizeShrink", km.ResizeShrink}, {"ResizeGrow", km.ResizeGrow},
		{"Command", km.Command}, {"Pick", km.Pick},
		{"FoldAll", km.FoldAll}, {"ExpandAll", km.ExpandAll}, {"FoldGroup", km.FoldGroup},
		{"StateMenu", km.StateMenu}, {"DailyView", km.DailyView}, {"PageMenu", km.PageMenu},
	} {
		if b.key == "" {
			continue
		}
		if prev, dup := seen[b.key]; dup {
			t.Errorf("Session.%s and Session.%s both bind %q; the later case is dead", prev, b.name, b.key)
		}
		seen[b.key] = b.name
	}
}

// TestNoHardcodedUnreachableKeyLiterals scans the package source for key
// dispatch on a bare uppercase letter that a 2-set Korean layout cannot
// produce. The reflection guard above only sees the Keymap struct, so a
// literal like `key == "D"` sitting directly in a handler is invisible to it —
// which is exactly how the daily-view toggle stayed unreachable. Matching on
// source is blunt, but it covers the gap the struct walk cannot.
func TestNoHardcodedUnreachableKeyLiterals(t *testing.T) {
	reachable := cjkReachableUpper()
	// Only dispatch sites: `case "X":` / `case "X",` and comparisons against a
	// key variable. Deliberately NOT bare `== "X"`, which also matches config
	// migrations comparing an old default (`s.Live == "L"`).
	pat := regexp.MustCompile(`case\s+"([A-Z])"\s*[:,]|(?:key|String\(\))\s*==\s*"([A-Z])"`)
	multiCharKey := regexp.MustCompile(`"[a-z]{2,}"`)

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			m := pat.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			key := m[1]
			if key == "" {
				key = m[2]
			}
			if reachable[key] {
				continue
			}
			// An adjacent lowercase alternative (`"y" || "Y"`) or a named key
			// (`case "G", "end":`) keeps the action reachable anyway.
			if strings.Contains(line, `"`+strings.ToLower(key)+`"`) || multiCharKey.MatchString(line) {
				continue
			}
			t.Errorf("%s:%d dispatches on %q, which is unreachable under a 2-set "+
				"Korean input source. Move it into the Keymap (so the reflection "+
				"guard covers it) and pick a lowercase or ctrl+ binding.\n\t%s",
				name, i+1, key, strings.TrimSpace(line))
		}
	}
}

// TestKeymapConfigItemsCoverEveryBinding pins the KEYMAPS config page to the
// Keymap struct. The page used to list fields by hand and had drifted, omitting
// 15 bindings — most of the actions menu — so it reported keys as nonexistent
// that the menu answered to. Reflection closed that gap; this keeps it closed.
func TestKeymapConfigItemsCoverEveryBinding(t *testing.T) {
	app := &App{keymap: DefaultKeymap()}
	listed := map[string]bool{}
	for _, it := range app.keymapConfigItems() {
		name, _, _ := strings.Cut(it.Name, " = ")
		listed[it.Group+"."+name] = true
	}

	km := reflect.ValueOf(DefaultKeymap())
	kt := km.Type()
	for i := range kt.NumField() {
		section := yamlName(kt.Field(i))
		sv := km.Field(i)
		if sv.Kind() != reflect.Struct {
			continue
		}
		for j := range sv.NumField() {
			f := sv.Field(j)
			// Empty defaults are intentionally unbound, and Navigation holds
			// []string alias lists rather than single bindings.
			if f.Kind() != reflect.String || f.String() == "" {
				continue
			}
			want := section + "." + yamlName(sv.Type().Field(j))
			if !listed[want] {
				t.Errorf("KEYMAPS config page omits %q; it is bound by default but invisible there", want)
			}
		}
	}
}

// TestFillKeymapDefaultsCoversEveryField guards the config round-trip: a field
// fillKeymapDefaults forgets is written to config.yaml empty, so a user editing
// the file sees a blank where a binding should be. Actions.Changes/Copy/Tags
// were missing exactly this way.
func TestFillKeymapDefaultsCoversEveryField(t *testing.T) {
	d := DefaultKeymap()
	cfg := &CCXConfig{}
	fillKeymapDefaults(cfg, d)

	filled := reflect.ValueOf(cfg.Keymaps)
	want := reflect.ValueOf(d)
	wantType := want.Type()
	for i := range wantType.NumField() {
		section := wantType.Field(i).Name
		fs := filled.FieldByName(section)
		if !fs.IsValid() || fs.Kind() != reflect.Struct {
			continue // Navigation, or a section KeymapsConfig does not carry
		}
		ws := want.Field(i)
		for j := range ws.NumField() {
			wf := ws.Field(j)
			if wf.Kind() != reflect.String || wf.String() == "" {
				continue
			}
			name := ws.Type().Field(j).Name
			if got := fs.FieldByName(name); got.IsValid() && got.String() == "" {
				t.Errorf("fillKeymapDefaults left %s.%s empty; it would round-trip "+
					"to config.yaml as a blank binding", section, name)
			}
		}
	}
}

// TestLegacyKeysStillWork pins the aliases that keep the pre-rebind keys alive.
//
// CPLAT-12413 moved ten actions off bare uppercase letters because a 2-set
// Korean layout cannot produce them. That was real, but it also took those keys
// away from everyone typing in English, where they had worked for months —
// region nav had been K/J since CPLAT-10969, and its loss was reported as
// "shift+hjkl doesn't switch regions any more". Both spellings must work.
func TestLegacyKeysStillWork(t *testing.T) {
	km := DefaultKeymap()
	for _, c := range []struct{ scope, old, want string }{
		{"session", "V", km.Session.Views},
		{"session", "D", km.Session.DailyView},
		{"actions", "F", km.Actions.Fork},
		{"actions", "M", km.Actions.ImportMem},
		{"actions", "X", km.Actions.RemoveMem},
		{"conversation", "A", km.Conversation.ExecutionContexts},
		{"conversation", "K", km.Conversation.RegionUp},
		{"conversation", "J", km.Conversation.RegionDown},
		{"conversation", "L", km.Conversation.LiveToggle},
		{"conversation", "I", km.Conversation.Input},
		{"preview", "F", km.Preview.ExpandAll},
	} {
		if got := km.resolveLegacyKey(c.scope, c.old); got != c.want {
			t.Errorf("%s: legacy %q resolved to %q, want %q (the key it replaced)",
				c.scope, c.old, got, c.want)
		}
	}

	// Everything else passes through untouched.
	for _, k := range []string{"q", "x", "enter", "esc", "ctrl+p", "Z", "f"} {
		for _, scope := range []string{"session", "actions", "conversation", "preview"} {
			if got := km.resolveLegacyKey(scope, k); got != k {
				t.Errorf("%s: unrelated key %q was rewritten to %q", scope, k, got)
			}
		}
	}
}

// TestLegacyAliasYieldsToUserBinding: an alias must never shadow a key the user
// deliberately bound to something else.
func TestLegacyAliasYieldsToUserBinding(t *testing.T) {
	km := DefaultKeymap()
	km.Conversation.Actions = "K" // user claims the old region-up key
	if got := km.resolveLegacyKey("conversation", "K"); got != "K" {
		t.Errorf("alias shadowed the user's own binding: K -> %q", got)
	}
	// The unclaimed aliases in the same scope still work.
	if got := km.resolveLegacyKey("conversation", "J"); got != km.Conversation.RegionDown {
		t.Errorf("unrelated alias broke: J -> %q", got)
	}
}

// TestLegacyAliasesCoverEveryRebind ties the alias table to the migration table
// in state.go: every default the rebind moved must keep its old key working.
// Without this, a future rebind can silently drop a key people still press.
func TestLegacyAliasesCoverEveryRebind(t *testing.T) {
	km := DefaultKeymap()
	aliased := map[string]bool{}
	for _, binds := range km.legacyAliases() {
		for _, b := range binds {
			aliased[b.old] = true
		}
	}
	// The uppercase keys migrateKeymapDefaults rewrites (see state.go).
	for _, old := range []string{"V", "D", "F", "M", "X", "A", "K", "J", "L", "I"} {
		if !aliased[old] {
			t.Errorf("rebind moved %q away but no alias keeps it working", old)
		}
	}
}
