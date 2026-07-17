package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

// --- headline tests ---

func TestBashHeadline(t *testing.T) {
	got := stripANSI(bashHeadline(`{"command":"git log --oneline"}`, 80))
	if got != "$ git log --oneline" {
		t.Errorf("bashHeadline = %q, want %q", got, "$ git log --oneline")
	}
}

func TestBashHeadline_Background(t *testing.T) {
	got := stripANSI(bashHeadline(`{"command":"npm run watch","run_in_background":true}`, 80))
	if !strings.HasSuffix(got, " &") {
		t.Errorf("expected background marker ' &', got %q", got)
	}
}

func TestBashHeadline_MiddleTruncation(t *testing.T) {
	long := strings.Repeat("a", 60) + " MIDDLE " + strings.Repeat("z", 60)
	got := stripANSI(bashHeadline(`{"command":"`+long+`"}`, 40))
	if !strings.Contains(got, "…") {
		t.Errorf("expected middle ellipsis in truncated command, got %q", got)
	}
	// Tail (command arguments) should survive truncation.
	if !strings.Contains(got, "zzz") {
		t.Errorf("expected command tail to survive truncation, got %q", got)
	}
}

func TestReadHeadline(t *testing.T) {
	got := stripANSI(readHeadline(`{"file_path":"/tmp/foo/bar.go"}`, 80))
	if !strings.HasPrefix(got, "Read ") || !strings.Contains(got, "bar.go") {
		t.Errorf("readHeadline = %q", got)
	}
}

func TestReadHeadline_OffsetLimit(t *testing.T) {
	got := stripANSI(readHeadline(`{"file_path":"/tmp/f.go","offset":100,"limit":40}`, 80))
	if !strings.Contains(got, ":100-140") {
		t.Errorf("expected offset-limit range, got %q", got)
	}
}

func TestReadHeadline_KeepsFilename(t *testing.T) {
	longPath := "/very/long/path/" + strings.Repeat("dir/", 20) + "important.go"
	got := stripANSI(readHeadline(`{"file_path":"`+longPath+`"}`, 40))
	if !strings.Contains(got, "important.go") {
		t.Errorf("expected filename to survive middle truncation, got %q", got)
	}
}

func TestEditHeadline(t *testing.T) {
	got := stripANSI(editHeadline(`{"file_path":"/tmp/messages.go","old_string":"a\nb","new_string":"c\nd\ne"}`, 80))
	if !strings.HasPrefix(got, "Edit messages.go") {
		t.Errorf("editHeadline = %q", got)
	}
	if !strings.Contains(got, "-2") || !strings.Contains(got, "+3") {
		t.Errorf("expected diff stat -2/+3, got %q", got)
	}
}

func TestWriteHeadline(t *testing.T) {
	got := stripANSI(writeHeadline(`{"file_path":"/tmp/new.go","content":"l1\nl2\nl3"}`, 80))
	if !strings.Contains(got, "new.go") || !strings.Contains(got, "(3 lines)") {
		t.Errorf("writeHeadline = %q", got)
	}
}

func TestGrepHeadline(t *testing.T) {
	got := stripANSI(grepHeadline(`{"pattern":"TODO","path":"/tmp"}`, 80))
	if !strings.HasPrefix(got, "Grep ") || !strings.Contains(got, "TODO") || !strings.Contains(got, "/tmp") {
		t.Errorf("grepHeadline = %q", got)
	}
}

func TestGlobHeadline(t *testing.T) {
	got := stripANSI(globHeadline(`{"pattern":"**/*.go"}`, 80))
	if !strings.HasPrefix(got, "Glob ") || !strings.Contains(got, "**/*.go") {
		t.Errorf("globHeadline = %q", got)
	}
}

func TestWebFetchHeadline(t *testing.T) {
	got := stripANSI(webFetchHeadline(`{"url":"https://example.com/docs","prompt":"summarize"}`, 80))
	if !strings.Contains(got, "https://example.com/docs") {
		t.Errorf("webFetchHeadline = %q", got)
	}
}

func TestWebSearchHeadline(t *testing.T) {
	got := stripANSI(webSearchHeadline(`{"query":"golang lipgloss tables"}`, 80))
	if !strings.Contains(got, `"golang lipgloss tables"`) {
		t.Errorf("webSearchHeadline = %q", got)
	}
}

func TestAgentHeadline(t *testing.T) {
	got := stripANSI(agentHeadline(`{"subagent_type":"Explore","description":"Find fold logic"}`, 80))
	if !strings.Contains(got, "Agent[Explore]") || !strings.Contains(got, `"Find fold logic"`) {
		t.Errorf("agentHeadline = %q", got)
	}
}

func TestAgentHeadline_PromptFallback(t *testing.T) {
	got := stripANSI(agentHeadline(`{"prompt":"Study the code\nand report back"}`, 80))
	if !strings.Contains(got, "Study the code and report back") {
		t.Errorf("expected prompt head in headline, got %q", got)
	}
}

func TestWorkflowHeadline_MetaName(t *testing.T) {
	script := "export const meta = {\\n  name: 'k7s-review',\\n  description: 'x'\\n}"
	got := stripANSI(workflowHeadline(`{"script":"`+script+`"}`, 80))
	if !strings.Contains(got, "Workflow: k7s-review") {
		t.Errorf("workflowHeadline = %q", got)
	}
}

func TestWorkflowHeadline_DescriptionFallback(t *testing.T) {
	got := stripANSI(workflowHeadline(`{"description":"Review gaps","script":"const x = 1"}`, 80))
	if !strings.Contains(got, "Workflow: Review gaps") {
		t.Errorf("workflowHeadline = %q", got)
	}
}

func TestWorkflowHeadline_Generic(t *testing.T) {
	got := stripANSI(workflowHeadline(`{}`, 80))
	if got != "Workflow" {
		t.Errorf("workflowHeadline = %q, want generic 'Workflow'", got)
	}
}

func TestTaskCreateHeadline(t *testing.T) {
	got := stripANSI(taskCreateHeadline(`{"subject":"Implement renderer"}`, 80))
	if !strings.Contains(got, "TaskCreate") || !strings.Contains(got, `"Implement renderer"`) {
		t.Errorf("taskCreateHeadline = %q", got)
	}
}

func TestTaskUpdateHeadline(t *testing.T) {
	got := stripANSI(taskUpdateHeadline(`{"task_id":"3","status":"completed"}`, 80))
	if !strings.Contains(got, "TaskUpdate") || !strings.Contains(got, "#3") || !strings.Contains(got, "completed") {
		t.Errorf("taskUpdateHeadline = %q", got)
	}
}

func TestSkillHeadline(t *testing.T) {
	got := stripANSI(skillHeadline(`{"skill":"verify"}`, 80))
	if got != "Skill: verify" {
		t.Errorf("skillHeadline = %q, want 'Skill: verify'", got)
	}
}

func TestSkillHeadline_NoName(t *testing.T) {
	if got := skillHeadline(`{}`, 80); got != "" {
		t.Errorf("expected empty headline for skill without name (generic fallback), got %q", got)
	}
}

func TestMonitorHeadline(t *testing.T) {
	got := stripANSI(monitorHeadline(`{"command":"kubectl get pods","description":"watch pods","persistent":true}`, 80))
	if !strings.Contains(got, "Monitor") || !strings.Contains(got, "[persistent]") || !strings.Contains(got, "watch pods") {
		t.Errorf("monitorHeadline = %q", got)
	}
}

func TestMCPRendererHeadline(t *testing.T) {
	r := lookupToolRenderer("mcp__github__get_pull_request")
	if r == nil {
		t.Fatal("expected MCP renderer for mcp__ tool name")
	}
	got := stripANSI(r.headline("{}", 80))
	if !strings.Contains(got, "MCP: github") || !strings.Contains(got, "get_pull_request") {
		t.Errorf("mcp headline = %q", got)
	}
}

func TestLookupToolRenderer_UnknownIsNil(t *testing.T) {
	if r := lookupToolRenderer("SomeUnknownTool"); r != nil {
		t.Error("expected nil renderer for unknown tool (generic fallback)")
	}
}

// --- body tests ---

func TestEditBody_UsesDiff(t *testing.T) {
	lines := editBody(`{"file_path":"/tmp/t.go","old_string":"a","new_string":"b"}`, nil, 80)
	joined := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "@@") {
		t.Errorf("expected unified diff hunk in edit body, got:\n%s", joined)
	}
	if !strings.Contains(joined, "- a") || !strings.Contains(joined, "+ b") {
		t.Errorf("expected -/+ lines in edit body, got:\n%s", joined)
	}
}

func TestWriteBody_ContentPreview(t *testing.T) {
	var content []string
	for range 60 {
		content = append(content, "line")
	}
	input := `{"file_path":"/tmp/big.go","content":"` + strings.Join(content, `\n`) + `"}`
	lines := writeBody(input, nil, 80)
	joined := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "more lines") {
		t.Errorf("expected content preview to be capped, got %d lines", len(lines))
	}
}

func TestBashBody_FullCommand(t *testing.T) {
	lines := bashBody(`{"command":"echo one\necho two","description":"say things"}`, nil, 80)
	joined := stripANSI(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "echo one") || !strings.Contains(joined, "echo two") {
		t.Errorf("expected full multi-line command in body, got:\n%s", joined)
	}
	if !strings.Contains(joined, "say things") {
		t.Errorf("expected description in body, got:\n%s", joined)
	}
}

// --- truncateMiddle ---

func TestTruncateMiddle(t *testing.T) {
	tests := []struct {
		in   string
		maxW int
	}{
		{"short", 80},
		{strings.Repeat("x", 100), 20},
		{"한글이 포함된 아주 아주 아주 긴 문자열입니다 테스트", 20},
	}
	for _, tt := range tests {
		got := truncateMiddle(tt.in, tt.maxW)
		if w := displayWidth(got); w > tt.maxW {
			t.Errorf("truncateMiddle(%q, %d) width=%d exceeds budget", tt.in, tt.maxW, w)
		}
	}
	if got := truncateMiddle("abc", 80); got != "abc" {
		t.Errorf("short string should be unchanged, got %q", got)
	}
}

func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeDisplayWidth(r)
	}
	return w
}

func runeDisplayWidth(r rune) int {
	// mirror runewidth for the test without importing it twice
	switch {
	case r >= 0x1100 && (r <= 0x115F || (r >= 0xAC00 && r <= 0xD7A3) || (r >= 0x4E00 && r <= 0x9FFF)):
		return 2
	default:
		return 1
	}
}

// --- result pairing / summaries ---

func TestFindToolResult_ByID(t *testing.T) {
	content := []session.ContentBlock{
		{Type: "tool_use", ToolName: "Bash", ID: "toolu_1"},
		{Type: "tool_use", ToolName: "Read", ID: "toolu_2"},
		{Type: "tool_result", ID: "toolu_2", Text: "read result"},
		{Type: "tool_result", ID: "toolu_1", Text: "bash result"},
	}
	got := findToolResult(content, 0, "toolu_1")
	if got == nil || got.Text != "bash result" {
		t.Fatalf("expected bash result matched by ID, got %+v", got)
	}
}

func TestFindToolResult_NearestFollowing(t *testing.T) {
	content := []session.ContentBlock{
		{Type: "tool_use", ToolName: "Bash"},
		{Type: "text", Text: "..."},
		{Type: "tool_result", Text: "output"},
	}
	got := findToolResult(content, 0, "")
	if got == nil || got.Text != "output" {
		t.Fatalf("expected nearest following result, got %+v", got)
	}
}

func TestFindToolResult_None(t *testing.T) {
	content := []session.ContentBlock{
		{Type: "tool_use", ToolName: "Bash", ID: "toolu_1"},
	}
	if got := findToolResult(content, 0, "toolu_1"); got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestRenderResultSummary_Success(t *testing.T) {
	block := session.ContentBlock{Type: "tool_result", Text: "line one\nline two\nline three"}
	got := stripANSI(renderResultSummary(block, 80))
	if !strings.Contains(got, iconResultTee) || !strings.Contains(got, iconResultOK) {
		t.Errorf("expected ⎿ ✓ prefix, got %q", got)
	}
	if !strings.Contains(got, "line one") || !strings.Contains(got, "(+2 lines)") {
		t.Errorf("expected first line + count, got %q", got)
	}
	if strings.Count(got, "\n") != 1 {
		t.Errorf("success summary should be one line, got %q", got)
	}
}

func TestRenderResultSummary_ErrorAutoExpands(t *testing.T) {
	text := "cmd failed\ndetail 1\ndetail 2\ndetail 3\ndetail 4\ndetail 5\nfinal error line"
	block := session.ContentBlock{Type: "tool_result", Text: text, IsError: true}
	got := stripANSI(renderResultSummary(block, 80))
	if !strings.Contains(got, iconResultErr) {
		t.Errorf("expected ✗ glyph, got %q", got)
	}
	// First line and last lines visible; middle elided.
	if !strings.Contains(got, "cmd failed") {
		t.Errorf("expected first line, got %q", got)
	}
	if !strings.Contains(got, "final error line") {
		t.Errorf("expected error tail auto-expanded, got %q", got)
	}
	if !strings.Contains(got, "lines …") {
		t.Errorf("expected middle elision marker, got %q", got)
	}
}

func TestRenderResultSummary_Empty(t *testing.T) {
	block := session.ContentBlock{Type: "tool_result", Text: ""}
	got := stripANSI(renderResultSummary(block, 80))
	if !strings.Contains(got, "(no output)") {
		t.Errorf("expected no-output marker, got %q", got)
	}
}

// --- integration: renderFullMessage disclosure ladder ---

func toolEntry() session.Entry {
	return session.Entry{
		Role:      "assistant",
		Timestamp: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC),
		Content: []session.ContentBlock{
			{Type: "tool_use", ToolName: "Bash", ID: "toolu_9",
				ToolInput: `{"command":"go test ./...","description":"run tests"}`},
			{Type: "tool_result", ID: "toolu_9", Text: "ok  pkg 0.1s"},
		},
	}
}

func TestRenderMessage_ToolUseFoldedShowsHeadline(t *testing.T) {
	e := toolEntry()
	folds := defaultFolds(e)
	rp := renderFullMessageWithCursor(e, 80, folds, nil, -1)
	plain := stripANSI(rp.content)
	if !strings.Contains(plain, iconToolUse+" $ go test ./...") {
		t.Errorf("expected ⏺ headline when folded, got:\n%s", plain)
	}
	if strings.Contains(plain, "Tool: Bash") {
		t.Errorf("generic 'Tool: Bash' label should be replaced, got:\n%s", plain)
	}
	// Result attached as one summary line (whitespace runs collapse).
	if !strings.Contains(plain, iconResultTee+" "+iconResultOK+" ok pkg 0.1s") {
		t.Errorf("expected attached ⎿ ✓ result summary, got:\n%s", plain)
	}
}

func TestRenderMessage_ToolUseUnfoldedShowsSemanticBody(t *testing.T) {
	e := toolEntry()
	folds := make(foldSet) // everything unfolded
	rp := renderFullMessageWithCursor(e, 80, folds, nil, -1)
	plain := stripANSI(rp.content)
	if !strings.Contains(plain, "$ go test ./...") {
		t.Errorf("expected full command in body, got:\n%s", plain)
	}
	if !strings.Contains(plain, "# run tests") {
		t.Errorf("expected description in body, got:\n%s", plain)
	}
	// Semantic body, not raw JSON.
	if strings.Contains(plain, `"command"`) {
		t.Errorf("raw JSON should not appear at semantic level, got:\n%s", plain)
	}
}

func TestRenderMessage_ToolUseFormattedShowsRawJSON(t *testing.T) {
	e := toolEntry()
	folds := make(foldSet)
	formats := foldSet{0: true} // deepest disclosure on the tool_use block
	rp := renderFullMessageWithCursor(e, 80, folds, formats, -1)
	plain := stripANSI(rp.content)
	if !strings.Contains(plain, `"command"`) {
		t.Errorf("expected raw JSON at deepest disclosure level, got:\n%s", plain)
	}
}

func TestRenderMessage_ErrorResultAutoExpandsWhenFolded(t *testing.T) {
	e := session.Entry{
		Role:      "assistant",
		Timestamp: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC),
		Content: []session.ContentBlock{
			{Type: "tool_use", ToolName: "Bash", ID: "t1", ToolInput: `{"command":"make"}`},
			{Type: "tool_result", ID: "t1", IsError: true,
				Text: "make: error\nstep 1\nstep 2\nstep 3\nstep 4\nundefined: foo"},
		},
	}
	folds := defaultFolds(e)
	rp := renderFullMessageWithCursor(e, 80, folds, nil, -1)
	plain := stripANSI(rp.content)
	if !strings.Contains(plain, iconResultErr) {
		t.Errorf("expected ✗ on error result, got:\n%s", plain)
	}
	if !strings.Contains(plain, "undefined: foo") {
		t.Errorf("expected error tail visible while folded, got:\n%s", plain)
	}
}

func TestRenderMessage_UnknownToolKeepsGenericFallback(t *testing.T) {
	e := session.Entry{
		Role:      "assistant",
		Timestamp: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC),
		Content: []session.ContentBlock{
			{Type: "tool_use", ToolName: "SomethingNew", ToolInput: `{"x":1}`},
		},
	}
	folds := defaultFolds(e)
	rp := renderFullMessageWithCursor(e, 80, folds, nil, -1)
	plain := stripANSI(rp.content)
	if !strings.Contains(plain, "Tool: SomethingNew") {
		t.Errorf("unknown tools must keep the generic label, got:\n%s", plain)
	}
}

func TestRenderMessage_SkillNoNameFallsBackToGeneric(t *testing.T) {
	// Skill with unparseable input previously rendered "Tool: Skill" — keep that.
	e := session.Entry{
		Role:      "assistant",
		Timestamp: time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC),
		Content: []session.ContentBlock{
			{Type: "tool_use", ToolName: "Skill", ToolInput: `not-json`},
		},
	}
	folds := defaultFolds(e)
	rp := renderFullMessageWithCursor(e, 80, folds, nil, -1)
	plain := stripANSI(rp.content)
	if !strings.Contains(plain, "Tool: Skill") {
		t.Errorf("expected generic fallback for unparseable Skill input, got:\n%s", plain)
	}
}
