package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTouchHistoryFixture builds a session whose parent transcript writes the
// same memory note twice (at distinct timestamps), a second memory note once,
// a non-memory file (which must be ignored), and two ExitPlanMode plans.
func writeTouchHistoryFixture(t *testing.T) *Session {
	t.Helper()
	root := t.TempDir()
	sessID := "sess-touch"
	sessFile := filepath.Join(root, sessID+".jsonl")

	parent := `{"type":"user","uuid":"u1","timestamp":"2026-07-01T10:00:00Z","message":{"role":"user","content":"remember things"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-07-01T10:00:10Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"m1","name":"Write","input":{"file_path":"/home/u/.claude/projects/enc/memory/kiro.md","content":"first"}}]}}
{"type":"assistant","uuid":"a2","timestamp":"2026-07-01T10:05:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"m2","name":"Edit","input":{"file_path":"/home/u/.claude/projects/enc/memory/kiro.md","old_string":"first","new_string":"second"}}]}}
{"type":"assistant","uuid":"a3","timestamp":"2026-07-01T10:06:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"m3","name":"Write","input":{"file_path":"/home/u/.claude/projects/enc/memory/MEMORY.md","content":"index"}}]}}
{"type":"assistant","uuid":"a4","timestamp":"2026-07-01T10:07:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"c1","name":"Edit","input":{"file_path":"/repo/main.go","old_string":"x","new_string":"y"}}]}}
{"type":"assistant","uuid":"a5","timestamp":"2026-07-01T10:08:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"p1","name":"ExitPlanMode","input":{"plan":"do","planFilePath":"/home/u/.claude/plans/slug-a.md"}}]}}
{"type":"assistant","uuid":"a6","timestamp":"2026-07-01T10:09:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"p2","name":"ExitPlanMode","input":{"plan":"do2","planFilePath":"/home/u/.claude/plans/slug-a.md"}}]}}
`
	if err := os.WriteFile(sessFile, []byte(parent), 0o644); err != nil {
		t.Fatal(err)
	}
	return &Session{ID: sessID, FilePath: sessFile}
}

func TestMemoryTouchHistory(t *testing.T) {
	sess := writeTouchHistoryFixture(t)
	flow, err := BuildSessionFlow(sess)
	if err != nil {
		t.Fatal(err)
	}

	hist := flow.MemoryTouchHistory()

	kiro, ok := hist["kiro.md"]
	if !ok {
		t.Fatalf("kiro.md missing from history: %v", keys(hist))
	}
	if kiro.Count != 2 {
		t.Errorf("kiro.md count = %d, want 2", kiro.Count)
	}
	wantFirst := time.Date(2026, 7, 1, 10, 0, 10, 0, time.UTC)
	wantLast := time.Date(2026, 7, 1, 10, 5, 0, 0, time.UTC)
	if !kiro.First.Equal(wantFirst) {
		t.Errorf("kiro.md first = %s, want %s", kiro.First, wantFirst)
	}
	if !kiro.Last.Equal(wantLast) {
		t.Errorf("kiro.md last = %s, want %s", kiro.Last, wantLast)
	}

	mem, ok := hist["MEMORY.md"]
	if !ok {
		t.Fatalf("MEMORY.md missing from history: %v", keys(hist))
	}
	if mem.Count != 1 {
		t.Errorf("MEMORY.md count = %d, want 1", mem.Count)
	}

	// The non-memory /repo/main.go write must not appear.
	if _, ok := hist["main.go"]; ok {
		t.Errorf("non-memory main.go leaked into memory history")
	}
}

func TestPlanTouchHistory(t *testing.T) {
	sess := writeTouchHistoryFixture(t)
	flow, err := BuildSessionFlow(sess)
	if err != nil {
		t.Fatal(err)
	}

	hist := flow.PlanTouchHistory()
	plan, ok := hist["/home/u/.claude/plans/slug-a.md"]
	if !ok {
		t.Fatalf("plan slug-a.md missing: %v", keys(hist))
	}
	if plan.Count != 2 {
		t.Errorf("plan count = %d, want 2", plan.Count)
	}
	wantFirst := time.Date(2026, 7, 1, 10, 8, 0, 0, time.UTC)
	wantLast := time.Date(2026, 7, 1, 10, 9, 0, 0, time.UTC)
	if !plan.First.Equal(wantFirst) || !plan.Last.Equal(wantLast) {
		t.Errorf("plan window = %s → %s, want %s → %s", plan.First, plan.Last, wantFirst, wantLast)
	}
}

func TestTouchHistoryNilFlow(t *testing.T) {
	var fi *FlowIndex
	if got := fi.MemoryTouchHistory(); len(got) != 0 {
		t.Errorf("nil flow memory history = %v, want empty", got)
	}
	if got := fi.PlanTouchHistory(); len(got) != 0 {
		t.Errorf("nil flow plan history = %v, want empty", got)
	}
}

func keys(m map[string]TouchHistory) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
