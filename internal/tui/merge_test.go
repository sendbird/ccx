package tui

import (
	"testing"
	"time"

	"github.com/keyolk/ccx/internal/session"
)

// --- filterSideQuestionContext ---

func TestFilterSideQuestionContext_CollapsesBulkContext(t *testing.T) {
	base := time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)

	// Simulate a side-question file: 48 context entries + 2 real entries
	entries := make([]session.Entry, 50)
	for i := range 48 {
		role := "user"
		if i%2 == 1 {
			role = "assistant"
		}
		entries[i] = makeTextEntry(role, base.Add(time.Duration(i)*time.Second), "context message")
	}
	entries[48] = makeTextEntry("user", base.Add(48*time.Second), "actual question")
	entries[49] = makeTextEntry("assistant", base.Add(49*time.Second), "actual answer")

	result := filterSideQuestionContext(entries)

	// Should be: 1 summary + 2 real messages
	if len(result) != 3 {
		t.Fatalf("got %d entries, want 3 (summary + question + answer)", len(result))
	}

	// First entry should be the context summary (system_tag)
	if len(result[0].Content) != 1 {
		t.Fatalf("summary entry content blocks = %d, want 1", len(result[0].Content))
	}
	if result[0].Content[0].Type != "system_tag" {
		t.Errorf("summary block type = %q, want system_tag", result[0].Content[0].Type)
	}
	if result[0].Content[0].TagName != "context" {
		t.Errorf("summary TagName = %q, want context", result[0].Content[0].TagName)
	}

	// Second entry should be the actual user question
	if result[1].Content[0].Text != "actual question" {
		t.Errorf("question text = %q, want %q", result[1].Content[0].Text, "actual question")
	}

	// Third entry should be the actual assistant answer
	if result[2].Content[0].Text != "actual answer" {
		t.Errorf("answer text = %q, want %q", result[2].Content[0].Text, "actual answer")
	}
}

func TestFilterSideQuestionContext_TwoEntries(t *testing.T) {
	base := time.Now()
	entries := []session.Entry{
		makeTextEntry("user", base, "question"),
		makeTextEntry("assistant", base.Add(time.Second), "answer"),
	}

	result := filterSideQuestionContext(entries)

	// Only 2 entries — no context to strip, return as-is
	if len(result) != 2 {
		t.Fatalf("got %d entries, want 2 (no context to strip)", len(result))
	}
}

func TestFilterSideQuestionContext_SingleEntry(t *testing.T) {
	entries := []session.Entry{
		makeTextEntry("user", time.Now(), "hello"),
	}
	result := filterSideQuestionContext(entries)
	if len(result) != 1 {
		t.Fatalf("got %d entries, want 1", len(result))
	}
}

func TestFilterSideQuestionContext_Empty(t *testing.T) {
	result := filterSideQuestionContext(nil)
	if len(result) != 0 {
		t.Fatalf("got %d entries, want 0", len(result))
	}
}

func TestFilterSideQuestionContext_ThreeEntries(t *testing.T) {
	base := time.Now()
	entries := []session.Entry{
		makeTextEntry("user", base, "context"),
		makeTextEntry("user", base.Add(time.Second), "actual question"),
		makeTextEntry("assistant", base.Add(2*time.Second), "answer"),
	}

	result := filterSideQuestionContext(entries)

	// 1 context entry before last user → summary + question + answer = 3
	if len(result) != 3 {
		t.Fatalf("got %d entries, want 3", len(result))
	}
	if result[0].Content[0].Type != "system_tag" {
		t.Errorf("summary type = %q, want system_tag", result[0].Content[0].Type)
	}
	if result[1].Content[0].Text != "actual question" {
		t.Errorf("question = %q", result[1].Content[0].Text)
	}
}

func TestFilterSideQuestionContext_AllAssistant(t *testing.T) {
	// Edge case: no user messages at all
	base := time.Now()
	entries := []session.Entry{
		makeTextEntry("assistant", base, "response 1"),
		makeTextEntry("assistant", base.Add(time.Second), "response 2"),
		makeTextEntry("assistant", base.Add(2*time.Second), "response 3"),
	}

	result := filterSideQuestionContext(entries)

	// No user messages → returns as-is (lastUserIdx = -1)
	if len(result) != 3 {
		t.Fatalf("got %d entries, want 3 (no user msg to split on)", len(result))
	}
}

// --- filterAgentContextEntries ---

func TestFilterAgentContextEntries_SkipsContinuation(t *testing.T) {
	base := time.Now()
	entries := []session.Entry{
		makeTextEntry("user", base, "This session is being continued from a previous conversation that ran out of context."),
		makeTextEntry("user", base.Add(time.Second), "actual prompt"),
		makeTextEntry("assistant", base.Add(2*time.Second), "response"),
	}

	result := filterAgentContextEntries(entries)
	if len(result) != 2 {
		t.Fatalf("got %d entries, want 2 (continuation skipped)", len(result))
	}
	if result[0].Content[0].Text != "actual prompt" {
		t.Errorf("first entry = %q, want actual prompt", result[0].Content[0].Text)
	}
}

func TestFilterAgentContextEntries_NoContinuation(t *testing.T) {
	base := time.Now()
	entries := []session.Entry{
		makeTextEntry("user", base, "normal prompt"),
		makeTextEntry("assistant", base.Add(time.Second), "response"),
	}

	result := filterAgentContextEntries(entries)
	if len(result) != 2 {
		t.Fatalf("got %d entries, want 2 (nothing to skip)", len(result))
	}
}

// --- defaultFolds with system_tag ---

func TestDefaultFolds_SystemTagFolded(t *testing.T) {
	entry := session.Entry{
		Role: "user",
		Content: []session.ContentBlock{
			{Type: "system_tag", TagName: "system-reminder", Text: "instructions"},
			{Type: "text", Text: "actual content"},
		},
	}

	folds := defaultFolds(entry)

	if !folds[0] {
		t.Error("system_tag block should be folded by default")
	}
	if folds[1] {
		t.Error("text block should NOT be folded by default")
	}
}

func TestDefaultFolds_MixedTypes(t *testing.T) {
	entry := session.Entry{
		Role: "assistant",
		Content: []session.ContentBlock{
			{Type: "system_tag", TagName: "task-notification", Text: "task info"},
			{Type: "text", Text: "response"},
			{Type: "tool_use", ToolName: "Bash", ToolInput: `{}`},
			{Type: "tool_result", Text: "output"},
			{Type: "thinking", Text: "thinking..."},
			{Type: "text", Text: "final answer"},
		},
	}

	folds := defaultFolds(entry)

	// system_tag, tool_use, tool_result, thinking → folded
	for _, idx := range []int{0, 2, 3, 4} {
		if !folds[idx] {
			t.Errorf("block[%d] (type=%s) should be folded", idx, entry.Content[idx].Type)
		}
	}
	// text → not folded
	for _, idx := range []int{1, 5} {
		if folds[idx] {
			t.Errorf("block[%d] (type=%s) should NOT be folded", idx, entry.Content[idx].Type)
		}
	}
}

// --- mergeConversationTurns with system_tag ---

func TestMerge_SystemTagPreserved(t *testing.T) {
	base := time.Now()
	entries := []session.Entry{
		{
			Role:      "user",
			Timestamp: base,
			Content: []session.ContentBlock{
				{Type: "system_tag", TagName: "system-reminder", Text: "reminder"},
				{Type: "text", Text: "hello"},
			},
		},
		makeTextEntry("assistant", base.Add(time.Second), "hi"),
	}

	merged := mergeConversationTurns(entries)
	if len(merged) != 2 {
		t.Fatalf("got %d merged, want 2", len(merged))
	}

	// First merged entry (user) should preserve both blocks
	userBlocks := merged[0].entry.Content
	if len(userBlocks) != 2 {
		t.Fatalf("user blocks = %d, want 2", len(userBlocks))
	}
	if userBlocks[0].Type != "system_tag" {
		t.Errorf("block[0] type = %q, want system_tag", userBlocks[0].Type)
	}
	if userBlocks[1].Type != "text" {
		t.Errorf("block[1] type = %q, want text", userBlocks[1].Type)
	}
}
