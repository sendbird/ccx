package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

func mkEntry(role string, blocks ...session.ContentBlock) session.Entry {
	return session.Entry{Role: role, Timestamp: time.Now(), Content: blocks}
}

func mkTaskCreate(subject string) session.ContentBlock {
	return session.ContentBlock{
		Type:      "tool_use",
		ToolName:  "TaskCreate",
		ToolInput: `{"subject":"` + subject + `"}`,
	}
}

func mkTaskUpdate(taskID, status string) session.ContentBlock {
	return session.ContentBlock{
		Type:      "tool_use",
		ToolName:  "TaskUpdate",
		ToolInput: `{"taskId":"` + taskID + `","status":"` + status + `"}`,
	}
}

// Pending tasks (no in_progress/completed) should resolve to only the
// originating TaskCreate, not a slew of unrelated tasks that share digits.
func TestExtractTaskEntries_PendingTaskMatchesOriginatingCreate(t *testing.T) {
	entries := []session.Entry{
		mkEntry("assistant", mkTaskCreate("Task one")),
		mkEntry("assistant", mkTaskUpdate("1", "in_progress")),
		mkEntry("assistant", mkTaskUpdate("1", "completed")),
		mkEntry("assistant", mkTaskCreate("Task two")),
		mkEntry("assistant", mkTaskUpdate("2", "in_progress")),
		mkEntry("assistant", mkTaskUpdate("2", "completed")),
		mkEntry("assistant", mkTaskCreate("Task three")),
		mkEntry("assistant", mkTaskCreate("Task four pending")),
	}

	got := extractTaskEntries(entries, "4")
	if len(got) != 1 {
		t.Fatalf("expected 1 entry for pending task 4, got %d", len(got))
	}
	if got[0].Content[0].ToolInput != `{"subject":"Task four pending"}` {
		t.Fatalf("expected TaskCreate for task 4, got %q", got[0].Content[0].ToolInput)
	}
}

// taskId "1" must not match "10", "11", "21", etc.
func TestExtractTaskEntries_NoSubstringMatch(t *testing.T) {
	entries := []session.Entry{
		mkEntry("assistant", mkTaskCreate("First")),             // task 1
		mkEntry("assistant", mkTaskUpdate("10", "in_progress")), // unrelated
		mkEntry("assistant", mkTaskUpdate("10", "completed")),
		mkEntry("assistant", mkTaskUpdate("11", "in_progress")),
		mkEntry("assistant", mkTaskUpdate("11", "completed")),
	}

	got := extractTaskEntries(entries, "1")
	for _, e := range got {
		for _, b := range e.Content {
			if b.ToolName == "TaskUpdate" {
				// Any TaskUpdate in the result must have taskId == "1".
				if !strings.Contains(b.ToolInput, `"taskId":"1"`) || strings.Contains(b.ToolInput, `"taskId":"10"`) || strings.Contains(b.ToolInput, `"taskId":"11"`) {
					t.Fatalf("task 1 fallback should not include unrelated TaskUpdate: %q", b.ToolInput)
				}
			}
		}
	}
}
