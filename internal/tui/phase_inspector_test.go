package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sendbird/ccx/internal/session"
)

func TestRenderPhaseInspectorUsesPhaseSubtree(t *testing.T) {
	root := t.TempDir()
	sessID := "phase-overview"
	sessPath := filepath.Join(root, sessID+".jsonl")
	parent := `{"type":"assistant","uuid":"spawn","timestamp":"2026-07-03T10:00:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"workflow-call","name":"Workflow","input":{"script":"test"}}]}}
{"type":"user","uuid":"result","timestamp":"2026-07-03T10:00:01Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"workflow-call","content":"launched"}]},"toolUseResult":{"runId":"run-phases"}}
`
	if err := os.WriteFile(sessPath, []byte(parent), 0o644); err != nil {
		t.Fatal(err)
	}

	agentDir := filepath.Join(root, sessID, "subagents", "workflows", "run-phases")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	phaseOne := `{"type":"user","uuid":"p1-u","timestamp":"2026-07-03T10:01:00Z","message":{"role":"user","content":"scan"}}
{"type":"assistant","uuid":"p1-a","timestamp":"2026-07-03T10:01:01Z","message":{"role":"assistant","model":"claude-test","content":[{"type":"tool_use","id":"p1-edit","name":"Edit","input":{"file_path":"/repo/scan.go","old_string":"a","new_string":"b"}}],"usage":{"output_tokens":13}}}
`
	phaseTwo := `{"type":"user","uuid":"p2-u","timestamp":"2026-07-03T10:02:00Z","message":{"role":"user","content":"verify"}}
{"type":"assistant","uuid":"p2-a","timestamp":"2026-07-03T10:02:01Z","message":{"role":"assistant","model":"claude-test","content":[{"type":"tool_use","id":"p2-write","name":"Write","input":{"file_path":"/repo/verify.go","content":"ok"}}],"usage":{"output_tokens":29}}}
`
	if err := os.WriteFile(filepath.Join(agentDir, "agent-phaseone111111.jsonl"), []byte(phaseOne), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "agent-phasetwo222222.jsonl"), []byte(phaseTwo), 0o644); err != nil {
		t.Fatal(err)
	}

	workflowDir := filepath.Join(root, sessID, "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	summary := `{
  "runId":"run-phases",
  "workflowName":"phase-test",
  "status":"completed",
  "phases":[{"title":"Scan","detail":"find sites"},{"title":"Verify","detail":"check fixes"}],
  "workflowProgress":[
    {"type":"workflow_agent","label":"scanner","phaseIndex":1,"phaseTitle":"Scan","agentId":"phaseone111111","state":"done","tokens":130,"toolCalls":1},
    {"type":"workflow_agent","label":"verifier","phaseIndex":2,"phaseTitle":"Verify","agentId":"phasetwo222222","state":"done","tokens":290,"toolCalls":1}
  ]
}`
	if err := os.WriteFile(filepath.Join(workflowDir, "run-phases.json"), []byte(summary), 0o644); err != nil {
		t.Fatal(err)
	}

	flow, err := session.BuildSessionFlow(&session.Session{ID: sessID, FilePath: sessPath})
	if err != nil {
		t.Fatal(err)
	}
	runs := flow.Workflows()
	if len(runs) != 1 || len(runs[0].Phases) != 2 {
		t.Fatalf("workflow = %#v", runs)
	}

	scan := renderPhaseInspector(runs[0], runs[0].Phases[0], flow, session.FlowPhaseNodeID("run-phases", 1), session.ScopeNode)
	verify := renderPhaseInspector(runs[0], runs[0].Phases[1], flow, session.FlowPhaseNodeID("run-phases", 2), session.ScopeNode)
	for _, want := range []string{"Agents: 1", "Tokens: 13 exact", "Range: 2026-07-03T10:01:00Z → 2026-07-03T10:01:01Z", "scanner"} {
		if !strings.Contains(scan, want) {
			t.Fatalf("scan overview missing %q: %q", want, scan)
		}
	}
	if strings.Contains(scan, "verifier") || strings.Contains(scan, "29 exact") {
		t.Fatalf("scan overview leaked verify phase: %q", scan)
	}
	for _, want := range []string{"Agents: 1", "Tokens: 29 exact", "Range: 2026-07-03T10:02:00Z → 2026-07-03T10:02:01Z", "verifier"} {
		if !strings.Contains(verify, want) {
			t.Fatalf("verify overview missing %q: %q", want, verify)
		}
	}
	if strings.Contains(verify, "scanner") || strings.Contains(verify, "13 exact") {
		t.Fatalf("verify overview leaked scan phase: %q", verify)
	}

	// A phase node has children (its assigned agents) but scopeNodeIDs
	// auto-expands ScopeNode to the phase subtree, so Node and Subtree resolve
	// identically — the scope selector collapses to [Node] only. Session is never
	// offered on an individual node; it belongs to the pinned Session Flow row.
	app := newTestApp([]session.Session{{ID: sessID, FilePath: sessPath}})
	app.conv.flow = flow
	phaseNode := session.FlowPhaseNodeID("run-phases", 1)
	if scopes := app.inspectorScopesFor(phaseNode); len(scopes) != 1 || scopes[0] != session.ScopeNode {
		t.Fatalf("phase node scopes = %v, want [Node]", scopes)
	}
	// The workflow node is a non-phase parent with children → Node + Subtree.
	wfNode := session.FlowWorkflowNodeID("run-phases")
	if scopes := app.inspectorScopesFor(wfNode); len(scopes) != 2 ||
		scopes[0] != session.ScopeNode || scopes[1] != session.ScopeSubtree {
		t.Fatalf("workflow node scopes = %v, want [Node Subtree]", scopes)
	}
	// The session root is the whole session, so it is the one node that keeps
	// Session (and only Session).
	if scopes := app.inspectorScopesFor(flow.RootID); len(scopes) != 1 || scopes[0] != session.ScopeSession {
		t.Fatalf("root node scopes = %v, want [Session]", scopes)
	}
}
