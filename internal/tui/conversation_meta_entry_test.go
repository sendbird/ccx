package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

func TestMemoryHistoryLine(t *testing.T) {
	first := time.Date(2026, 4, 18, 14, 22, 0, 0, time.UTC)
	last := time.Date(2026, 7, 12, 9, 3, 0, 0, time.UTC)

	tests := []struct {
		name string
		hist session.TouchHistory
		want string
	}{
		{"none", session.TouchHistory{}, ""},
		{"single", session.TouchHistory{First: first, Last: first, Count: 1}, "04-18 14:22"},
		{"window", session.TouchHistory{First: first, Last: last, Count: 3}, "04-18 14:22 → 07-12 09:03 (×3)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := memoryHistoryLine(tt.hist); got != tt.want {
				t.Errorf("memoryHistoryLine = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMemoryListRowShowsNameTypeAndHistory(t *testing.T) {
	note := session.MemoryNote{
		Name:        "kiro-tool-name-and-token-pitfalls",
		FileName:    "kiro-tool-name-and-token-pitfalls.md",
		Type:        "feedback",
		Description: "64-char tool-name limit",
	}
	hist := session.TouchHistory{
		First: time.Date(2026, 4, 18, 14, 22, 0, 0, time.UTC),
		Last:  time.Date(2026, 7, 12, 9, 3, 0, 0, time.UTC),
		Count: 2,
	}
	row := stripANSI(memoryListRow(note, hist))
	for _, want := range []string{"kiro-tool-name-and-token-pitfalls", "[feedback]", "64-char tool-name limit", "04-18 14:22 → 07-12 09:03"} {
		if !strings.Contains(row, want) {
			t.Errorf("row missing %q: %q", want, row)
		}
	}
}

func TestMemoryListRowIndexHasNoTypeTag(t *testing.T) {
	note := session.MemoryNote{Name: "MEMORY", FileName: "MEMORY.md", IsIndex: true}
	row := stripANSI(memoryListRow(note, session.TouchHistory{}))
	if !strings.Contains(row, "MEMORY.md") {
		t.Errorf("index row should show MEMORY.md: %q", row)
	}
	if strings.Contains(row, "[") {
		t.Errorf("index row should not carry a type tag: %q", row)
	}
}

func TestMemoryDrillFallbackWhenFileMissing(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 50)
	app.currentSess.HasMemory = true
	app.conv.sess = app.currentSess
	app.conv.contextItems = buildConvContextItems(app.conv.sess, app.conv.merged, nil)
	app.conv.items = buildConvItems(app.conv.sess, app.conv.merged, nil, nil, nil)
	app.rebuildConversationList(0)
	for i, item := range app.conv.contextItems {
		if item.sessionMeta == "memory" {
			app.selectConvContext(i)
			break
		}
	}
	// Drilling into a note that does not exist on disk must fall back to the
	// file list rather than leaving a dangling drill state.
	app.enterMemoryDrill("does-not-exist.md")
	if app.conv.inspector.MetaDrill != "" {
		t.Fatalf("drill into missing file should reset, got %q", app.conv.inspector.MetaDrill)
	}
}

func TestExitMemoryDrillNoop(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 50)
	if app.exitMemoryDrill() {
		t.Fatal("exitMemoryDrill should be a no-op when not drilled")
	}
	// Set drill directly and confirm exit clears it and reports handled.
	app.conv.inspector.MetaDrill = "kiro.md"
	if !app.exitMemoryDrill() {
		t.Fatal("exitMemoryDrill should report handled while drilled")
	}
	if app.conv.inspector.MetaDrill != "" {
		t.Fatalf("exitMemoryDrill did not clear drill state: %q", app.conv.inspector.MetaDrill)
	}
}

func TestMergedByUUID(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 50)
	// testEntries includes a user turn; find any real UUID to look up.
	if len(app.conv.merged) == 0 {
		t.Skip("no merged turns")
	}
	want := app.conv.merged[0].entry.UUID
	if want == "" {
		t.Skip("first turn has no UUID")
	}
	m, ok := app.mergedByUUID(want)
	if !ok || m.entry.UUID != want {
		t.Fatalf("mergedByUUID(%q) = %q,%v", want, m.entry.UUID, ok)
	}
	if _, ok := app.mergedByUUID("no-such-uuid"); ok {
		t.Fatal("mergedByUUID should miss on unknown UUID")
	}
}

func TestCurrentMetaTargetRespectsCursor(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 50)
	app.conv.inspector.MetaTargets = []metaEntryTarget{
		{blockIdx: -1},
		{kind: metaTargetMemoryFile, fileName: "a.md"},
		{kind: metaTargetDecision, messageUUID: "u9", blockIdx: 2},
	}
	app.conv.split.Folds.Entry = session.Entry{Content: []session.ContentBlock{
		{Type: "text", Text: "h"}, {Type: "text", Text: "a"}, {Type: "text", Text: "b"},
	}}

	app.conv.split.Folds.BlockCursor = 1
	if tgt, ok := app.currentMetaTarget(); !ok || tgt.fileName != "a.md" {
		t.Fatalf("cursor 1 target = %+v ok=%v", tgt, ok)
	}
	app.conv.split.Folds.BlockCursor = 2
	if tgt, ok := app.currentMetaTarget(); !ok || tgt.messageUUID != "u9" {
		t.Fatalf("cursor 2 target = %+v ok=%v", tgt, ok)
	}
	// Out of range → no target.
	app.conv.split.Folds.BlockCursor = 9
	if _, ok := app.currentMetaTarget(); ok {
		t.Fatal("out-of-range cursor should yield no target")
	}
}
