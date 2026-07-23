package tmux

import "testing"

func TestNormalizeWindowName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"ccx", "ccx"},
		{"ccx:ad9ef9b", "ccxad9ef9b"},
		{"ccx*", "ccx"},           // active-window marker stripped
		{"ccx-", "ccx-"},          // hyphen kept
		{"ccx_v2", "ccx_v2"},      // underscore kept
		{"ccx (live)", "ccxlive"}, // spaces + parens stripped
		{"ccx·plan", "ccxplan"},   // non-ASCII stripped
		{"*", ""},                 // only status → empty
		{"", ""},
	}
	for _, c := range cases {
		if got := NormalizeWindowName(c.in); got != c.want {
			t.Errorf("NormalizeWindowName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsShell(t *testing.T) {
	for _, s := range []string{"bash", "zsh", "fish", "sh"} {
		if !isShell(s) {
			t.Errorf("isShell(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"node", "vim", "claude", "less", ""} {
		if isShell(s) {
			t.Errorf("isShell(%q) = true, want false", s)
		}
	}
}
