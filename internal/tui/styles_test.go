package tui

import "testing"

// TestBadgeIconsNonEmpty guards against a badge glyph being left as the empty
// string, which renders the badge with no leading icon. Regression: iconBadgePR
// was "" so the open-PR badge showed as "[ PR]" with a blank icon slot while
// every other badge had its Nerd Font glyph.
func TestBadgeIconsNonEmpty(t *testing.T) {
	icons := map[string]string{
		"iconBadgeLive":   iconBadgeLive,
		"iconBadgeBusy":   iconBadgeBusy,
		"iconBadgeBg":     iconBadgeBg,
		"iconBadgeMon":    iconBadgeMon,
		"iconBadgeHere":   iconBadgeHere,
		"iconBadgeDone":   iconBadgeDone,
		"iconBadgeWait":   iconBadgeWait,
		"iconBadgeStuck":  iconBadgeStuck,
		"iconBadgeInput":  iconBadgeInput,
		"iconBadgeRemote": iconBadgeRemote,
		"iconBadgePR":     iconBadgePR,
	}
	for name, glyph := range icons {
		if glyph == "" {
			t.Errorf("%s is empty — badge would render with a blank icon slot", name)
		}
	}
}

// TestBadgeLabelIncludesIcon verifies badgeLabel actually places the icon before
// the text so the rendered PR badge carries its glyph.
func TestBadgeLabelIncludesIcon(t *testing.T) {
	got := badgeLabel(iconBadgePR, "PR")
	if want := "[" + iconBadgePR + " PR]"; got != want {
		t.Fatalf("badgeLabel = %q, want %q", got, want)
	}
	if iconBadgePR == "" {
		t.Fatal("PR badge label has no icon (iconBadgePR empty)")
	}
}
