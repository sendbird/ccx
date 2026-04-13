package extract

import (
	"testing"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

func TestBlockChanges(t *testing.T) {
	items := BlockChanges([]session.ContentBlock{
		{Type: "tool_use", ToolName: "Edit", ToolInput: `{"file_path":"/tmp/a.go","old_string":"a\n","new_string":"b\n"}`},
		{Type: "tool_use", ToolName: "Write", ToolInput: `{"file_path":"/tmp/b.go","content":"hello\nworld\n"}`},
		{Type: "tool_use", ToolName: "Edit", ToolInput: `{"file_path":"/tmp/a.go","old_string":"x","new_string":"y"}`},
	})
	if len(items) != 2 {
		t.Fatalf("expected 2 changed files, got %d", len(items))
	}
	if items[0].ChangeCount != 2 && items[1].ChangeCount != 2 {
		t.Fatalf("expected one aggregated item with change count 2, got %#v", items)
	}
}

func TestEntryChangesPreservesTimestamps(t *testing.T) {
	t1 := time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 11, 10, 5, 0, 0, time.UTC)
	entries := []session.Entry{
		{
			Timestamp: t1,
			Role:      "assistant",
			Content: []session.ContentBlock{
				{Type: "tool_use", ToolName: "Edit", ToolInput: `{"file_path":"/tmp/a.go","old_string":"a","new_string":"b"}`},
			},
		},
		{
			Timestamp: t2,
			Role:      "assistant",
			Content: []session.ContentBlock{
				{Type: "tool_use", ToolName: "Write", ToolInput: `{"file_path":"/tmp/b.go","content":"hello\n"}`},
				{Type: "tool_use", ToolName: "Edit", ToolInput: `{"file_path":"/tmp/a.go","old_string":"x","new_string":"y"}`},
			},
		},
	}
	items := EntryChanges(entries)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	// Find the aggregated item for a.go
	var aItem ChangeItem
	for _, item := range items {
		if item.Item.URL == "/tmp/a.go" {
			aItem = item
		}
	}
	if aItem.ChangeCount != 2 {
		t.Errorf("expected ChangeCount=2 for a.go, got %d", aItem.ChangeCount)
	}
	if len(aItem.ToolInputs) != 2 {
		t.Errorf("expected 2 ToolInputs for a.go, got %d", len(aItem.ToolInputs))
	}
	// Timestamp should be the latest (t2)
	if !aItem.Timestamp.Equal(t2) {
		t.Errorf("expected timestamp %v, got %v", t2, aItem.Timestamp)
	}
}

func TestEntryChangesToolInputsStored(t *testing.T) {
	entries := []session.Entry{
		{
			Role: "assistant",
			Content: []session.ContentBlock{
				{Type: "tool_use", ToolName: "Write", ToolInput: `{"file_path":"/tmp/new.go","content":"package main\n"}`},
			},
		},
	}
	items := EntryChanges(entries)
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if len(items[0].ToolNames) != 1 || items[0].ToolNames[0] != "Write" {
		t.Errorf("expected ToolNames=[Write], got %v", items[0].ToolNames)
	}
	if len(items[0].ToolInputs) != 1 {
		t.Errorf("expected 1 ToolInput, got %d", len(items[0].ToolInputs))
	}
}
