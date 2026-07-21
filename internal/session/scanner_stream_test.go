package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadTodosFromEntriesUsesLatestSnapshot(t *testing.T) {
	entries := []Entry{
		{Content: []ContentBlock{{Type: "tool_use", ToolName: "TodoWrite", ToolInput: `{"todos":[{"content":"old","status":"pending"}]}`}}},
		{Content: []ContentBlock{{Type: "tool_use", ToolName: "TodoWrite", ToolInput: `{"todos":[{"content":"new","status":"in_progress"},{"content":"done","status":"completed"}]}`}}},
	}
	todos := LoadTodosFromEntries(entries)
	if len(todos) != 2 || todos[0].Content != "new" || todos[1].Status != "completed" {
		t.Fatalf("latest todos = %#v", todos)
	}
}

func TestLoadTodoSnapshotFromEntriesPreservesEmptyLatestSnapshot(t *testing.T) {
	entries := []Entry{
		{Content: []ContentBlock{{Type: "tool_use", ToolName: "TodoWrite", ToolInput: `{"todos":[{"content":"old","status":"pending"}]}`}}},
		{Content: []ContentBlock{{Type: "tool_use", ToolName: "TodoWrite", ToolInput: `{"todos":[]}`}}},
	}
	todos, found := LoadTodoSnapshotFromEntries(entries)
	if !found {
		t.Fatal("empty TodoWrite snapshot was treated as absent")
	}
	if len(todos) != 0 {
		t.Fatalf("empty latest snapshot returned %#v", todos)
	}
	if got := LoadTodosFromEntries(entries); len(got) != 0 {
		t.Fatalf("compatibility loader returned %#v", got)
	}
}

func TestLoadTasksFromEntries_ResolvesIDLessCreatesFromResults(t *testing.T) {
	entries := []Entry{
		{Role: "assistant", Content: []ContentBlock{
			{Type: "tool_use", ID: "create-a", ToolName: "TaskCreate", ToolInput: `{"subject":"First","description":"one"}`},
			{Type: "tool_use", ID: "create-b", ToolName: "TaskCreate", ToolInput: `{"subject":"Second","description":"two"}`},
		}},
		{Role: "user", Content: []ContentBlock{{Type: "tool_result", ID: "create-b", Text: "Task #22 created successfully: Second"}}},
		{Role: "user", Content: []ContentBlock{{Type: "tool_result", ID: "create-a", Text: `{"taskId":"11"}`}}},
		{Role: "assistant", Content: []ContentBlock{{Type: "tool_use", ID: "update-a", ToolName: "TaskUpdate", ToolInput: `{"taskId":"11","status":"completed"}`}}},
	}

	tasks := LoadTasksFromEntries(entries)
	if len(tasks) != 2 {
		t.Fatalf("tasks = %#v, want two", tasks)
	}
	if tasks[0].ID != "11" || tasks[0].Subject != "First" || tasks[0].Status != "completed" {
		t.Fatalf("first task = %#v", tasks[0])
	}
	if tasks[1].ID != "22" || tasks[1].Subject != "Second" {
		t.Fatalf("second task = %#v", tasks[1])
	}
}

func TestLoadTasksFromEntries_KeepsDistinctLegacyCreates(t *testing.T) {
	entries := []Entry{{Content: []ContentBlock{
		{Type: "tool_use", ID: "legacy-a", ToolName: "TaskCreate", ToolInput: `{"subject":"First"}`},
		{Type: "tool_use", ID: "legacy-b", ToolName: "TaskCreate", ToolInput: `{"subject":"Second"}`},
	}}}
	tasks := LoadTasksFromEntries(entries)
	if len(tasks) != 2 || tasks[0].ID == tasks[1].ID {
		t.Fatalf("legacy creates collapsed: %#v", tasks)
	}
}

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
