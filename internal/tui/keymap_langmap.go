package tui

import tea "github.com/charmbracelet/bubbletea"

// defaultHangulToLatin maps Hangul jamo produced by the 2-set (두벌식) Korean
// layout to the Latin key at the same physical position on a US QWERTY
// keyboard. This lets ccx's single-letter shortcuts (q, x, R, j/k, …) work while
// the OS input source is Korean, without switching back to English — mirroring
// Vim's `langmap`.
//
// Users can override or extend this (e.g. for 세벌식, pinyin, kana) via the
// `langmap` section in ~/.config/ccx/config.yaml; see ResolveLangmap.
func defaultHangulToLatin() map[rune]string {
	return map[rune]string{
		// lowercase row (unshifted jamo → lowercase Latin)
		'ㅂ': "q", 'ㅈ': "w", 'ㄷ': "e", 'ㄱ': "r", 'ㅅ': "t",
		'ㅛ': "y", 'ㅕ': "u", 'ㅑ': "i", 'ㅐ': "o", 'ㅔ': "p",
		'ㅁ': "a", 'ㄴ': "s", 'ㅇ': "d", 'ㄹ': "f", 'ㅎ': "g",
		'ㅗ': "h", 'ㅓ': "j", 'ㅏ': "k", 'ㅣ': "l",
		'ㅋ': "z", 'ㅌ': "x", 'ㅊ': "c", 'ㅍ': "v", 'ㅠ': "b",
		'ㅜ': "n", 'ㅡ': "m",
		// shifted jamo → uppercase Latin (double consonants + ㅒㅖ)
		'ㅃ': "Q", 'ㅉ': "W", 'ㄸ': "E", 'ㄲ': "R", 'ㅆ': "T",
		'ㅒ': "O", 'ㅖ': "P",
	}
}

// ResolveLangmap builds the effective source-rune → Latin-key map: the built-in
// Korean 2-set default, with any user overrides from config layered on top. A
// config entry whose value is empty removes that mapping (lets a user disable a
// specific default). Config keys are single source characters (the jamo/letter
// the OS emits); values are the Latin key to translate it to.
func ResolveLangmap(override map[string]string) map[rune]string {
	m := defaultHangulToLatin()
	for k, v := range override {
		kr := []rune(k)
		if len(kr) != 1 {
			continue // only single-character source keys are meaningful
		}
		if v == "" {
			delete(m, kr[0])
			continue
		}
		m[kr[0]] = v
	}
	return m
}

// NormalizeCJKKey rewrites a single-source-rune KeyMsg to the Latin key at the
// same physical position using langmap, so shortcuts fire under a CJK input
// source. It returns the original msg unchanged when the key is not mapped, is a
// paste, or carries modifiers (ctrl/alt) — those already arrive as Latin. A nil
// langmap is a no-op (returns msg).
//
// Callers must only apply this when NOT in a text input, so CJK text can still
// be typed into search/filter fields verbatim. Exported so the CLI picker
// (package cli) shares the same mapping as the TUI.
func NormalizeCJKKey(msg tea.KeyMsg, langmap map[rune]string) tea.KeyMsg {
	if langmap == nil || msg.Type != tea.KeyRunes || msg.Paste || msg.Alt || len(msg.Runes) != 1 {
		return msg
	}
	latin, ok := langmap[msg.Runes[0]]
	if !ok {
		return msg
	}
	r := []rune(latin)
	if len(r) == 0 {
		return msg
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r[0]}}
}
