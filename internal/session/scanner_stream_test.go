package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanSessionStream_FallsBackToJSONLTasksForCompletedSessions(t *testing.T) {
	home := t.TempDir()
	sessionID := "session-1"
	sessDir := filepath.Join(home, "project-dir")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessDir, sessionID+".jsonl")
	jsonl :=
		`{"type":"assistant","timestamp":"2026-05-14T00:00:01Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"task-1","name":"TaskCreate","input":{"id":"1","subject":"Check context","status":"pending"}}]}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-05-14T00:01:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"task-2","name":"TaskUpdate","input":{"taskId":"1","status":"completed"}}]}}` + "\n"
	if err := os.WriteFile(path, []byte(jsonl), 0o644); err != nil {
		t.Fatal(err)
	}

	sess := scanSessionStream(path, time.Now(), home, nil)
	if len(sess.Tasks) != 1 {
		t.Fatalf("expected 1 task reconstructed from JSONL, got %d", len(sess.Tasks))
	}
	if sess.Tasks[0].ID != "1" {
		t.Fatalf("expected task ID 1, got %q", sess.Tasks[0].ID)
	}
	if sess.Tasks[0].Status != "completed" {
		t.Fatalf("expected completed status, got %q", sess.Tasks[0].Status)
	}
	if sess.HasTasks {
		t.Fatalf("expected HasTasks=false because there is no unfinished task")
	}
	if !sess.allWorkCompleted() {
		t.Fatalf("expected allWorkCompleted=true for reconstructed completed task")
	}
	if got := sess.Lifecycle(); got != LifecycleDone {
		t.Fatalf("expected LifecycleDone, got %v", got)
	}
}
