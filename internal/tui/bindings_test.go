package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/keyolk/tuikit/render"
)

// Every string field of the keymap sections must have exactly one table
// entry, and every entry must name a real field — otherwise a binding has no
// footer label / help text, or a footer reads a key that does not exist.
func TestBindingTableCoversKeymap(t *testing.T) {
	km := DefaultKeymap()
	seen := map[string]bool{}
	for _, b := range bindingTable {
		id := b.scope + "." + b.name
		if seen[id] {
			t.Errorf("duplicate table entry %s", id)
		}
		seen[id] = true
		if b.label == "" || b.help == "" {
			t.Errorf("%s: empty label or help", id)
		}
	}
	for _, scope := range []string{"session", "actions", "views", "conversation", "preview"} {
		v, ok := km.scopeStruct(scope)
		if !ok {
			t.Fatalf("no keymap section for %s", scope)
		}
		fields := map[string]bool{}
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if f.Type.Kind() != reflect.String {
				continue
			}
			name := f.Tag.Get("yaml")
			fields[name] = true
			if !seen[scope+"."+name] {
				t.Errorf("keymap field %s.%s has no binding table entry", scope, name)
			}
		}
		for _, b := range bindingTable {
			if b.scope == scope && !fields[b.name] {
				t.Errorf("table entry %s.%s names no keymap field", scope, b.name)
			}
		}
	}
}

func TestDefaultKeymapHasNoConflicts(t *testing.T) {
	km := DefaultKeymap()
	if c := km.Conflicts(); len(c) != 0 {
		t.Fatalf("default keymap conflicts: %v", c)
	}
	km.Actions.Move = km.Actions.Delete
	if c := km.Conflicts(); len(c) != 1 || !strings.Contains(c[0], "delete") || !strings.Contains(c[0], "move") {
		t.Fatalf("Conflicts() = %v, want one actions conflict", c)
	}
	// Same key in different scopes is not a conflict (menus dispatch per scope).
	km = DefaultKeymap()
	km.Actions.Kill = km.Session.Quit
	if c := km.Conflicts(); len(c) != 0 {
		t.Fatalf("cross-scope reuse reported as conflict: %v", c)
	}
}

// globalIntercepts are keys Update consumes before view dispatch.
func globalIntercepts(km Keymap) map[string]bool {
	out := map[string]bool{km.Session.Command: true, km.Session.Help: true}
	for c := '0'; c <= '9'; c++ {
		out[string(c)] = true
	}
	return out
}

// Every binding the "?" overlay lists for the session list must reach its
// handler: bound, not shadowed by another binding in the scope, and not
// swallowed by a global intercept that runs before view dispatch.
func TestSessionsHelpActionsAreReachable(t *testing.T) {
	a := newTestApp(fakeSessions())
	km := a.keymap
	if c := km.Conflicts(); len(c) != 0 {
		t.Fatalf("conflicts: %v", c)
	}
	intercept := globalIntercepts(km)
	helpKeys := map[string]bool{}
	for _, r := range append(a.sessionsListHelpRows(), a.helpCommonRows()...) {
		if r.key == "" {
			t.Errorf("help row %q has no key", r.desc)
		}
		helpKeys[r.key] = true
	}
	for _, b := range bindingTable {
		if b.scope != "session" || b.name == "command" || b.name == "help" || b.name == "pick" {
			continue
		}
		k := km.bindingKey("session", b.name)
		if k == "" {
			continue
		}
		if intercept[k] {
			t.Errorf("session.%s = %q is swallowed by a global intercept", b.name, k)
		}
		if !helpKeys[displayKey(k)] && !strings.Contains(strings.Join(keysOf(helpKeys), " "), displayKey(k)) {
			t.Errorf("session.%s (%q) is bound but missing from the ? overlay", b.name, k)
		}
	}
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// footerTokens returns the "key" part of each "key label" hint; hints are
// separated by two spaces (tuikit's footer separator).
func footerTokens(s string) []string {
	var out []string
	for _, hint := range strings.Split(stripANSI(s), "  ") {
		if k, _, ok := strings.Cut(strings.TrimSpace(hint), " "); ok && k != "" {
			out = append(out, k)
		}
	}
	return out
}

// Whatever the session-list footer advertises, the ? overlay documents.
func TestSessionFooterKeysAppearInHelp(t *testing.T) {
	a := newTestApp(fakeSessions())
	a.width = 400
	help := map[string]bool{}
	for _, r := range append(a.sessionsListHelpRows(), a.helpCommonRows()...) {
		for _, k := range strings.Split(r.key, " / ") {
			help[strings.TrimSpace(k)] = true
		}
	}
	toks := footerTokens(a.sessHelpLine())
	if len(toks) < 3 {
		t.Fatalf("footer parsed into %v; the token format changed", toks)
	}
	for _, k := range toks {
		if !help[k] {
			t.Errorf("footer key %q is not in the ? overlay", k)
		}
	}
}

func TestFooterDropsLowPriorityHintsAndPinsHelpQuit(t *testing.T) {
	a := newTestApp(fakeSessions())
	a.width = 400
	wide := stripANSI(a.sessHelpLine())
	if strings.Contains(wide, render.Ellipsis()) {
		t.Fatalf("wide footer should not be cut: %q", wide)
	}

	a.width = 36
	narrow := a.sessHelpLine()
	plain := stripANSI(narrow)
	if w := lipgloss.Width(narrow); w > a.width {
		t.Fatalf("footer width %d > %d: %q", w, a.width, plain)
	}
	if !strings.Contains(plain, render.Ellipsis()) {
		t.Fatalf("cut footer should show the ellipsis: %q", plain)
	}
	if !strings.HasSuffix(plain, "? help  q quit") {
		t.Fatalf("help/quit not pinned at the end: %q", plain)
	}
	// Highest-priority hint survives.
	if !strings.Contains(plain, "↵ open") {
		t.Fatalf("first hint dropped before lower ones: %q", plain)
	}
}

func TestFooterKeepsPinnedHint(t *testing.T) {
	out := stripANSI(renderFooter("", []footerHint{fh("a", "one"), fh("b", "two"), fh("esc", "back").keep(), fh("?", "help").keep()}, 22))
	if !strings.Contains(out, "esc back") || strings.Contains(out, "b two") {
		t.Fatalf("pinned hint dropped or low hint kept: %q", out)
	}
}

// A keymap override moves the binding in dispatch, footer and help at once.
func TestKeymapOverrideReachesFooterHelpAndDispatch(t *testing.T) {
	a := newTestApp(fakeSessions())
	a.width = 400
	a.keymap.Session.PageMenu = "m"
	a.keymap.Session.Views = "w"

	a.state = viewGlobalStats
	a.globalStatsLoading = false
	if got := stripANSI(a.View()); !strings.Contains(got, "m page") || !strings.Contains(got, "w views") {
		t.Fatalf("stats footer did not follow the override: %q", got[max(0, len(got)-200):])
	}
	a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if !a.statsPageMenu {
		t.Fatal("overridden page key did not open the stats page menu")
	}

	var found bool
	for _, r := range a.helpCommonRows() {
		found = found || (r.key == "w" && strings.Contains(r.desc, "views"))
	}
	if !found {
		t.Fatal("? overlay did not follow the views override")
	}
}

func TestResolveSplitKeyHonorsOverride(t *testing.T) {
	km := DefaultKeymap()
	km.Session.Left, km.Session.ResizeGrow = "H", "+"
	if got := km.resolveSplitKey("H"); got != "left" {
		t.Fatalf("resolveSplitKey(H) = %q, want left", got)
	}
	if got := km.resolveSplitKey("+"); got != "]" {
		t.Fatalf("resolveSplitKey(+) = %q, want ]", got)
	}
	if got := km.resolveSplitKey("x"); got != "x" {
		t.Fatalf("unrelated key rewritten: %q", got)
	}
}

// The state-menu footer used to say "S:states" while the key is "s".
func TestMenuFootersUseBoundKeys(t *testing.T) {
	a := newTestApp(fakeSessions())
	a.stateMenu = true
	got := stripANSI(a.View())
	want := displayKey(a.keymap.Session.StateMenu) + " states"
	if !strings.Contains(got, want) {
		t.Fatalf("state menu footer missing %q", want)
	}
}

// Conflicts runs tuikit's checks, which add Korean reachability to the
// duplicate check: an action bound only to an uppercase letter a 2-set layout
// cannot type is reported, and a legacy alias does not count as a duplicate.
func TestConflictsReportsKeysUnreachableUnderKorean(t *testing.T) {
	km := DefaultKeymap()
	km.Actions.Fork = "F" // ㄹ with or without Shift is "f"
	c := km.Conflicts()
	if len(c) != 1 || !strings.Contains(c[0], "actions.fork") {
		t.Fatalf("Conflicts() = %v, want actions.fork unreachable", c)
	}
}
