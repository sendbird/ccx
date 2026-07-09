package extract

import (
	"testing"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

// TestEntryURLsRecentFirst verifies URLs are stamped with the latest entry they
// appear in and ordered most-recent first.
func TestEntryURLsRecentFirst(t *testing.T) {
	t1 := time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 11, 10, 5, 0, 0, time.UTC)
	t3 := time.Date(2026, 4, 11, 10, 9, 0, 0, time.UTC)
	entries := []session.Entry{
		{Timestamp: t1, Content: []session.ContentBlock{{Text: "see https://github.com/sendbird/ccx/pull/1"}}},
		{Timestamp: t2, Content: []session.ContentBlock{{Text: "and https://github.com/sendbird/ccx/pull/2"}}},
		// pull/1 reappears later — its stored timestamp should refresh to t3,
		// bumping it back to the front.
		{Timestamp: t3, Content: []session.ContentBlock{{Text: "again https://github.com/sendbird/ccx/pull/1"}}},
	}
	items := EntryURLs(entries)
	if len(items) != 2 {
		t.Fatalf("expected 2 unique URLs, got %d: %#v", len(items), items)
	}
	if items[0].URL != "https://github.com/sendbird/ccx/pull/1" {
		t.Errorf("expected pull/1 first (refreshed to t3), got %s", items[0].URL)
	}
	if !items[0].Timestamp.Equal(t3) {
		t.Errorf("expected pull/1 timestamp %v, got %v", t3, items[0].Timestamp)
	}
	if !items[1].Timestamp.Equal(t2) {
		t.Errorf("expected pull/2 timestamp %v, got %v", t2, items[1].Timestamp)
	}
}

// TestSessionFilePathsBlockScopeNoTimestamp guards that single-message BlockURLs
// / BlockFilePaths leave the timestamp zero (no entry context available).
func TestBlockScopeNoTimestamp(t *testing.T) {
	urls := BlockURLs([]session.ContentBlock{{Text: "https://example.com/x"}})
	if len(urls) != 1 || !urls[0].Timestamp.IsZero() {
		t.Errorf("block-scope URL should have zero timestamp, got %#v", urls)
	}
	files := BlockFilePaths([]session.ContentBlock{
		{Type: "tool_use", ToolName: "Edit", ToolInput: `{"file_path":"/tmp/a.go"}`},
	})
	if len(files) != 1 || !files[0].Timestamp.IsZero() {
		t.Errorf("block-scope file should have zero timestamp, got %#v", files)
	}
}

// TestSessionFilePathsRecentFirst verifies file paths carry the latest entry
// timestamp and are ordered most-recent first via the shared sort helper.
func TestFilePathsRecentFirstStamp(t *testing.T) {
	t1 := time.Date(2026, 4, 11, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 11, 10, 5, 0, 0, time.UTC)
	seen := make(map[string]int)
	var items []Item
	extractFilePathsInto([]session.ContentBlock{
		{Type: "tool_use", ToolName: "Edit", ToolInput: `{"file_path":"/tmp/a.go"}`},
	}, t1, FilePathTools, false, seen, &items)
	extractFilePathsInto([]session.ContentBlock{
		{Type: "tool_use", ToolName: "Write", ToolInput: `{"file_path":"/tmp/b.go"}`},
	}, t2, FilePathTools, false, seen, &items)
	// a.go reappears at t2 → its timestamp refreshes to the latest.
	extractFilePathsInto([]session.ContentBlock{
		{Type: "tool_use", ToolName: "Edit", ToolInput: `{"file_path":"/tmp/a.go"}`},
	}, t2, FilePathTools, false, seen, &items)
	sortItemsByTime(items)
	if len(items) != 2 {
		t.Fatalf("expected 2 files, got %d", len(items))
	}
	for _, it := range items {
		if !it.Timestamp.Equal(t2) {
			t.Errorf("%s: expected timestamp %v, got %v", it.URL, t2, it.Timestamp)
		}
	}
}
