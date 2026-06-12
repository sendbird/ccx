package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

func TestRenderConversationPreviewCacheMatchesColdOutput(t *testing.T) {
	msgs := []mergedMsg{
		{
			entry: session.Entry{
				UUID:      "u1",
				Role:      "user",
				Timestamp: time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC),
				Content: []session.ContentBlock{{
					Type: "text",
					Text: strings.Repeat("hello world ", 8),
				}},
			},
			startIdx: 0,
			endIdx:   0,
		},
		{
			entry: session.Entry{
				UUID:      "u2",
				Role:      "assistant",
				Timestamp: time.Date(2026, 5, 22, 10, 1, 0, 0, time.UTC),
				Content: []session.ContentBlock{
					{Type: "text", Text: "assistant reply"},
					{Type: "tool_use", ToolName: "Bash", ToolInput: `{"command":"go test ./..."}`},
				},
			},
			startIdx: 1,
			endIdx:   1,
		},
	}
	expanded := map[int]bool{1: true}

	cold := renderConversationPreview(msgs, 80, 1, expanded, "reply", nil)
	cache := newSessionRowCache(16)
	cachedCold := renderConversationPreview(msgs, 80, 1, expanded, "reply", cache)
	if cachedCold != cold {
		t.Fatalf("cached cold output mismatch\nwant:\n%s\ngot:\n%s", cold, cachedCold)
	}
	if len(cache.items) != len(msgs) {
		t.Fatalf("expected %d cached rows, got %d", len(msgs), len(cache.items))
	}
	cachedWarm := renderConversationPreview(msgs, 80, 1, expanded, "reply", cache)
	if cachedWarm != cold {
		t.Fatalf("cached warm output mismatch\nwant:\n%s\ngot:\n%s", cold, cachedWarm)
	}
}
