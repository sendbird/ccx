package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

func makeWindowedPreviewMsgs(n int) []mergedMsg {
	now := time.Now()
	msgs := make([]mergedMsg, 0, n)
	for i := 0; i < n; i++ {
		msgs = append(msgs, mergedMsg{
			entry: session.Entry{
				UUID:      fmt.Sprintf("windowed-%03d", i),
				Role:      "assistant",
				Timestamp: now.Add(time.Duration(i) * time.Minute),
				Content: []session.ContentBlock{{
					Type: "text",
					Text: fmt.Sprintf("message %03d %s", i, strings.Repeat("payload ", 6)),
				}},
			},
			startIdx: i,
			endIdx:   i,
		})
	}
	return msgs
}

func TestRenderConversationPreviewWindowedFallsBackForSmallInputs(t *testing.T) {
	msgs := makeWindowedPreviewMsgs(10)
	cache := newSessionRowCache(64)
	full := renderConversationPreview(msgs, 80, 3, nil, "", cache)
	windowed, rendered, localCursor, localExpanded, usedWindow := renderConversationPreviewWindowed(msgs, 80, 3, nil, "", cache)
	if usedWindow {
		t.Fatal("expected small input to skip windowing")
	}
	if windowed != full {
		t.Fatalf("expected fallback output to match full render")
	}
	if len(rendered) != len(msgs) {
		t.Fatalf("expected rendered slice len=%d, got %d", len(msgs), len(rendered))
	}
	if localCursor != 3 {
		t.Fatalf("expected localCursor=3, got %d", localCursor)
	}
	if localExpanded != nil {
		t.Fatalf("expected localExpanded=nil for fallback path")
	}
}

func TestRenderConversationPreviewWindowedSlicesLargeInputs(t *testing.T) {
	msgs := makeWindowedPreviewMsgs(convPreviewWindowThreshold + 40)
	cursor := convPreviewWindowThreshold / 2
	expanded := map[int]bool{cursor: true, cursor + 1: true}
	cache := newSessionRowCache(1024)
	content, rendered, localCursor, localExpanded, usedWindow := renderConversationPreviewWindowed(msgs, 90, cursor, expanded, "", cache)
	if !usedWindow {
		t.Fatal("expected large input to use windowing")
	}
	if len(rendered) >= len(msgs) {
		t.Fatalf("expected rendered window to be smaller than full message set (%d >= %d)", len(rendered), len(msgs))
	}
	if localCursor < 0 || localCursor >= len(rendered) {
		t.Fatalf("localCursor out of range: %d (window size %d)", localCursor, len(rendered))
	}
	if !strings.Contains(content, "earlier messages hidden") {
		t.Fatalf("expected top elision marker in content, got %q", content)
	}
	if !strings.Contains(content, "later messages hidden") {
		t.Fatalf("expected bottom elision marker in content, got %q", content)
	}
	if localExpanded == nil {
		t.Fatal("expected localExpanded to be non-nil in windowed mode")
	}
	if len(localExpanded) == 0 {
		t.Fatal("expected at least one expanded message to be preserved in the local window")
	}
}
