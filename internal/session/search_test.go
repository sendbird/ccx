package session

import (
	"strings"
	"testing"
)

func TestToolNameMatches(t *testing.T) {
	cases := []struct {
		toolName, filter string
		want             bool
	}{
		{"Read", "read", true},
		{"Read", "grep", false},
		{"mcp__portal__loggerhead_search", "mcp*", true},
		{"mcp__portal__x", "mcp__portal*", true},
		{"mcp__grafana__x", "mcp__portal*", false},
		{"Read", "mcp*", false},
		{"MCP__Portal__X", "mcp*", true}, // case-insensitive prefix
		{"Bash", "bash*", true},
	}
	for _, c := range cases {
		if got := toolNameMatches(c.toolName, c.filter); got != c.want {
			t.Errorf("toolNameMatches(%q, %q) = %v, want %v", c.toolName, c.filter, got, c.want)
		}
	}
}

func TestParseSearchQuery_ToolGlob(t *testing.T) {
	q := ParseSearchQuery("tool:mcp* error")
	if q.ToolName != "mcp*" {
		t.Errorf("ToolName = %q, want mcp*", q.ToolName)
	}
	if len(q.Terms) != 1 || q.Terms[0] != "error" {
		t.Errorf("Terms = %v, want [error]", q.Terms)
	}
}

func TestMCPToolLabel(t *testing.T) {
	cases := []struct {
		in, server, tool string
		ok               bool
	}{
		{"mcp__portal__loggerhead_search_logs", "portal", "loggerhead_search_logs", true},
		{"mcp__grafana__query", "grafana", "query", true},
		{"mcp__x", "x", "", true},
		{"Read", "", "", false},
		{"Bash", "", "", false},
	}
	for _, c := range cases {
		s, tl, ok := MCPToolLabel(c.in)
		if ok != c.ok || s != c.server || tl != c.tool {
			t.Errorf("MCPToolLabel(%q) = (%q,%q,%v), want (%q,%q,%v)", c.in, s, tl, ok, c.server, c.tool, c.ok)
		}
	}
}

// TestBuildSnippet_MultibyteBoundary guards against a panic where the
// firstMatch±40/80 byte-offset arithmetic sliced a multi-byte rune (e.g.
// Korean text) in half, producing invalid UTF-8 that then diverged in
// length from strings.ToLower(text) inside highlightMatches and caused a
// slice-bounds-out-of-range panic.
func TestBuildSnippet_MultibyteBoundary(t *testing.T) {
	// Repeat a 3-byte Korean rune so byte offsets 40 and 80 land mid-rune.
	prefix := strings.Repeat("가", 50) // 150 bytes before the match
	text := prefix + "match" + strings.Repeat("나", 50)

	for i := 0; i < len(text); i++ {
		// Fuzz-ish: just ensure no panic across many slice windows by
		// searching for "match" at its real position each time.
		_ = buildSnippet(text, []string{"match"}, nil)
	}

	snippet := buildSnippet(text, []string{"match"}, nil)
	if !strings.Contains(snippet, "match") {
		t.Errorf("snippet %q missing match", snippet)
	}
	if !strings.HasPrefix(snippet, "...") {
		t.Errorf("expected snippet to be truncated with prefix ellipsis, got %q", snippet)
	}
}

func TestHighlightMatches_NoPanicOnCaseFoldLengthChange(t *testing.T) {
	// Turkish İ lowercases (via strings.ToLower) to a 2-byte "i̇" sequence,
	// which can shift byte offsets between text and strings.ToLower(text).
	// This must not panic even though span offsets are computed against the
	// lowercased string.
	text := strings.Repeat("İ", 30) + "needle" + strings.Repeat("İ", 30)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("highlightMatches panicked: %v", r)
		}
	}()
	_ = highlightMatches(text, []string{"needle"}, nil)
}
