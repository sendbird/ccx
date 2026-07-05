package session

import "testing"

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
