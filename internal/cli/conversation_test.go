package cli

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sendbird/ccx/internal/extract"
	"github.com/sendbird/ccx/internal/session"
)

func TestExtractConversationWithContext(t *testing.T) {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	entries := []session.Entry{
		{Role: "user", UUID: "u1", Timestamp: base, Content: []session.ContentBlock{{Type: "text", Text: "hello"}}},
		{Role: "assistant", UUID: "a1", Timestamp: base.Add(time.Second), Content: []session.ContentBlock{{Type: "text", Text: "hi there"}}},
		{Role: "user", UUID: "u2", Timestamp: base.Add(2 * time.Second), Content: []session.ContentBlock{{Type: "text", Text: "please read file"}}},
		{Role: "assistant", UUID: "a2", Timestamp: base.Add(3 * time.Second), Content: []session.ContentBlock{{Type: "tool_use", ToolName: "Read", ToolInput: `{"file_path":"/tmp/x.go"}`}}},
	}

	items := extractConversationWithContext(entries, "sess-1")
	if len(items) != 4 {
		t.Fatalf("expected 4 conversation items, got %d", len(items))
	}
	if items[0].Item.Category != "conversation" {
		t.Fatalf("expected conversation category, got %q", items[0].Item.Category)
	}
	if items[0].Refs[0].EntryUUID != "u1" {
		t.Fatalf("expected first UUID u1, got %q", items[0].Refs[0].EntryUUID)
	}
	if !strings.Contains(items[3].Item.Label, "[Read]") {
		t.Fatalf("expected tool summary label, got %q", items[3].Item.Label)
	}
}

func TestConversationLabelIncludesDedupedToolSummary(t *testing.T) {
	e := session.Entry{
		Role: "assistant",
		Content: []session.ContentBlock{
			{Type: "tool_use", ToolName: "Read"},
			{Type: "tool_use", ToolName: "Read"},
			{Type: "tool_use", ToolName: "Edit"},
		},
	}
	label := conversationLabel(e, 0)
	if !strings.Contains(label, "[Read×2, Edit]") {
		t.Fatalf("expected deduped tool summary, got %q", label)
	}
}

func TestConversationArtifacts(t *testing.T) {
	entry := session.Entry{
		Content: []session.ContentBlock{
			{Type: "tool_use", ToolName: "Read", ToolInput: `{"file_path":"/tmp/x.go"}`},
			{Type: "tool_use", ToolName: "Edit", ToolInput: `{"file_path":"/tmp/x.go","old_string":"a","new_string":"b"}`},
		},
	}
	items := conversationArtifacts(entry, "sess-1", "/tmp/no-home")
	if len(items) < 2 {
		t.Fatalf("expected conversation artifacts, got %#v", items)
	}
	cats := []string{items[0].Category, items[1].Category}
	if !strings.Contains(strings.Join(cats, ","), "file") || !strings.Contains(strings.Join(cats, ","), "change") {
		t.Fatalf("unexpected artifact categories: %#v", items)
	}
}

func TestConversationArtifactTargets(t *testing.T) {
	m := pickerModel{
		kind:           "conversation",
		cursor:         0,
		artifactCursor: 0,
		items: []PickerItem{{
			ConversationArtifacts: []extract.Item{
				{URL: "https://example.com", Category: "url"},
				{URL: "/tmp/x.go", Category: "file"},
			},
		}},
	}
	gotAll := m.conversationArtifactTargets(false)
	if len(gotAll) != 1 || gotAll[0] != "https://example.com" {
		t.Fatalf("expected focused artifact target, got %v", gotAll)
	}
	gotEditable := m.conversationArtifactTargets(true)
	if len(gotEditable) != 0 {
		t.Fatalf("expected no editable target for focused url artifact, got %v", gotEditable)
	}
}

func TestConversationArtifactTargetsUsesFocusedArtifact(t *testing.T) {
	m := pickerModel{
		kind:           "conversation",
		cursor:         0,
		artifactCursor: 1,
		items: []PickerItem{{
			ConversationArtifacts: []extract.Item{
				{URL: "https://example.com", Category: "url"},
				{URL: "/tmp/x.go", Category: "file"},
			},
		}},
	}
	got := m.conversationArtifactTargets(false)
	if len(got) != 1 || got[0] != "/tmp/x.go" {
		t.Fatalf("expected focused artifact target, got %v", got)
	}
}

func TestConversationArtifactTargetsUsesMultiSelect(t *testing.T) {
	m := pickerModel{
		kind:             "conversation",
		cursor:           0,
		artifactSelected: map[int]bool{0: true, 1: true},
		items: []PickerItem{{
			ConversationArtifacts: []extract.Item{
				{URL: "https://example.com", Category: "url"},
				{URL: "/tmp/x.go", Category: "file"},
			},
		}},
	}
	got := m.conversationArtifactTargets(true)
	if len(got) != 1 || got[0] != "/tmp/x.go" {
		t.Fatalf("expected editable selected artifact only, got %v", got)
	}
}

func TestConversationListText(t *testing.T) {
	e := session.Entry{
		Role:    "assistant",
		Content: []session.ContentBlock{{Type: "text", Text: "line one\nline two\nline three\nline four"}},
	}
	got := conversationListText(e)
	if strings.Count(got, "\n") != 2 {
		t.Fatalf("expected 3 lines max, got %q", got)
	}
}

func TestConversationPreviewModeCycle(t *testing.T) {
	m := newPickerModel("conversation", []PickerItem{{
		Item:             extract.Item{URL: "conversation:1", Label: "#1", Category: "conversation"},
		ConversationText: "hello",
	}})
	if m.previewMode != pickerPreviewConversation {
		t.Fatalf("initial preview mode = %v, want conversation", m.previewMode)
	}
	m.cycleConversationPreviewMode(false)
	if m.previewMode != pickerPreviewArtifacts {
		t.Fatalf("after cycle preview mode = %v, want artifacts", m.previewMode)
	}
	m.cycleConversationPreviewMode(true)
	if m.previewMode != pickerPreviewConversation {
		t.Fatalf("after reverse cycle preview mode = %v, want conversation", m.previewMode)
	}
}

func TestPrintConversation(t *testing.T) {
	items := []PickerItem{{
		Item:      extract.Item{URL: "conversation:1", Label: "#1  USER  12:00:00  hello", Category: "conversation"},
		SessionID: "sess-1",
		Refs:      []ItemRef{{EntryUUID: "uuid-1", Role: "user"}},
	}}

	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w

	err = printConversation(items)
	w.Close()
	os.Stdout = oldStdout
	if err != nil {
		t.Fatalf("printConversation: %v", err)
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "USER ") || !strings.Contains(got, "uuid-1") {
		t.Fatalf("unexpected output: %q", got)
	}
}
