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
