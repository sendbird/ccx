package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func keyRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestNormalizeCJKKey(t *testing.T) {
	lm := ResolveLangmap(nil)
	cases := []struct {
		in   tea.KeyMsg
		want string
	}{
		// lowercase jamo → lowercase Latin at the same QWERTY position
		{keyRunes("ㅂ"), "q"},
		{keyRunes("ㅌ"), "x"},
		{keyRunes("ㅓ"), "j"},
		{keyRunes("ㅏ"), "k"},
		{keyRunes("ㅣ"), "l"},
		{keyRunes("ㅁ"), "a"},
		// shifted jamo → uppercase Latin
		{keyRunes("ㄲ"), "R"},
		{keyRunes("ㅃ"), "Q"},
		{keyRunes("ㄸ"), "E"},
		// already-Latin keys pass through untouched
		{keyRunes("q"), "q"},
		{keyRunes("R"), "R"},
		{keyRunes("0"), "0"},
		// non-mappable rune passes through
		{keyRunes("ㅇ"), "d"}, // ㅇ → d
	}
	for _, c := range cases {
		got := NormalizeCJKKey(c.in, lm).String()
		if got != c.want {
			t.Errorf("NormalizeCJKKey(%q) = %q, want %q", c.in.String(), got, c.want)
		}
	}
}

func TestNormalizeCJKKeyLeavesModifiersAndPaste(t *testing.T) {
	lm := ResolveLangmap(nil)
	// Ctrl combos already arrive as Latin — non-KeyRunes, untouched.
	if got := NormalizeCJKKey(tea.KeyMsg{Type: tea.KeyCtrlS}, lm).String(); got != "ctrl+s" {
		t.Errorf("ctrl+s changed: %q", got)
	}
	// Paste of Korean text must pass through verbatim (not remapped char-by-char).
	paste := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("안녕"), Paste: true}
	if got := NormalizeCJKKey(paste, lm); got.String() != paste.String() || !got.Paste {
		t.Errorf("paste mangled: %q paste=%v", got.String(), got.Paste)
	}
	// Alt-modified jamo is left alone.
	alt := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ㅂ"), Alt: true}
	if got := NormalizeCJKKey(alt, lm); got.String() != alt.String() {
		t.Errorf("alt+jamo changed: %q", got.String())
	}
	// A nil langmap is a no-op.
	if got := NormalizeCJKKey(keyRunes("ㅂ"), nil).String(); got != "ㅂ" {
		t.Errorf("nil langmap should be a no-op, got %q", got)
	}
}

// TestResolveLangmapOverride verifies config overrides layer onto the default:
// a new mapping is added, and an empty value removes a default.
func TestResolveLangmapOverride(t *testing.T) {
	lm := ResolveLangmap(map[string]string{
		"の": "n", // add a kana mapping
		"ㅂ": "",  // remove the default ㅂ→q
	})
	if lm['の'] != "n" {
		t.Errorf("override not added: の → %q", lm['の'])
	}
	if _, ok := lm['ㅂ']; ok {
		t.Errorf("empty-value override should remove ㅂ, still present")
	}
	// A default that wasn't overridden survives.
	if lm['ㅌ'] != "x" {
		t.Errorf("untouched default lost: ㅌ → %q", lm['ㅌ'])
	}
}

// TestCJKKeyDrivesShortcutButNotSearch verifies a Korean jamo fires a shortcut
// through the real Update path (ㅌ → x → actions menu), while jamo typed into
// the search field is preserved verbatim.
func TestCJKKeyDrivesShortcutButNotSearch(t *testing.T) {
	app := newTestApp(fakeSessions())
	app.sessSplit.Show = false

	// ㅌ occupies the physical `x` position → opens the actions menu.
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ㅌ")})
	app = m.(*App)
	if !app.actionsMenu {
		t.Fatal("Korean jamo ㅌ should map to x and open the actions menu")
	}
	// Close it.
	app.actionsMenu = false

	// Start search (/), then typing a jamo must reach the filter as Korean, not
	// be remapped to a Latin shortcut.
	m, _ = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	app = m.(*App)
	if !app.isFiltering() {
		t.Fatal("/ should start filtering")
	}
	m, _ = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ㅂ")})
	app = m.(*App)
	if got := app.sessionList.FilterInput.Value(); got != "ㅂ" {
		t.Fatalf("search field should keep Korean jamo verbatim, got %q", got)
	}
}
