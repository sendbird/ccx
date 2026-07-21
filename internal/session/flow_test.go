package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFlowFixture builds a fake session on disk:
//
//	root/sess-flow.jsonl                                  parent transcript
//	root/sess-flow/subagents/agent-aaaa1111bbbb2222.jsonl ordinary agent
//	root/sess-flow/subagents/workflows/wf_run1/agent-bbbb2222cccc3333.jsonl
//	root/sess-flow/workflows/wf_run1.json                 run summary
//
// The parent transcript wires exact edges: an Agent tool_use whose tool_result
// carries agentId, a Workflow tool_use whose result records runId, plus
// TaskCreate / Edit / Write / ExitPlanMode / error blocks for artifact
// extraction. The workflow summary lists one transcribed agent and one
// summary-only agent ("ghost").
func writeFlowFixture(t *testing.T) *Session {
	t.Helper()
	root := t.TempDir()
	sessID := "sess-flow"
	sessFile := filepath.Join(root, sessID+".jsonl")

	parent := `{"type":"user","uuid":"u1","timestamp":"2026-06-01T10:00:00Z","message":{"role":"user","content":"Please fix the bug in main.go"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-06-01T10:00:10Z","message":{"role":"assistant","model":"claude-x","content":[{"type":"tool_use","id":"toolu_task1","name":"TaskCreate","input":{"subject":"Fix bug","status":"pending"}}],"usage":{"input_tokens":5,"output_tokens":10}}}
{"type":"assistant","uuid":"a2","timestamp":"2026-06-01T10:00:20Z","message":{"role":"assistant","model":"claude-x","content":[{"type":"tool_use","id":"toolu_ag1","name":"Agent","input":{"subagent_type":"Explore","prompt":"look"}}]}}
{"type":"user","uuid":"r1","timestamp":"2026-06-01T10:05:00Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_ag1","content":"agent done"}]},"toolUseResult":{"agentId":"aaaa1111bbbb2222","agentType":"Explore"}}
{"type":"assistant","uuid":"a3","timestamp":"2026-06-01T10:06:00Z","message":{"role":"assistant","model":"claude-x","content":[{"type":"tool_use","id":"toolu_e1","name":"Edit","input":{"file_path":"/x/main.go","old_string":"a","new_string":"b"}},{"type":"tool_use","id":"toolu_e2","name":"Edit","input":{"file_path":"/x/main.go","old_string":"c","new_string":"d"}},{"type":"tool_use","id":"toolu_w1","name":"Write","input":{"file_path":"/x/other.go","content":"hi"}}],"usage":{"input_tokens":5,"output_tokens":20}}}
{"type":"assistant","uuid":"a4","timestamp":"2026-06-01T10:07:00Z","message":{"role":"assistant","model":"claude-x","content":[{"type":"tool_use","id":"toolu_wf1","name":"Workflow","input":{"script":"export const meta = {}"}}]}}
{"type":"user","uuid":"r2","timestamp":"2026-06-01T10:07:05Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_wf1","content":"Workflow launched"}]},"toolUseResult":{"status":"async_launched","runId":"wf_run1"}}
{"type":"assistant","uuid":"a5","timestamp":"2026-06-01T10:08:00Z","message":{"role":"assistant","model":"claude-x","content":[{"type":"text","text":"PR: https://github.com/o/r/pull/12"},{"type":"tool_use","id":"toolu_pl1","name":"ExitPlanMode","input":{"plan":"do things","planFilePath":"/home/u/.claude/plans/fancy-slug.md"}}]}}
{"type":"user","uuid":"r3","timestamp":"2026-06-01T10:08:05Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_pl1","content":"nope","is_error":true}]}}
`
	if err := os.WriteFile(sessFile, []byte(parent), 0o644); err != nil {
		t.Fatal(err)
	}

	subDir := filepath.Join(root, sessID, "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	agent := `{"type":"user","uuid":"ag_u1","timestamp":"2026-06-01T10:00:30Z","message":{"role":"user","content":"Investigate the bug"}}
{"type":"user","uuid":"ag_u2","timestamp":"2026-06-01T10:00:40Z","imagePasteIds":[7],"message":{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"x"}}]}}
{"type":"assistant","uuid":"ag_a1","timestamp":"2026-06-01T10:01:00Z","message":{"role":"assistant","model":"claude-x","content":[{"type":"tool_use","id":"toolu_agE","name":"Edit","input":{"file_path":"/x/agent.go","old_string":"q","new_string":"r"}}],"usage":{"input_tokens":2,"output_tokens":30}}}
`
	if err := os.WriteFile(filepath.Join(subDir, "agent-aaaa1111bbbb2222.jsonl"), []byte(agent), 0o644); err != nil {
		t.Fatal(err)
	}

	runDir := filepath.Join(subDir, "workflows", "wf_run1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wfAgent := `{"type":"user","uuid":"wf_u1","timestamp":"2026-06-01T10:07:10Z","message":{"role":"user","content":"scan things"}}
{"type":"assistant","uuid":"wf_a1","timestamp":"2026-06-01T10:07:30Z","message":{"role":"assistant","model":"claude-x","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":1,"output_tokens":40}}}
`
	if err := os.WriteFile(filepath.Join(runDir, "agent-bbbb2222cccc3333.jsonl"), []byte(wfAgent), 0o644); err != nil {
		t.Fatal(err)
	}

	wfDir := filepath.Join(root, sessID, "workflows")
	if err := os.MkdirAll(wfDir, 0o755); err != nil {
		t.Fatal(err)
	}
	summary := `{
	  "runId": "wf_run1",
	  "workflowName": "scout-wf",
	  "status": "completed",
	  "phases": [{"title":"Scan","detail":""}],
	  "workflowProgress": [
	    {"type":"workflow_agent","index":1,"label":"scout","phaseIndex":1,"phaseTitle":"Scan","agentId":"bbbb2222cccc3333","state":"done","tokens":500,"toolCalls":3,"durationMs":1000},
	    {"type":"workflow_agent","index":2,"label":"ghost","phaseIndex":1,"phaseTitle":"Scan","agentId":"cccc3333dddd4444","state":"done","tokens":700,"toolCalls":5,"durationMs":2000}
	  ]
	}`
	if err := os.WriteFile(filepath.Join(wfDir, "wf_run1.json"), []byte(summary), 0o644); err != nil {
		t.Fatal(err)
	}

	return &Session{ID: sessID, FilePath: sessFile}
}

func TestAttachSpawnOrigins_ExactEdge(t *testing.T) {
	sess := writeFlowFixture(t)
	entries, err := LoadMessages(sess.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	agents, err := FindSubagents(sess.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	AttachSpawnOrigins(agents, entries)

	var got *Subagent
	for i := range agents {
		if agents[i].ID == "aaaa1111bbbb2222" {
			got = &agents[i]
		}
	}
	if got == nil {
		t.Fatal("agent aaaa1111bbbb2222 not discovered")
	}
	if got.SpawnToolUseID != "toolu_ag1" {
		t.Errorf("SpawnToolUseID = %q, want toolu_ag1", got.SpawnToolUseID)
	}
	if got.OriginMessageUUID != "a2" {
		t.Errorf("OriginMessageUUID = %q, want a2", got.OriginMessageUUID)
	}
	if got.OriginEntryIndex != 2 {
		t.Errorf("OriginEntryIndex = %d, want 2", got.OriginEntryIndex)
	}

	// The workflow agent has no toolUseResult edge in the parent — untouched.
	for _, a := range agents {
		if a.ID == "bbbb2222cccc3333" && a.SpawnToolUseID != "" {
			t.Errorf("workflow agent got a spawn edge %q, want none", a.SpawnToolUseID)
		}
	}
}

func TestBuildSessionFlow_Structure(t *testing.T) {
	sess := writeFlowFixture(t)
	fi, err := BuildSessionFlow(sess)
	if err != nil {
		t.Fatal(err)
	}

	root, ok := fi.Node(fi.RootID)
	if !ok || root.Kind != FlowNodeSession {
		t.Fatalf("missing session root: %+v", root)
	}

	// Agent hangs off the exact spawn turn (a2), not a timestamp guess.
	ag, ok := fi.Node("agent:aaaa1111bbbb2222")
	if !ok {
		t.Fatal("agent node missing")
	}
	if ag.ParentID != "turn:a2" {
		t.Errorf("agent parent = %q, want turn:a2", ag.ParentID)
	}
	if ag.Origin.ToolUseID != "toolu_ag1" {
		t.Errorf("agent origin tool_use = %q, want toolu_ag1", ag.Origin.ToolUseID)
	}
	if ag.Transcript == nil || ag.Transcript.ID != "aaaa1111bbbb2222" {
		t.Errorf("agent transcript ref = %+v", ag.Transcript)
	}

	// Workflow run node attaches to the spawning turn (a4) via the runId edge;
	// phases nest under the run; workflow agents nest under their phase.
	wf, ok := fi.Node("wf:wf_run1")
	if !ok {
		t.Fatal("workflow node missing")
	}
	if wf.ParentID != "turn:a4" {
		t.Errorf("workflow parent = %q, want turn:a4", wf.ParentID)
	}
	if wf.Origin.ToolUseID != "toolu_wf1" {
		t.Errorf("workflow origin tool_use = %q, want toolu_wf1", wf.Origin.ToolUseID)
	}
	ph, ok := fi.Node("phase:wf_run1:1")
	if !ok {
		t.Fatal("phase node missing")
	}
	if ph.ParentID != "wf:wf_run1" || ph.Label != "Scan" {
		t.Errorf("phase node = %+v", ph)
	}
	wfa, ok := fi.Node("agent:bbbb2222cccc3333")
	if !ok {
		t.Fatal("workflow agent node missing")
	}
	if wfa.ParentID != "phase:wf_run1:1" {
		t.Errorf("workflow agent parent = %q, want phase:wf_run1:1", wfa.ParentID)
	}
	if wfa.Label != "scout" {
		t.Errorf("workflow agent label = %q, want scout", wfa.Label)
	}
	if wfa.Estimated {
		t.Error("transcribed workflow agent must not be Estimated")
	}

	// Summary-only agent still becomes a node, Transcript nil + Estimated.
	ghost, ok := fi.Node("agent:cccc3333dddd4444")
	if !ok {
		t.Fatal("summary-only workflow agent node missing")
	}
	if ghost.Transcript != nil || !ghost.Estimated || ghost.Label != "ghost" {
		t.Errorf("summary-only agent node = %+v", ghost)
	}
	if ghost.ParentID != "phase:wf_run1:1" {
		t.Errorf("summary-only agent parent = %q, want phase:wf_run1:1", ghost.ParentID)
	}
}

func TestBuildSessionFlow_ArtifactProvenance(t *testing.T) {
	sess := writeFlowFixture(t)
	fi, err := BuildSessionFlow(sess)
	if err != nil {
		t.Fatal(err)
	}

	// Agent-owned image resolves to the AGENT transcript, not the parent.
	imgs := fi.Artifacts("agent:aaaa1111bbbb2222", ArtifactImage, ScopeNode)
	if len(imgs) != 1 {
		t.Fatalf("agent images = %d, want 1", len(imgs))
	}
	img := imgs[0]
	if filepath.Base(img.Origin.Transcript) != "agent-aaaa1111bbbb2222.jsonl" {
		t.Errorf("image transcript = %q, want the agent transcript", img.Origin.Transcript)
	}
	if id, _ := img.Data.(int); id != 7 {
		t.Errorf("image paste ID = %v, want 7", img.Data)
	}
	if img.Origin.AgentID != "aaaa1111bbbb2222" {
		t.Errorf("image AgentID = %q", img.Origin.AgentID)
	}

	// Ref occurrence carries turn provenance in the parent transcript.
	refs := fi.Artifacts(fi.RootID, ArtifactRef, ScopeSession)
	if len(refs) != 1 {
		t.Fatalf("refs = %d, want 1", len(refs))
	}
	if refs[0].Key != "o/r#12" || refs[0].NodeID != "turn:a5" {
		t.Errorf("ref = key %q node %q, want o/r#12 on turn:a5", refs[0].Key, refs[0].NodeID)
	}
	if refs[0].Origin.Transcript != sess.FilePath {
		t.Errorf("ref transcript = %q, want parent transcript", refs[0].Origin.Transcript)
	}

	// Error occurrence from is_error tool_result.
	errs := fi.Artifacts(fi.RootID, ArtifactError, ScopeSession)
	if len(errs) != 1 {
		t.Fatalf("errors = %d, want 1", len(errs))
	}

	// Every change occurrence is stored (2× main.go + other.go + agent.go);
	// presentation dedupe collapses by key.
	changes := fi.Artifacts(fi.RootID, ArtifactChange, ScopeSession)
	if len(changes) != 4 {
		t.Fatalf("change occurrences = %d, want 4", len(changes))
	}
	deduped := DedupeArtifactsByKey(changes)
	if len(deduped) != 3 {
		t.Errorf("deduped changes = %d, want 3", len(deduped))
	}
}

func TestBuildSessionFlow_Decisions(t *testing.T) {
	sess := writeFlowFixture(t)
	fi, err := BuildSessionFlow(sess)
	if err != nil {
		t.Fatal(err)
	}

	decisions := fi.Decisions(ScopeSession)
	kinds := make(map[DecisionKind]int)
	changeKeys := make(map[string]int)
	firstChangeLabels := 0
	for _, d := range decisions {
		dd, ok := d.Data.(DecisionData)
		if !ok {
			t.Fatalf("decision Data type %T", d.Data)
		}
		kinds[dd.Kind]++
		if dd.Kind == DecisionFirstChange {
			changeKeys[d.Key]++
			if strings.HasPrefix(dd.Label, "first change:") {
				firstChangeLabels++
			}
		}
	}

	if kinds[DecisionTask] != 1 {
		t.Errorf("task decisions = %d, want 1", kinds[DecisionTask])
	}
	if kinds[DecisionPlan] != 1 {
		t.Errorf("plan decisions = %d, want 1", kinds[DecisionPlan])
	}
	// Every change occurrence is now marked: main.go twice, other.go, agent.go.
	if kinds[DecisionFirstChange] != 4 {
		t.Errorf("change decisions = %d, want 4 (%v)", kinds[DecisionFirstChange], changeKeys)
	}
	if changeKeys["first-change:/x/main.go"] != 2 {
		t.Errorf("main.go change markers = %d, want 2", changeKeys["first-change:/x/main.go"])
	}
	// Exactly one "first change:" label per distinct file (main/other/agent).
	if firstChangeLabels != 3 {
		t.Errorf("first-change labels = %d, want 3", firstChangeLabels)
	}
	if changeKeys["first-change:/x/agent.go"] != 1 {
		t.Errorf("agent.go change markers = %d, want 1", changeKeys["first-change:/x/agent.go"])
	}
	// u1 is followed (before the next real user turn) by TaskCreate → steering.
	if kinds[DecisionSteering] != 1 {
		t.Errorf("steering decisions = %d, want 1", kinds[DecisionSteering])
	}
	for _, d := range decisions {
		if dd := d.Data.(DecisionData); dd.Kind == DecisionSteering {
			if d.Origin.MessageUUID != "u1" {
				t.Errorf("steering origin = %q, want u1", d.Origin.MessageUUID)
			}
		}
	}
	// Chronological order.
	for i := 1; i < len(decisions); i++ {
		if decisions[i].Origin.Timestamp.Before(decisions[i-1].Origin.Timestamp) {
			t.Errorf("decisions out of order at %d", i)
		}
	}

	// Each change marker points at a distinct occurrence: the two main.go
	// markers must have different origins (first edit vs. second edit).
	var mainOrigins []ArtifactOrigin
	for _, d := range decisions {
		dd := d.Data.(DecisionData)
		if dd.Kind == DecisionFirstChange && d.Key == "first-change:/x/main.go" {
			mainOrigins = append(mainOrigins, d.Origin)
		}
	}
	if len(mainOrigins) != 2 {
		t.Fatalf("main.go markers = %d, want 2", len(mainOrigins))
	}
	if mainOrigins[0].Transcript == mainOrigins[1].Transcript &&
		mainOrigins[0].EntryIndex == mainOrigins[1].EntryIndex &&
		mainOrigins[0].BlockIndex == mainOrigins[1].BlockIndex {
		t.Errorf("main.go change markers share an origin %+v; each must point at its own edit", mainOrigins[0])
	}
}

func TestBuildSessionFlow_ScopeAggregation(t *testing.T) {
	sess := writeFlowFixture(t)
	fi, err := BuildSessionFlow(sess)
	if err != nil {
		t.Fatal(err)
	}

	// Node scope: turn a3 itself owns 3 change occurrences.
	if got := len(fi.Artifacts("turn:a3", ArtifactChange, ScopeNode)); got != 3 {
		t.Errorf("turn:a3 node-scope changes = %d, want 3", got)
	}
	// The agent's change is NOT in turn a3's scope…
	// …but IS in turn a2's subtree (agent is a child of a2).
	if got := len(fi.Artifacts("turn:a2", ArtifactChange, ScopeNode)); got != 0 {
		t.Errorf("turn:a2 node-scope changes = %d, want 0", got)
	}
	if got := len(fi.Artifacts("turn:a2", ArtifactChange, ScopeSubtree)); got != 1 {
		t.Errorf("turn:a2 subtree changes = %d, want 1", got)
	}
	// Session scope sees all 4 occurrences.
	if got := len(fi.Artifacts("turn:a2", ArtifactChange, ScopeSession)); got != 4 {
		t.Errorf("session-scope changes = %d, want 4", got)
	}

	// Facets mirror the same scoping and carry tokens.
	f := fi.Facets("turn:a3", ScopeNode)
	if f.Counts[ArtifactChange] != 3 {
		t.Errorf("turn:a3 facet changes = %d, want 3", f.Counts[ArtifactChange])
	}
	if f.Tokens != 20 {
		t.Errorf("turn:a3 facet tokens = %d, want 20", f.Tokens)
	}
	sub := fi.Facets("turn:a2", ScopeSubtree)
	if sub.Counts[ArtifactChange] != 1 || sub.Counts[ArtifactImage] != 1 {
		t.Errorf("turn:a2 subtree facets = %+v", sub.Counts)
	}
	if sub.Tokens != 30 { // agent transcript output tokens
		t.Errorf("turn:a2 subtree tokens = %d, want 30", sub.Tokens)
	}
	sess2 := fi.Facets(fi.RootID, ScopeSession)
	if sess2.Counts[ArtifactChange] != 4 {
		t.Errorf("session facet changes = %d, want 4", sess2.Counts[ArtifactChange])
	}
	if sess2.Errors != 1 {
		t.Errorf("session facet errors = %d, want 1", sess2.Errors)
	}
}

func TestBuildSessionFlow_StatsNoDoubleCount(t *testing.T) {
	sess := writeFlowFixture(t)
	fi, err := BuildSessionFlow(sess)
	if err != nil {
		t.Fatal(err)
	}

	st := fi.Stats(fi.RootID, ScopeSession)
	// Exact transcript totals: parent (10+20) + agent (30) + wf agent (40).
	if st.TotalOutputTokens != 100 {
		t.Errorf("TotalOutputTokens = %d, want 100", st.TotalOutputTokens)
	}
	// Only the summary-only "ghost" agent contributes estimated metrics; the
	// transcribed workflow agent (500 summary tokens) must NOT be added again.
	if !st.Estimated {
		t.Error("Estimated = false, want true (ghost agent)")
	}
	if st.EstimatedTokens != 700 {
		t.Errorf("EstimatedTokens = %d, want 700 (ghost only)", st.EstimatedTokens)
	}
	if st.EstimatedToolCalls != 5 {
		t.Errorf("EstimatedToolCalls = %d, want 5", st.EstimatedToolCalls)
	}

	// Node scope on a transcribed agent: exact stats, nothing estimated.
	ast := fi.Stats("agent:bbbb2222cccc3333", ScopeNode)
	if ast.TotalOutputTokens != 40 || ast.Estimated {
		t.Errorf("wf agent stats = tokens %d estimated %v, want 40/false", ast.TotalOutputTokens, ast.Estimated)
	}
}

func TestBuildSessionFlow_WorkflowJoinMeta(t *testing.T) {
	sess := writeFlowFixture(t)
	fi, err := BuildSessionFlow(sess)
	if err != nil {
		t.Fatal(err)
	}
	var wf *Subagent
	for i := range fi.Agents() {
		if fi.Agents()[i].ID == "bbbb2222cccc3333" {
			a := fi.Agents()[i]
			wf = &a
		}
	}
	if wf == nil {
		t.Fatal("workflow agent not in Agents()")
	}
	if wf.WorkflowLabel != "scout" || wf.WorkflowPhaseIndex != 1 || wf.WorkflowPhaseTitle != "Scan" {
		t.Errorf("workflow meta = %q/%d/%q, want scout/1/Scan", wf.WorkflowLabel, wf.WorkflowPhaseIndex, wf.WorkflowPhaseTitle)
	}
}

func TestLoadShellJobs_StatusFromBashOutputResult(t *testing.T) {
	t1 := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	mk := func(min int) time.Time { return t1.Add(time.Duration(min) * time.Minute) }

	entries := []Entry{
		{Timestamp: mk(0), Role: "assistant", Content: []ContentBlock{{
			Type: "tool_use", ToolName: "Bash", ID: "toolu_b1",
			ToolInput: `{"command":"make build","run_in_background":true}`,
		}}},
		{Timestamp: mk(1), Role: "assistant", Content: []ContentBlock{{
			Type: "tool_use", ToolName: "BashOutput", ID: "toolu_p1",
			ToolInput: `{"tool_use_id":"toolu_b1"}`,
		}}},
		// Poll result carries an explicit completion record.
		{Timestamp: mk(2), Role: "user", Content: []ContentBlock{{
			Type: "tool_result", ID: "toolu_p1",
			Text: "<retrieval_status>success</retrieval_status>\n<task_id>b123</task_id>\n<status>failed</status>\n<exit_code>1</exit_code>\n<output>boom</output>",
		}}},
	}
	jobs := LoadShellJobsFromEntries(entries)
	if len(jobs) != 1 {
		t.Fatalf("jobs = %d, want 1", len(jobs))
	}
	if jobs[0].Status != "failed" {
		t.Errorf("status = %q, want failed", jobs[0].Status)
	}
	if jobs[0].PollCount != 1 {
		t.Errorf("poll count = %d, want 1", jobs[0].PollCount)
	}

	// completed case
	entries[2].Content[0].Text = "<retrieval_status>success</retrieval_status>\n<status>completed</status>\n<exit_code>0</exit_code>"
	jobs = LoadShellJobsFromEntries(entries)
	if jobs[0].Status != "completed" {
		t.Errorf("status = %q, want completed", jobs[0].Status)
	}

	// Undetectable result → conservative: stays "polled".
	entries[2].Content[0].Text = "some interim output, still going"
	jobs = LoadShellJobsFromEntries(entries)
	if jobs[0].Status != "polled" {
		t.Errorf("status = %q, want polled", jobs[0].Status)
	}
}

func TestLoadShellJobs_StatusFromTaskNotification(t *testing.T) {
	t1 := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	entries := []Entry{
		{Timestamp: t1, Role: "assistant", Content: []ContentBlock{{
			Type: "tool_use", ToolName: "Bash", ID: "toolu_b2",
			ToolInput: `{"command":"docker build .","run_in_background":true}`,
		}}},
		// Background completion arrives as a task-notification system tag.
		{Timestamp: t1.Add(5 * time.Minute), Role: "user", Content: []ContentBlock{{
			Type:    "system_tag",
			TagName: "task-notification",
			Text:    "<task-id>bx1</task-id>\n<tool-use-id>toolu_b2</tool-use-id>\n<status>completed</status>\n<summary>Background command \"docker build\" completed (exit code 0)</summary>",
		}}},
	}
	jobs := LoadShellJobsFromEntries(entries)
	if len(jobs) != 1 {
		t.Fatalf("jobs = %d, want 1", len(jobs))
	}
	if jobs[0].Status != "completed" {
		t.Errorf("status = %q, want completed", jobs[0].Status)
	}
	// Completed jobs are no longer "active".
	s := Session{IsLive: true, HasShellJobs: true, ShellJobs: jobs}
	if n := len(s.ActiveShellJobs()); n != 0 {
		t.Errorf("active jobs = %d, want 0", n)
	}
}

func TestBuildSessionFlow_ShellNodes(t *testing.T) {
	root := t.TempDir()
	sessID := "sess-shell"
	sessFile := filepath.Join(root, sessID+".jsonl")
	content := `{"type":"user","uuid":"u1","timestamp":"2026-06-01T09:00:00Z","message":{"role":"user","content":"run it"}}
{"type":"assistant","uuid":"a1","timestamp":"2026-06-01T09:00:10Z","message":{"role":"assistant","model":"m","content":[{"type":"tool_use","id":"toolu_sh1","name":"Bash","input":{"command":"make watch","description":"watch build","run_in_background":true}}]}}
`
	if err := os.WriteFile(sessFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := BuildSessionFlow(&Session{ID: sessID, FilePath: sessFile})
	if err != nil {
		t.Fatal(err)
	}
	sh, ok := fi.Node("shell:toolu_sh1")
	if !ok {
		t.Fatal("shell node missing")
	}
	if sh.ParentID != "turn:a1" {
		t.Errorf("shell parent = %q, want turn:a1", sh.ParentID)
	}
	if sh.Origin.ToolUseID != "toolu_sh1" || sh.Label != "watch build" {
		t.Errorf("shell node = %+v", sh)
	}
	if len(fi.ShellJobs()) != 1 {
		t.Errorf("ShellJobs = %d, want 1", len(fi.ShellJobs()))
	}
}

func TestFlowIndex_ChildrenAndDedup(t *testing.T) {
	sess := writeFlowFixture(t)
	fi, err := BuildSessionFlow(sess)
	if err != nil {
		t.Fatal(err)
	}
	kids := fi.Children("turn:a2")
	if len(kids) != 1 || kids[0].ID != "agent:aaaa1111bbbb2222" {
		t.Errorf("turn:a2 children = %+v", kids)
	}
	// Files: main.go referenced twice → occurrences 2, dedupe 1.
	files := fi.Artifacts("turn:a3", ArtifactFile, ScopeNode)
	if len(files) != 3 {
		t.Errorf("file occurrences = %d, want 3", len(files))
	}
	if got := len(DedupeArtifactsByKey(files)); got != 2 {
		t.Errorf("deduped files = %d, want 2", got)
	}
}
