package session

import (
	"testing"
	"time"
)

func TestLoadShellJobsFromEntries(t *testing.T) {
	t1 := time.Date(2026, 5, 7, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(2 * time.Minute)
	t3 := t1.Add(5 * time.Minute)
	t4 := t1.Add(7 * time.Minute)
	t5 := t1.Add(8 * time.Minute)

	entries := []Entry{
		{Timestamp: t1, Role: "assistant", Content: []ContentBlock{{
			Type:      "tool_use",
			ToolName:  "Bash",
			ID:        "toolu_bash_1",
			ToolInput: `{"command":"npm run build","description":"build app","run_in_background":true,"timeout":120000}`,
		}}},
		{Timestamp: t2, Role: "assistant", Content: []ContentBlock{{
			Type:      "tool_use",
			ToolName:  "Bash",
			ID:        "toolu_bash_fg",
			ToolInput: `{"command":"ls","description":"list","run_in_background":false}`,
		}}},
		{Timestamp: t3, Role: "assistant", Content: []ContentBlock{{
			Type:      "tool_use",
			ToolName:  "Monitor",
			ID:        "toolu_mon_1",
			ToolInput: `{"command":"while true; do echo .; sleep 60; done","description":"poll secrets","persistent":true,"timeout_ms":300000}`,
		}}},
		{Timestamp: t4, Role: "assistant", Content: []ContentBlock{{
			Type:      "tool_use",
			ToolName:  "BashOutput",
			ToolInput: `{"tool_use_id":"toolu_bash_1"}`,
		}}},
		{Timestamp: t5, Role: "assistant", Content: []ContentBlock{{
			Type:      "tool_use",
			ToolName:  "KillShell",
			ToolInput: `{"tool_use_id":"toolu_bash_1"}`,
		}}},
	}

	jobs := LoadShellJobsFromEntries(entries)
	if len(jobs) != 2 {
		t.Fatalf("expected 2 shell jobs, got %d (%v)", len(jobs), jobs)
	}

	bash := jobs[0]
	if bash.ToolName != "Bash" || bash.Command != "npm run build" || bash.Description != "build app" {
		t.Fatalf("bash job mismatch: %+v", bash)
	}
	if bash.TimeoutMS != 120000 {
		t.Errorf("bash timeout: got %d, want 120000", bash.TimeoutMS)
	}
	if bash.PollCount != 1 {
		t.Errorf("bash poll count: got %d, want 1", bash.PollCount)
	}
	if bash.Status != "killed" {
		t.Errorf("bash status: got %q, want killed", bash.Status)
	}
	if !bash.LastEventAt.Equal(t5) {
		t.Errorf("bash last event: got %v, want %v", bash.LastEventAt, t5)
	}

	mon := jobs[1]
	if mon.ToolName != "Monitor" || !mon.Persistent {
		t.Fatalf("monitor job mismatch: %+v", mon)
	}
	if mon.Status != "running" {
		t.Errorf("monitor status: got %q, want running", mon.Status)
	}
	if mon.TimeoutMS != 300000 {
		t.Errorf("monitor timeout: got %d, want 300000", mon.TimeoutMS)
	}
}

func TestMonitorInputSummary(t *testing.T) {
	// Prefers description.
	desc, persistent, ok := MonitorInputSummary(`{"description":"watch PR #45","persistent":true,"command":"gh pr view 45\nsleep 30"}`)
	if !ok || desc != "watch PR #45" || !persistent {
		t.Errorf("got (%q,%v,%v), want (watch PR #45,true,true)", desc, persistent, ok)
	}
	// Falls back to first line of command when no description.
	desc, _, ok = MonitorInputSummary(`{"command":"tail -f log\ngrep ERROR"}`)
	if !ok || desc != "tail -f log" {
		t.Errorf("command fallback: got %q, want 'tail -f log'", desc)
	}
	// Bad JSON.
	if _, _, ok := MonitorInputSummary(`not json`); ok {
		t.Error("expected ok=false for bad JSON")
	}
}

func TestAwaitingUserInput(t *testing.T) {
	ask := func(id string) Entry {
		return Entry{Role: "assistant", Content: []ContentBlock{{Type: "tool_use", ToolName: "AskUserQuestion", ID: id}}}
	}
	answer := func(id string) Entry {
		return Entry{Role: "user", Content: []ContentBlock{{Type: "tool_result", ID: id}}}
	}
	text := func(s string) Entry {
		return Entry{Role: "assistant", Content: []ContentBlock{{Type: "text", Text: s}}}
	}

	// Unanswered question at the end → awaiting.
	if !AwaitingUserInput([]Entry{text("hi"), ask("q1")}) {
		t.Error("unanswered AskUserQuestion should be awaiting")
	}
	// Answered → not awaiting.
	if AwaitingUserInput([]Entry{ask("q1"), answer("q1")}) {
		t.Error("answered question should not be awaiting")
	}
	// No question at all → not awaiting.
	if AwaitingUserInput([]Entry{text("hi"), text("bye")}) {
		t.Error("no question should not be awaiting")
	}
	// A later unanswered question after an earlier answered one → awaiting.
	if !AwaitingUserInput([]Entry{ask("q1"), answer("q1"), text("more"), ask("q2")}) {
		t.Error("latest unanswered question should be awaiting")
	}
}
