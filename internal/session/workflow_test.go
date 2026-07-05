package session

import (
	"os"
	"path/filepath"
	"testing"
)

// writeWorkflowFixture creates a fake session with a workflows/ dir containing
// the given files, and returns the fake session .jsonl path.
func writeWorkflowFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	sessID := "sess-abc"
	sessFile := filepath.Join(root, sessID+".jsonl")
	if err := os.WriteFile(sessFile, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wfDir := filepath.Join(root, sessID, "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(wfDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return sessFile
}

func TestFindWorkflows_FullShape(t *testing.T) {
	// Mirrors the real full-shape summary: string result, phases[], and a
	// workflowProgress array with phase + agent entries.
	full := `{
	  "runId": "wf_k7sfinal",
	  "workflowName": "k7s-final-polish-review",
	  "summary": "Review remaining k7s gaps",
	  "status": "completed",
	  "agentCount": 4,
	  "durationMs": 294791,
	  "totalTokens": 319215,
	  "totalToolCalls": 62,
	  "defaultModel": "claude-opus-4-8[1m]",
	  "phases": [{"title":"Review","detail":"Parallel reviewers"},{"title":"Synthesize","detail":"Merge"}],
	  "workflowProgress": [
	    {"type":"workflow_phase","index":1,"title":"Review"},
	    {"type":"workflow_agent","index":1,"label":"ux-review","phaseIndex":1,"phaseTitle":"Review","agentId":"a130e698071c6d347","model":"claude-opus-4-8[1m]","state":"done","tokens":130580,"toolCalls":24,"durationMs":174338,"lastToolName":"Read","promptPreview":"Inspect k7s","resultPreview":"P0 — scope"}
	  ],
	  "result": "1. Unblock build/test\n2. Fix sort field"
	}`
	sessFile := writeWorkflowFixture(t, map[string]string{"wf_k7sfinal.json": full})

	if !HasWorkflows(sessFile) {
		t.Fatal("HasWorkflows = false, want true")
	}
	runs, err := FindWorkflows(sessFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	r := runs[0]
	if r.RunID != "wf_k7sfinal" || r.Name != "k7s-final-polish-review" || r.Status != "completed" {
		t.Errorf("bad summary fields: %+v", r)
	}
	if r.AgentCount != 4 || r.TotalTokens != 319215 || r.TotalToolCalls != 62 {
		t.Errorf("bad metrics: %+v", r)
	}
	if len(r.Phases) != 2 || r.Phases[0].Title != "Review" || r.Phases[1].Title != "Synthesize" {
		t.Errorf("bad phases: %+v", r.Phases)
	}
	if len(r.Agents) != 1 {
		t.Fatalf("got %d agents, want 1", len(r.Agents))
	}
	a := r.Agents[0]
	if a.Label != "ux-review" || a.AgentID != "a130e698071c6d347" || a.State != "done" || a.Tokens != 130580 {
		t.Errorf("bad agent: %+v", a)
	}
	if a.PhaseTitle != "Review" {
		t.Errorf("agent phase = %q, want Review", a.PhaseTitle)
	}
	if r.Result == "" || r.Result[:2] != "1." {
		t.Errorf("bad result: %q", r.Result)
	}
}

func TestFindWorkflows_ObjectResult(t *testing.T) {
	// The `result` field is polymorphic: some runs store {"results":[...]}.
	min := `{
	  "runId": "wf_invalid",
	  "workflowName": "k7s-implementation-scout",
	  "result": {"results": ["First guidance", "Second guidance"]}
	}`
	sessFile := writeWorkflowFixture(t, map[string]string{"wf_invalid.json": min})
	runs, err := FindWorkflows(sessFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	want := "First guidance\n\n---\n\nSecond guidance"
	if runs[0].Result != want {
		t.Errorf("object result not flattened:\n got: %q\nwant: %q", runs[0].Result, want)
	}
}

func TestFindWorkflows_None(t *testing.T) {
	root := t.TempDir()
	sessFile := filepath.Join(root, "sess-x.jsonl")
	if err := os.WriteFile(sessFile, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if HasWorkflows(sessFile) {
		t.Error("HasWorkflows = true for session with no workflows dir")
	}
	runs, err := FindWorkflows(sessFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if runs != nil {
		t.Errorf("got %v, want nil", runs)
	}
}

func TestFindSubagents_RecursesIntoWorkflows(t *testing.T) {
	// Workflow-spawned agents live under subagents/workflows/{runId}/ and must be
	// discovered with WorkflowRunID set — the regression this fixes.
	root := t.TempDir()
	sessID := "sess-y"
	sessFile := filepath.Join(root, sessID+".jsonl")
	if err := os.WriteFile(sessFile, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(root, sessID, "subagents")
	// A normal top-level subagent.
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	agentLine := `{"type":"user","message":{"role":"user","content":"hi"},"timestamp":"2026-06-13T06:07:13.948Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(subDir, "agent-aaa111.jsonl"), []byte(agentLine), 0o644); err != nil {
		t.Fatal(err)
	}
	// A workflow-nested agent.
	runDir := filepath.Join(subDir, "workflows", "wf_run1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "agent-b130e698071c6d347.jsonl"), []byte(agentLine), 0o644); err != nil {
		t.Fatal(err)
	}

	agents, err := FindSubagents(sessFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 {
		t.Fatalf("got %d agents, want 2 (1 top-level + 1 workflow)", len(agents))
	}
	var wfAgent *Subagent
	for i := range agents {
		if agents[i].WorkflowRunID != "" {
			wfAgent = &agents[i]
		}
	}
	if wfAgent == nil {
		t.Fatal("no agent had WorkflowRunID set")
	}
	if wfAgent.WorkflowRunID != "wf_run1" {
		t.Errorf("WorkflowRunID = %q, want wf_run1", wfAgent.WorkflowRunID)
	}
	if !hasSubagents(sessFile) {
		t.Error("hasSubagents = false, want true")
	}
}
