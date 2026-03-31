package tui

import (
	"strings"
	"testing"

	"github.com/sendbird/ccx/internal/session"
)

func TestFormatEditDiff(t *testing.T) {
	input := `{"file_path":"/tmp/test.go","old_string":"foo := 1\nbar := 2","new_string":"foo := 10\nbar := 20\nbaz := 30"}`
	result := formatEditDiff(input, 80)

	if result == "" {
		t.Fatal("expected non-empty diff output")
	}
	if !strings.Contains(result, "test.go") {
		t.Error("expected file path in output")
	}
	if !strings.Contains(result, "@@") {
		t.Error("expected hunk header in output")
	}
	if !strings.Contains(result, "-") {
		t.Error("expected removal lines")
	}
	if !strings.Contains(result, "+") {
		t.Error("expected addition lines")
	}
}

func TestFormatEditDiff_ReplaceAll(t *testing.T) {
	input := `{"file_path":"/tmp/test.go","old_string":"foo","new_string":"bar","replace_all":true}`
	result := formatEditDiff(input, 80)

	if !strings.Contains(result, "replace all") {
		t.Error("expected replace_all note in output")
	}
}

func TestFormatEditDiff_InvalidJSON(t *testing.T) {
	result := formatEditDiff("not json", 80)
	if result != "" {
		t.Error("expected empty string for invalid JSON")
	}
}

func TestFormatEditFolded(t *testing.T) {
	input := `{"file_path":"/tmp/test.go","old_string":"line1\nline2","new_string":"line1\nline2\nline3"}`
	result := formatEditFolded(input)

	if result == "" {
		t.Fatal("expected non-empty folded summary")
	}
	if !strings.Contains(result, "test.go") {
		t.Error("expected file path in folded summary")
	}
	if !strings.Contains(result, "-2") {
		t.Error("expected removal count")
	}
	if !strings.Contains(result, "+3") {
		t.Error("expected addition count")
	}
}

func TestFormatWriteDiff(t *testing.T) {
	input := `{"file_path":"/tmp/new.go","content":"package main\n\nfunc main() {}\n"}`
	result := formatWriteDiff(input, 80)

	if result == "" {
		t.Fatal("expected non-empty write diff output")
	}
	if !strings.Contains(result, "new.go") {
		t.Error("expected file path in output")
	}
	if !strings.Contains(result, "new file") {
		t.Error("expected 'new file' marker")
	}
	if !strings.Contains(result, "+") {
		t.Error("expected addition lines")
	}
}

func TestFormatWriteDiff_LongContent(t *testing.T) {
	// Generate content with > 50 lines
	var lines []string
	for i := 0; i < 60; i++ {
		lines = append(lines, "line content")
	}
	content := strings.Join(lines, "\n")
	input := `{"file_path":"/tmp/big.go","content":"` + strings.ReplaceAll(content, "\n", `\n`) + `"}`
	result := formatWriteDiff(input, 80)

	if !strings.Contains(result, "more lines") {
		t.Error("expected truncation message for long content")
	}
}

func TestFormatWriteFolded(t *testing.T) {
	input := `{"file_path":"/tmp/new.go","content":"line1\nline2\nline3"}`
	result := formatWriteFolded(input)

	if result == "" {
		t.Fatal("expected non-empty folded summary")
	}
	if !strings.Contains(result, "new.go") {
		t.Error("expected file path")
	}
	if !strings.Contains(result, "+3 lines") {
		t.Error("expected line count")
	}
}

func TestToolFoldedSummary(t *testing.T) {
	tests := []struct {
		name     string
		block    session.ContentBlock
		wantNon  bool
	}{
		{
			name:    "Edit tool",
			block:   session.ContentBlock{ToolName: "Edit", ToolInput: `{"file_path":"/tmp/t.go","old_string":"a","new_string":"b"}`},
			wantNon: true,
		},
		{
			name:    "Write tool",
			block:   session.ContentBlock{ToolName: "Write", ToolInput: `{"file_path":"/tmp/t.go","content":"hello"}`},
			wantNon: true,
		},
		{
			name:    "Read tool",
			block:   session.ContentBlock{ToolName: "Read", ToolInput: `{"file_path":"/tmp/t.go"}`},
			wantNon: true,
		},
		{
			name:    "Bash tool",
			block:   session.ContentBlock{ToolName: "Bash", ToolInput: `{"command":"ls"}`},
			wantNon: true,
		},
		{
			name:    "Grep tool",
			block:   session.ContentBlock{ToolName: "Grep", ToolInput: `{"pattern":"TODO","path":"/tmp"}`},
			wantNon: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := toolFoldedSummary(tt.block)
			if tt.wantNon && result == "" {
				t.Error("expected non-empty summary")
			}
			if !tt.wantNon && result != "" {
				t.Errorf("expected empty summary, got %q", result)
			}
		})
	}
}

func TestToolDiffOutput(t *testing.T) {
	tests := []struct {
		name    string
		block   session.ContentBlock
		wantNon bool
	}{
		{
			name:    "Edit tool",
			block:   session.ContentBlock{ToolName: "Edit", ToolInput: `{"file_path":"/tmp/t.go","old_string":"a","new_string":"b"}`},
			wantNon: true,
		},
		{
			name:    "Write tool",
			block:   session.ContentBlock{ToolName: "Write", ToolInput: `{"file_path":"/tmp/t.go","content":"hello"}`},
			wantNon: true,
		},
		{
			name:    "Bash tool",
			block:   session.ContentBlock{ToolName: "Bash", ToolInput: `{"command":"ls"}`},
			wantNon: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := toolDiffOutput(tt.block, 80)
			if tt.wantNon && result == "" {
				t.Error("expected non-empty diff output")
			}
			if !tt.wantNon && result != "" {
				t.Errorf("expected empty diff output, got %q", result)
			}
		})
	}
}

func TestSplitLines(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 1},
		{"single", 1},
		{"a\nb", 2},
		{"a\nb\n", 2}, // trailing newline doesn't add extra line
		{"a\nb\nc", 3},
	}

	for _, tt := range tests {
		got := splitLines(tt.input)
		if len(got) != tt.want {
			t.Errorf("splitLines(%q) = %d lines, want %d", tt.input, len(got), tt.want)
		}
	}
}
