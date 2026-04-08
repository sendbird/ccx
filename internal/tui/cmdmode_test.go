package tui

import "testing"

func TestMatchCommand_ExactName(t *testing.T) {
	registry := buildCmdRegistry()
	found := false
	for _, entry := range registry {
		if matchCmdEntry(entry, "group:flat") {
			found = true
			if entry.name != "group:flat" {
				t.Errorf("expected group:flat, got %s", entry.name)
			}
			break
		}
	}
	if !found {
		t.Error("group:flat not found in registry")
	}
}

func TestMatchCommand_Alias(t *testing.T) {
	registry := buildCmdRegistry()
	found := false
	for _, entry := range registry {
		if matchCmdEntry(entry, "g:flat") {
			found = true
			if entry.name != "group:flat" {
				t.Errorf("expected group:flat via alias, got %s", entry.name)
			}
			break
		}
	}
	if !found {
		t.Error("g:flat alias not found in registry")
	}
}

func TestMatchCommand_PrefixMatch(t *testing.T) {
	registry := buildCmdRegistry()
	var matches []string
	for _, entry := range registry {
		if matchCmdEntry(entry, "grou") {
			matches = append(matches, entry.name)
		}
	}
	if len(matches) == 0 {
		t.Error("prefix 'grou' should match group:* commands")
	}
	for _, m := range matches {
		if m[:5] != "group" {
			t.Errorf("unexpected match: %s", m)
		}
	}
}

func TestMatchCommand_CaseInsensitive(t *testing.T) {
	registry := buildCmdRegistry()
	found := false
	for _, entry := range registry {
		if matchCmdEntry(entry, "GROUP:FLAT") {
			found = true
			break
		}
	}
	if !found {
		t.Error("case-insensitive match should work")
	}
}

func TestFallbackToFilter(t *testing.T) {
	registry := buildCmdRegistry()
	for _, entry := range registry {
		if matchCmdEntry(entry, "is:live") {
			t.Errorf("'is:live' should not match any command, but matched %s", entry.name)
		}
	}
}

func TestSuggestionFiltering_PreviewPrefix(t *testing.T) {
	registry := buildCmdRegistry()
	var matches []cmdEntry
	for _, entry := range registry {
		if matchCmdEntry(entry, "p:") {
			matches = append(matches, entry)
		}
	}
	// Should match all preview commands (p:conv, p:stats, p:mem, p:tasks, p:live)
	if len(matches) < 5 {
		t.Errorf("expected at least 5 preview matches for 'p:', got %d", len(matches))
	}
}

func TestSuggestionFiltering_GroupPrefix(t *testing.T) {
	registry := buildCmdRegistry()
	var matches []cmdEntry
	for _, entry := range registry {
		if matchCmdEntry(entry, "g:") {
			matches = append(matches, entry)
		}
	}
	if len(matches) < 5 {
		t.Errorf("expected at least 5 group matches for 'g:', got %d", len(matches))
	}
}

func TestSetRatioParsing(t *testing.T) {
	cases := []struct {
		input string
		want  int
		ok    bool
	}{
		{"set:ratio 50", 50, true},
		{"set:ratio 15", 15, true},
		{"set:ratio 85", 85, true},
		{"set:ratio abc", 0, false},
		{"set:ratio", 0, false},
	}
	for _, c := range cases {
		got, ok := parseCmdSetRatioValue(c.input)
		if ok != c.ok {
			t.Errorf("parseCmdSetRatioValue(%q): ok=%v, want %v", c.input, ok, c.ok)
			continue
		}
		if ok && got != c.want {
			t.Errorf("parseCmdSetRatioValue(%q)=%d, want %d", c.input, got, c.want)
		}
	}
}

func TestRegistryCompleteness(t *testing.T) {
	registry := buildCmdRegistry()
	expected := map[string]bool{
		"group:flat": false, "group:proj": false, "group:tree": false,
		"group:chain": false, "group:fork": false,
		"pane:flat": false, "pane:tree": false,
		"preview:conv": false, "preview:stats": false, "preview:mem": false,
		"preview:tasks": false, "preview:live": false,
		"view:sessions": false, "view:stats": false, "view:config": false,
		"view:config:hooks": false, "view:plugins": false,
		"view:stats:tools": false, "view:stats:mcp": false,
		"view:stats:agents": false, "view:stats:skills": false,
		"view:stats:commands": false, "view:stats:errors": false,
		"refresh": false,
	}
	for _, entry := range registry {
		if _, ok := expected[entry.name]; ok {
			expected[entry.name] = true
		}
	}
	for name, found := range expected {
		if !found {
			t.Errorf("missing command in registry: %s", name)
		}
	}
}
