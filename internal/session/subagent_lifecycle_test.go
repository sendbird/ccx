package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func agentByID(agents []Subagent, id string) (Subagent, bool) {
	for _, a := range agents {
		if a.ID == id {
			return a, true
		}
	}
	return Subagent{}, false
}

// TestSubagentLifecycleFromNestedTranscriptSignal verifies that a completion
// signal living only inside a parent agent's transcript (not the root session)
// still ends the child, proving discovery-pass signals are reused without a
// second read of the agent files.
func TestSubagentLifecycleFromNestedTranscriptSignal(t *testing.T) {
	root := t.TempDir()
	sessID := "sess-nested"
	sessFile := filepath.Join(root, sessID+".jsonl")
	// Root only spawns the parent; it has no signal for the child.
	rootTranscript := `{"type":"assistant","uuid":"spawn-parent","timestamp":"2026-07-04T10:00:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"call-parent","name":"Agent","input":{"prompt":"p"}}]}}
`
	if err := os.WriteFile(sessFile, []byte(rootTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(root, sessID, "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The parent transcript is where the child's synchronous completion lands.
	parent := `{"type":"user","agentId":"parent111111111","timestamp":"2026-07-04T10:01:00Z","message":{"role":"user","content":"go"}}
{"type":"assistant","agentId":"parent111111111","timestamp":"2026-07-04T10:01:01Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"call-child","name":"Agent","input":{"prompt":"c"}}]}}
{"type":"user","agentId":"parent111111111","timestamp":"2026-07-04T10:04:00Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-child","content":"done"}]},"toolUseResult":{"agentId":"child222222222","agentType":"general-purpose"}}
`
	child := `{"type":"user","agentId":"child222222222","timestamp":"2026-07-04T10:01:30Z","message":{"role":"user","content":"go"}}
`
	if err := os.WriteFile(filepath.Join(subDir, "agent-parent111111111.jsonl"), []byte(parent), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "agent-child222222222.jsonl"), []byte(child), 0o644); err != nil {
		t.Fatal(err)
	}

	agents, err := FindSubagents(sessFile)
	if err != nil {
		t.Fatal(err)
	}
	childAgent, ok := agentByID(agents, "child222222222")
	if !ok {
		t.Fatal("child agent missing")
	}
	wantEnd := time.Date(2026, 7, 4, 10, 4, 0, 0, time.UTC)
	if !childAgent.EndedAt.Equal(wantEnd) {
		t.Fatalf("child EndedAt = %v, want %v (nested-transcript signal)", childAgent.EndedAt, wantEnd)
	}
}

// TestSubagentLifecycleFromSyncResult verifies that a synchronous agent whose
// parent tool_result carries agentId is marked ended at the result timestamp,
// while an agent that never reported completion stays live (EndedAt zero).
func TestSubagentLifecycleFromSyncResult(t *testing.T) {
	root := t.TempDir()
	sessID := "sess-lifecycle"
	sessFile := filepath.Join(root, sessID+".jsonl")
	rootTranscript := `{"type":"assistant","uuid":"spawn-a","timestamp":"2026-07-04T10:00:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"call-a","name":"Agent","input":{"prompt":"sync"}}]}}
{"type":"user","uuid":"result-a","timestamp":"2026-07-04T10:05:00Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-a","content":"done"}]},"toolUseResult":{"agentId":"sync111111111111","agentType":"general-purpose"}}
{"type":"assistant","uuid":"spawn-b","timestamp":"2026-07-04T10:06:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"call-b","name":"Agent","input":{"prompt":"async","run_in_background":true}}]}}
{"type":"user","uuid":"result-b","timestamp":"2026-07-04T10:06:01Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-b","content":"launched"}]},"toolUseResult":{"agentId":"async22222222222","agentType":"general-purpose","isAsync":true}}
`
	if err := os.WriteFile(sessFile, []byte(rootTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(root, sessID, "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sync := `{"type":"user","agentId":"sync111111111111","timestamp":"2026-07-04T10:00:10Z","message":{"role":"user","content":"go"}}
`
	async := `{"type":"user","agentId":"async22222222222","timestamp":"2026-07-04T10:06:10Z","message":{"role":"user","content":"go"}}
`
	if err := os.WriteFile(filepath.Join(subDir, "agent-sync111111111111.jsonl"), []byte(sync), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "agent-async22222222222.jsonl"), []byte(async), 0o644); err != nil {
		t.Fatal(err)
	}

	agents, err := FindSubagents(sessFile)
	if err != nil {
		t.Fatal(err)
	}
	syncAgent, ok := agentByID(agents, "sync111111111111")
	if !ok {
		t.Fatal("sync agent missing")
	}
	wantEnd := time.Date(2026, 7, 4, 10, 5, 0, 0, time.UTC)
	if !syncAgent.EndedAt.Equal(wantEnd) {
		t.Fatalf("sync agent EndedAt = %v, want %v", syncAgent.EndedAt, wantEnd)
	}
	asyncAgent, ok := agentByID(agents, "async22222222222")
	if !ok {
		t.Fatal("async agent missing")
	}
	if !asyncAgent.EndedAt.IsZero() {
		t.Fatalf("async agent without completion should stay live, got EndedAt=%v", asyncAgent.EndedAt)
	}
}

// TestSubagentLifecycleFromCompletionNotification verifies that a background
// agent is marked ended when a task-notification with a terminal status and its
// task-id lands in the transcript.
func TestSubagentLifecycleFromCompletionNotification(t *testing.T) {
	root := t.TempDir()
	sessID := "sess-notify"
	sessFile := filepath.Join(root, sessID+".jsonl")
	rootTranscript := `{"type":"assistant","uuid":"spawn","timestamp":"2026-07-04T10:00:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"call","name":"Agent","input":{"prompt":"bg","run_in_background":true}}]}}
{"type":"user","uuid":"result","timestamp":"2026-07-04T10:00:01Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call","content":"launched"}]},"toolUseResult":{"agentId":"bg111111111111","agentType":"general-purpose","isAsync":true}}
{"type":"user","uuid":"notify","timestamp":"2026-07-04T10:09:00Z","message":{"role":"user","content":"<task-notification><task-id>bg111111111111</task-id><status>completed</status></task-notification>"}}
`
	if err := os.WriteFile(sessFile, []byte(rootTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(root, sessID, "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	agentLine := `{"type":"user","agentId":"bg111111111111","timestamp":"2026-07-04T10:00:10Z","message":{"role":"user","content":"go"}}
`
	if err := os.WriteFile(filepath.Join(subDir, "agent-bg111111111111.jsonl"), []byte(agentLine), 0o644); err != nil {
		t.Fatal(err)
	}

	agents, err := FindSubagents(sessFile)
	if err != nil {
		t.Fatal(err)
	}
	agent, ok := agentByID(agents, "bg111111111111")
	if !ok {
		t.Fatal("background agent missing")
	}
	wantEnd := time.Date(2026, 7, 4, 10, 9, 0, 0, time.UTC)
	if !agent.EndedAt.Equal(wantEnd) {
		t.Fatalf("background agent EndedAt = %v, want %v", agent.EndedAt, wantEnd)
	}
}
