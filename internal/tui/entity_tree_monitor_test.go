package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

// TestBuildEntityTree_MonitorSection verifies that Monitor/background-shell jobs
// surface as their own section in the conversation entity tree — previously they
// were invisible (rendered only as inline tool_use blocks).
func TestBuildEntityTree_MonitorSection(t *testing.T) {
	ts := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	merged := []mergedMsg{{
		entry: session.Entry{
			Timestamp: ts,
			Role:      "assistant",
			Content: []session.ContentBlock{
				{Type: "tool_use", ToolName: "Monitor", ID: "toolu_mon_1", ToolInput: `{"description":"watch PR #45 checks","persistent":true,"command":"gh pr checks 45"}`},
			},
		},
	}}
	sess := session.Session{ID: "s1", HasShellJobs: true, HasMonitorJobs: true}

	items := buildEntityTree(sess, merged, nil, nil, nil, nil)

	var header, monRow bool
	for _, it := range items {
		if it.groupTag == "shelljobs" {
			header = true
		}
		if strings.Contains(it.label, "watch PR #45 checks") {
			monRow = true
		}
	}
	if !header {
		t.Error("expected a 'shelljobs' section header in the entity tree")
	}
	if !monRow {
		t.Error("expected the monitor's description to appear as a tree row")
	}
}
