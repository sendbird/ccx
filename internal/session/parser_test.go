package session

import (
	"testing"
	"time"
)

func TestParseEntry_TextContent(t *testing.T) {
	line := `{"type":"","timestamp":"2025-01-15T10:30:00Z","message":{"role":"user","content":"hello world"}}`
	entry, err := ParseEntry(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry.Role != "user" {
		t.Errorf("role = %q, want %q", entry.Role, "user")
	}
	if len(entry.Content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(entry.Content))
	}
	if entry.Content[0].Type != "text" {
		t.Errorf("block type = %q, want %q", entry.Content[0].Type, "text")
	}
	if entry.Content[0].Text != "hello world" {
		t.Errorf("block text = %q, want %q", entry.Content[0].Text, "hello world")
	}
	if entry.Timestamp.IsZero() {
		t.Error("timestamp should not be zero")
	}
}

func TestParseEntry_ToolUse(t *testing.T) {
	line := `{"type":"","message":{"role":"assistant","content":[{"type":"tool_use","name":"Read","input":{"file_path":"/tmp/test.go"}}]}}`
	entry, err := ParseEntry(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry.Role != "assistant" {
		t.Errorf("role = %q, want %q", entry.Role, "assistant")
	}
	if len(entry.Content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(entry.Content))
	}
	b := entry.Content[0]
	if b.Type != "tool_use" {
		t.Errorf("type = %q, want %q", b.Type, "tool_use")
	}
	if b.ToolName != "Read" {
		t.Errorf("tool name = %q, want %q", b.ToolName, "Read")
	}
	if b.ToolInput == "" {
		t.Error("tool input should not be empty")
	}
}

func TestParseEntry_ToolResult(t *testing.T) {
	line := `{"type":"","message":{"role":"user","content":[{"type":"tool_result","content":"file contents here","is_error":false}]}}`
	entry, err := ParseEntry(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entry.Content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(entry.Content))
	}
	b := entry.Content[0]
	if b.Type != "tool_result" {
		t.Errorf("type = %q, want %q", b.Type, "tool_result")
	}
	if b.Text != "file contents here" {
		t.Errorf("text = %q, want %q", b.Text, "file contents here")
	}
	if b.IsError {
		t.Error("is_error should be false")
	}
}

func TestParseEntry_ToolResultError(t *testing.T) {
	line := `{"type":"","message":{"role":"user","content":[{"type":"tool_result","content":"command failed","is_error":true}]}}`
	entry, err := ParseEntry(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !entry.Content[0].IsError {
		t.Error("is_error should be true")
	}
}

func TestParseEntry_ToolResultArrayContent(t *testing.T) {
	line := `{"type":"","message":{"role":"user","content":[{"type":"tool_result","content":[{"type":"text","text":"line1"},{"type":"text","text":"line2"}]}]}}`
	entry, err := ParseEntry(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry.Content[0].Text != "line1\nline2" {
		t.Errorf("text = %q, want %q", entry.Content[0].Text, "line1\nline2")
	}
}

func TestParseEntry_Thinking(t *testing.T) {
	line := `{"type":"","message":{"role":"assistant","content":[{"type":"thinking","text":"let me think about this"}]}}`
	entry, err := ParseEntry(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entry.Content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(entry.Content))
	}
	b := entry.Content[0]
	if b.Type != "thinking" {
		t.Errorf("type = %q, want %q", b.Type, "thinking")
	}
	if b.Text != "let me think about this" {
		t.Errorf("text = %q, want %q", b.Text, "let me think about this")
	}
}

func TestParseEntry_Image(t *testing.T) {
	line := `{"type":"","message":{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png"}}]}}`
	entry, err := ParseEntry(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entry.Content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(entry.Content))
	}
	b := entry.Content[0]
	if b.Type != "image" {
		t.Errorf("type = %q, want %q", b.Type, "image")
	}
	if b.Text != "[Image: image/png]" {
		t.Errorf("text = %q, want %q", b.Text, "[Image: image/png]")
	}
}

func TestParseEntry_ImageNoSource(t *testing.T) {
	line := `{"type":"","message":{"role":"user","content":[{"type":"image"}]}}`
	entry, err := ParseEntry(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry.Content[0].Text != "[Image: image]" {
		t.Errorf("text = %q, want %q", entry.Content[0].Text, "[Image: image]")
	}
}

func TestParseEntry_MixedContent(t *testing.T) {
	line := `{"type":"","message":{"role":"assistant","content":[{"type":"text","text":"I will read the file"},{"type":"tool_use","name":"Read","input":{"file_path":"/tmp/x"}}]}}`
	entry, err := ParseEntry(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entry.Content) != 2 {
		t.Fatalf("content blocks = %d, want 2", len(entry.Content))
	}
	if entry.Content[0].Type != "text" {
		t.Errorf("block[0] type = %q, want text", entry.Content[0].Type)
	}
	if entry.Content[1].Type != "tool_use" {
		t.Errorf("block[1] type = %q, want tool_use", entry.Content[1].Type)
	}
}

func TestParseEntry_NilContent(t *testing.T) {
	line := `{"type":"","message":{"role":"assistant"}}`
	entry, err := ParseEntry(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entry.Content) != 0 {
		t.Errorf("content blocks = %d, want 0", len(entry.Content))
	}
}

func TestParseEntry_EmptyStringContent(t *testing.T) {
	line := `{"type":"","message":{"role":"user","content":""}}`
	entry, err := ParseEntry(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entry.Content) != 0 {
		t.Errorf("content blocks = %d, want 0 (empty string)", len(entry.Content))
	}
}

func TestParseEntry_EmptyArrayContent(t *testing.T) {
	line := `{"type":"","message":{"role":"user","content":[]}}`
	entry, err := ParseEntry(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entry.Content) != 0 {
		t.Errorf("content blocks = %d, want 0", len(entry.Content))
	}
}

func TestParseEntry_Timestamp(t *testing.T) {
	tests := []struct {
		name string
		ts   string
		want time.Time
	}{
		{"RFC3339", `2025-01-15T10:30:00Z`, time.Date(2025, 1, 15, 10, 30, 0, 0, time.UTC)},
		{"RFC3339Nano", `2025-01-15T10:30:00.123456789Z`, time.Date(2025, 1, 15, 10, 30, 0, 123456789, time.UTC)},
		{"empty", ``, time.Time{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			line := `{"type":"","timestamp":"` + tt.ts + `","message":{"role":"user","content":"hi"}}`
			if tt.ts == "" {
				line = `{"type":"","message":{"role":"user","content":"hi"}}`
			}
			entry, err := ParseEntry(line)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !entry.Timestamp.Equal(tt.want) {
				t.Errorf("timestamp = %v, want %v", entry.Timestamp, tt.want)
			}
		})
	}
}

func TestParseEntry_MetaFields(t *testing.T) {
	line := `{"type":"user","isMeta":true,"uuid":"abc-123","parentUuid":"parent-1","agentId":"agent-x","message":{"role":"user","content":"test"}}`
	entry, err := ParseEntry(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry.Type != "user" {
		t.Errorf("type = %q, want %q", entry.Type, "user")
	}
	if !entry.IsMeta {
		t.Error("isMeta should be true")
	}
	if entry.UUID != "abc-123" {
		t.Errorf("uuid = %q, want %q", entry.UUID, "abc-123")
	}
	if entry.ParentID != "parent-1" {
		t.Errorf("parentID = %q, want %q", entry.ParentID, "parent-1")
	}
	if entry.AgentID != "agent-x" {
		t.Errorf("agentID = %q, want %q", entry.AgentID, "agent-x")
	}
}

func TestParseEntry_InvalidJSON(t *testing.T) {
	_, err := ParseEntry("not json")
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestParseEntry_Model(t *testing.T) {
	line := `{"type":"","message":{"role":"assistant","model":"claude-sonnet-4-20250514","content":"hello"}}`
	entry, err := ParseEntry(line)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry.Model != "claude-sonnet-4-20250514" {
		t.Errorf("model = %q, want %q", entry.Model, "claude-sonnet-4-20250514")
	}
}

func TestEntryPreview_TextOnly(t *testing.T) {
	e := Entry{Content: []ContentBlock{{Type: "text", Text: "hello world"}}}
	got := EntryPreview(e)
	if got != "hello world" {
		t.Errorf("preview = %q, want %q", got, "hello world")
	}
}

func TestEntryPreview_LongText(t *testing.T) {
	long := ""
	for i := 0; i < 120; i++ {
		long += "a"
	}
	e := Entry{Content: []ContentBlock{{Type: "text", Text: long}}}
	got := EntryPreview(e)
	if len(got) != 100 {
		t.Errorf("preview length = %d, want 100", len(got))
	}
	if got[97:] != "..." {
		t.Errorf("preview should end with ..., got %q", got[97:])
	}
}

func TestEntryPreview_ToolsOnly(t *testing.T) {
	e := Entry{Content: []ContentBlock{
		{Type: "tool_use", ToolName: "Read"},
		{Type: "tool_use", ToolName: "Edit"},
	}}
	got := EntryPreview(e)
	if got != "[Read, Edit]" {
		t.Errorf("preview = %q, want %q", got, "[Read, Edit]")
	}
}

func TestEntryPreview_ImagesOnly(t *testing.T) {
	e := Entry{Content: []ContentBlock{
		{Type: "image", Text: "[Image: image/png]"},
		{Type: "image", Text: "[Image: image/jpeg]"},
	}}
	got := EntryPreview(e)
	if got != "[2 image(s)]" {
		t.Errorf("preview = %q, want %q", got, "[2 image(s)]")
	}
}

func TestEntryPreview_NoContent(t *testing.T) {
	e := Entry{}
	got := EntryPreview(e)
	if got != "(no content)" {
		t.Errorf("preview = %q, want %q", got, "(no content)")
	}
}

func TestEntryPreview_TextWithANSI(t *testing.T) {
	e := Entry{Content: []ContentBlock{{Type: "text", Text: "\x1b[31mred text\x1b[0m"}}}
	got := EntryPreview(e)
	if got != "red text" {
		t.Errorf("preview = %q, want %q", got, "red text")
	}
}

func TestEntryPreview_TextWithXMLTags(t *testing.T) {
	e := Entry{Content: []ContentBlock{{Type: "text", Text: "<command-name>git status</command-name>"}}}
	got := EntryPreview(e)
	if got != "git status" {
		t.Errorf("preview = %q, want %q", got, "git status")
	}
}

func TestEntryPreview_SkipsEmptyTextBlocks(t *testing.T) {
	e := Entry{Content: []ContentBlock{
		{Type: "text", Text: ""},
		{Type: "text", Text: "   "},
		{Type: "text", Text: "actual content"},
	}}
	got := EntryPreview(e)
	if got != "actual content" {
		t.Errorf("preview = %q, want %q", got, "actual content")
	}
}

func TestToolSummary(t *testing.T) {
	tests := []struct {
		name   string
		entry  Entry
		want   string
	}{
		{"no tools", Entry{Content: []ContentBlock{{Type: "text", Text: "hi"}}}, ""},
		{"one tool", Entry{Content: []ContentBlock{{Type: "tool_use", ToolName: "Read"}}}, "[Read]"},
		{"multiple tools", Entry{Content: []ContentBlock{
			{Type: "text", Text: "I will read"},
			{Type: "tool_use", ToolName: "Read"},
			{Type: "tool_use", ToolName: "Edit"},
		}}, "[Read, Edit]"},
		{"empty entry", Entry{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToolSummary(tt.entry)
			if got != tt.want {
				t.Errorf("ToolSummary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStripXMLTags(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"<command-name>git</command-name>", "git"},
		{"no tags here", "no tags here"},
		{"<a>nested<b>tags</b></a>", "nestedtags"},
		{"", ""},
	}
	for _, tt := range tests {
		got := StripXMLTags(tt.input)
		if got != tt.want {
			t.Errorf("StripXMLTags(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestExtractMetadata(t *testing.T) {
	line := `{"cwd":"/Users/test/project","gitBranch":"main","message":{"role":"user","content":"hi"}}`
	cwd, branch := ExtractMetadata(line)
	if cwd != "/Users/test/project" {
		t.Errorf("cwd = %q, want %q", cwd, "/Users/test/project")
	}
	if branch != "main" {
		t.Errorf("branch = %q, want %q", branch, "main")
	}
}

func TestExtractMetadata_InvalidJSON(t *testing.T) {
	cwd, branch := ExtractMetadata("not json")
	if cwd != "" || branch != "" {
		t.Errorf("expected empty values for invalid JSON, got cwd=%q branch=%q", cwd, branch)
	}
}
