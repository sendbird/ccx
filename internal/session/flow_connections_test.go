package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildSessionFlow_SessionWideConnectionsAndPhaseScopes(t *testing.T) {
	root := t.TempDir()
	sessID := "connected-flow"
	sessPath := filepath.Join(root, sessID+".jsonl")
	rootTranscript := `{"type":"assistant","uuid":"root-spawn","timestamp":"2026-07-02T10:00:00Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"spawn-parent","name":"Agent","input":{"prompt":"parent"}}]}}
{"type":"user","uuid":"root-result","timestamp":"2026-07-02T10:00:01Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"spawn-parent","content":"done"}]},"toolUseResult":{"agentId":"parent1111111111","agentType":"general-purpose"}}
`
	if err := os.WriteFile(sessPath, []byte(rootTranscript), 0o644); err != nil {
		t.Fatal(err)
	}

	subDir := filepath.Join(root, sessID, "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	parentPath := filepath.Join(subDir, "agent-parent1111111111.jsonl")
	parentTranscript := `{"type":"user","uuid":"parent-u","agentId":"parent1111111111","timestamp":"2026-07-02T10:00:02Z","message":{"role":"user","content":"coordinate"}}
{"type":"assistant","uuid":"parent-spawn-child","agentId":"parent1111111111","timestamp":"2026-07-02T10:00:03Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"spawn-child","name":"Agent","input":{"prompt":"child"}}]}}
{"type":"user","uuid":"parent-child-result","agentId":"parent1111111111","timestamp":"2026-07-02T10:00:04Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"spawn-child","content":"done"}]},"toolUseResult":{"agentId":"child2222222222","agentType":"general-purpose"}}
{"type":"assistant","uuid":"parent-spawn-workflow","agentId":"parent1111111111","timestamp":"2026-07-02T10:00:05Z","message":{"role":"assistant","content":[{"type":"tool_use","id":"spawn-workflow","name":"Workflow","input":{"script":"test"}}]}}
{"type":"user","uuid":"parent-workflow-result","agentId":"parent1111111111","timestamp":"2026-07-02T10:00:06Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"spawn-workflow","content":"launched"}]},"toolUseResult":{"runId":"run-connected"}}
`
	if err := os.WriteFile(parentPath, []byte(parentTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(subDir, "agent-child2222222222.jsonl")
	childTranscript := `{"type":"user","uuid":"child-u","agentId":"child2222222222","timestamp":"2026-07-02T10:00:07Z","message":{"role":"user","content":"child work"}}
{"type":"assistant","uuid":"child-a","agentId":"child2222222222","timestamp":"2026-07-02T10:00:08Z","message":{"role":"assistant","model":"claude-test","content":[{"type":"tool_use","id":"child-edit","name":"Edit","input":{"file_path":"/repo/child.go","old_string":"a","new_string":"b"}}],"usage":{"output_tokens":7}}}
`
	if err := os.WriteFile(childPath, []byte(childTranscript), 0o644); err != nil {
		t.Fatal(err)
	}

	runAgentDir := filepath.Join(subDir, "workflows", "run-connected")
	if err := os.MkdirAll(runAgentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	phaseOneTranscript := `{"type":"user","uuid":"scan-u","agentId":"scan333333333333","timestamp":"2026-07-02T10:01:00Z","message":{"role":"user","content":"scan"}}
{"type":"assistant","uuid":"scan-a","agentId":"scan333333333333","timestamp":"2026-07-02T10:01:01Z","message":{"role":"assistant","model":"claude-test","content":[{"type":"tool_use","id":"scan-edit","name":"Edit","input":{"file_path":"/repo/scan.go","old_string":"a","new_string":"b"}}],"usage":{"output_tokens":11}}}
`
	phaseTwoTranscript := `{"type":"user","uuid":"verify-u","agentId":"verify4444444444","timestamp":"2026-07-02T10:02:00Z","message":{"role":"user","content":"verify"}}
{"type":"assistant","uuid":"verify-a","agentId":"verify4444444444","timestamp":"2026-07-02T10:02:01Z","message":{"role":"assistant","model":"claude-test","content":[{"type":"tool_use","id":"verify-write","name":"Write","input":{"file_path":"/repo/verify.go","content":"ok"}}],"usage":{"output_tokens":22}}}
`
	if err := os.WriteFile(filepath.Join(runAgentDir, "agent-scan333333333333.jsonl"), []byte(phaseOneTranscript), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runAgentDir, "agent-verify4444444444.jsonl"), []byte(phaseTwoTranscript), 0o644); err != nil {
		t.Fatal(err)
	}

	workflowDir := filepath.Join(root, sessID, "workflows")
	if err := os.MkdirAll(workflowDir, 0o755); err != nil {
		t.Fatal(err)
	}
	summary := `{
  "runId":"run-connected",
  "workflowName":"connected",
  "status":"completed",
  "phases":[{"title":"Scan","detail":"find sites"},{"title":"Verify","detail":"check fixes"}],
  "workflowProgress":[
    {"type":"workflow_agent","label":"scanner","phaseIndex":1,"phaseTitle":"Scan","agentId":"scan333333333333","state":"done","tokens":111,"toolCalls":1},
    {"type":"workflow_agent","label":"verifier","phaseIndex":2,"phaseTitle":"Verify","agentId":"verify4444444444","state":"done","tokens":222,"toolCalls":1}
  ]
}`
	if err := os.WriteFile(filepath.Join(workflowDir, "run-connected.json"), []byte(summary), 0o644); err != nil {
		t.Fatal(err)
	}

	flow, err := BuildSessionFlow(&Session{ID: sessID, FilePath: sessPath})
	if err != nil {
		t.Fatal(err)
	}

	parent, ok := flow.Node(FlowAgentNodeID("parent1111111111"))
	if !ok || parent.ParentID != FlowTurnNodeID("root-spawn") {
		t.Fatalf("parent node = %#v", parent)
	}
	child, ok := flow.Node(FlowAgentNodeID("child2222222222"))
	if !ok || child.ParentID != parent.ID {
		t.Fatalf("child node = %#v, want parent %q", child, parent.ID)
	}
	if child.Origin.ToolUseID != "spawn-child" || child.Origin.Transcript != parentPath {
		t.Fatalf("child origin = %#v", child.Origin)
	}

	workflow, ok := flow.Node(FlowWorkflowNodeID("run-connected"))
	if !ok || workflow.ParentID != parent.ID {
		t.Fatalf("workflow node = %#v, want parent %q", workflow, parent.ID)
	}
	if workflow.Origin.ToolUseID != "spawn-workflow" || workflow.Origin.Transcript != parentPath {
		t.Fatalf("workflow origin = %#v", workflow.Origin)
	}

	phaseOneID := FlowPhaseNodeID("run-connected", 1)
	phaseTwoID := FlowPhaseNodeID("run-connected", 2)
	phaseOne, _ := flow.Node(phaseOneID)
	phaseTwo, _ := flow.Node(phaseTwoID)
	if !phaseOne.Origin.Timestamp.IsZero() || !phaseTwo.Origin.Timestamp.IsZero() {
		t.Fatalf("phase grouping nodes inherited workflow launch origin: %#v %#v", phaseOne.Origin, phaseTwo.Origin)
	}
	if got := flow.Facets(phaseOneID, ScopeNode); got.Counts[ArtifactChange] != 1 || got.Counts[ArtifactFile] != 1 {
		t.Fatalf("phase one facets = %#v", got)
	}
	if got := flow.Facets(phaseTwoID, ScopeNode); got.Counts[ArtifactChange] != 1 || got.Counts[ArtifactFile] != 1 {
		t.Fatalf("phase two facets = %#v", got)
	}
	if got := flow.Stats(phaseOneID, ScopeNode); got.TotalOutputTokens != 11 || got.FirstTimestamp.IsZero() {
		t.Fatalf("phase one stats = %#v", got)
	}
	if got := flow.Stats(phaseTwoID, ScopeNode); got.TotalOutputTokens != 22 || got.FirstTimestamp.IsZero() {
		t.Fatalf("phase two stats = %#v", got)
	}
	phaseOneFiles := flow.Artifacts(phaseOneID, ArtifactFile, ScopeNode)
	phaseTwoFiles := flow.Artifacts(phaseTwoID, ArtifactFile, ScopeNode)
	if len(phaseOneFiles) != 1 || phaseOneFiles[0].Key != "/repo/scan.go" {
		t.Fatalf("phase one files = %#v", phaseOneFiles)
	}
	if len(phaseTwoFiles) != 1 || phaseTwoFiles[0].Key != "/repo/verify.go" {
		t.Fatalf("phase two files = %#v", phaseTwoFiles)
	}
}
