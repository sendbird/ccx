package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sendbird/ccx/internal/session"
)

func TestUnifiedFlow_MonitorLifecycleRow(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s1.jsonl")
	line := `{"type":"assistant","uuid":"a1","timestamp":"2026-06-01T12:00:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_mon_1","name":"Monitor","input":{"description":"watch PR #45 checks","persistent":true,"command":"gh pr checks 45"}}]}}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	sess := session.Session{ID: "s1", FilePath: path, HasShellJobs: true, HasMonitorJobs: true}
	flow, err := session.BuildSessionFlow(&sess)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := session.LoadMessages(path)
	if err != nil {
		t.Fatal(err)
	}
	merged := filterConversation(mergeConversationTurns(entries))
	items := buildConvItems(sess, merged, nil, nil, nil, flow)

	for _, item := range items {
		if item.kind == convShell && item.shell.ID == "toolu_mon_1" {
			if item.parentIdx < 0 {
				t.Fatal("monitor missing exact origin")
			}
			return
		}
	}
	t.Fatal("expected Monitor lifecycle row in unified flow")
}
