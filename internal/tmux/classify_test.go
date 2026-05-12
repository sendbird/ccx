package tmux

import (
	"reflect"
	"sort"
	"testing"
)

func TestClassifyClaudeProcs_DirectChildAndOrphan(t *testing.T) {
	// Pane shell PID 100 spawned a direct claude (PID 200).
	// PID 300 is an orphan (parent isn't a pane and isn't another claude).
	procs := []ClaudeProc{
		{PID: 200, PPID: 100, Args: "claude --session-id abc"},
		{PID: 300, PPID: 1, Args: "claude --session-id def"},
	}
	panePIDs := map[int]bool{100: true}

	direct, orphan := classifyClaudeProcs(procs, panePIDs)
	if got, want := direct[100], "claude --session-id abc"; got != want {
		t.Fatalf("direct[100] = %q, want %q", got, want)
	}
	if len(orphan) != 1 || orphan[0].PID != 300 {
		t.Fatalf("expected orphan PID 300; got %+v", orphan)
	}
}

func TestClassifyClaudeProcs_DropsSubagent(t *testing.T) {
	// Pane shell PID 100 -> claude PID 200 -> subagent PID 201 (PPID=200).
	// The subagent must NOT appear in either direct or orphan, otherwise the
	// fallback would mark an unrelated past session on the same path as LIVE.
	procs := []ClaudeProc{
		{PID: 200, PPID: 100, Args: "claude --session-id main"},
		{PID: 201, PPID: 200, Args: "claude --session-id agent-1"},
		{PID: 202, PPID: 200, Args: "claude --session-id agent-2"},
	}
	panePIDs := map[int]bool{100: true}

	direct, orphan := classifyClaudeProcs(procs, panePIDs)
	if got, want := direct[100], "claude --session-id main"; got != want {
		t.Fatalf("direct[100] = %q, want %q", got, want)
	}
	if len(orphan) != 0 {
		gotPIDs := make([]int, len(orphan))
		for i, o := range orphan {
			gotPIDs[i] = o.PID
		}
		sort.Ints(gotPIDs)
		t.Fatalf("subagents must be excluded; got orphan PIDs %v", gotPIDs)
	}
	if len(direct) != 1 {
		t.Fatalf("direct map should hold exactly 1 entry, got %d", len(direct))
	}
}

func TestClassifyClaudeProcs_NestedSubagent(t *testing.T) {
	// Three-level chain: pane -> claude (200) -> subagent (201) -> nested (202).
	// Both 201 and 202 must be dropped.
	procs := []ClaudeProc{
		{PID: 200, PPID: 100, Args: "claude main"},
		{PID: 201, PPID: 200, Args: "claude sub-1"},
		{PID: 202, PPID: 201, Args: "claude sub-2"},
	}
	panePIDs := map[int]bool{100: true}

	direct, orphan := classifyClaudeProcs(procs, panePIDs)
	if len(direct) != 1 || direct[100] != "claude main" {
		t.Fatalf("direct = %v, want only main claude", direct)
	}
	if len(orphan) != 0 {
		t.Fatalf("nested subagents must be excluded; got %d orphans", len(orphan))
	}
}

func TestClassifyClaudeProcs_StandaloneOrphan(t *testing.T) {
	// nohup'd / pane-closed claude with PPID=1 is a legitimate orphan and
	// should still be classified as orphan (not subagent).
	procs := []ClaudeProc{
		{PID: 500, PPID: 1, Args: "claude --session-id standalone"},
	}
	panePIDs := map[int]bool{100: true, 200: true}

	direct, orphan := classifyClaudeProcs(procs, panePIDs)
	if len(direct) != 0 {
		t.Fatalf("direct should be empty, got %v", direct)
	}
	wantOrphan := []ClaudeProc{{PID: 500, PPID: 1, Args: "claude --session-id standalone"}}
	if !reflect.DeepEqual(orphan, wantOrphan) {
		t.Fatalf("orphan = %+v, want %+v", orphan, wantOrphan)
	}
}

func TestClassifyClaudeProcsByAncestry_CcproxyWrappedClaude(t *testing.T) {
	// pane shell (100) -> ccproxy (150) -> claude (200).
	// The ancestry walk must attribute claude to pane shell 100 even though
	// the immediate PPID is ccproxy, not the pane.
	procs := []ClaudeProc{
		{PID: 200, PPID: 150, Args: "claude --settings ..."},
	}
	panePIDs := map[int]bool{100: true}
	ppidOf := map[int]int{
		200: 150,
		150: 100,
		100: 1,
	}
	direct, orphan := classifyClaudeProcsByAncestry(procs, panePIDs, ppidOf)
	if got, want := direct[100], "claude --settings ..."; got != want {
		t.Fatalf("direct[100] = %q, want %q", got, want)
	}
	if len(orphan) != 0 {
		t.Fatalf("ccproxy-wrapped claude must not be orphaned; got %+v", orphan)
	}
}

func TestClassifyClaudeProcsByAncestry_SubagentUnderWrappedClaude(t *testing.T) {
	// pane (100) -> ccproxy (150) -> main claude (200) -> sub claude (201).
	// Sub must be dropped because the chain passes through another claude
	// before reaching the pane.
	procs := []ClaudeProc{
		{PID: 200, PPID: 150, Args: "main"},
		{PID: 201, PPID: 200, Args: "sub"},
	}
	panePIDs := map[int]bool{100: true}
	ppidOf := map[int]int{
		200: 150,
		201: 200,
		150: 100,
		100: 1,
	}
	direct, orphan := classifyClaudeProcsByAncestry(procs, panePIDs, ppidOf)
	if got, want := direct[100], "main"; got != want {
		t.Fatalf("direct[100] = %q, want %q", got, want)
	}
	if len(orphan) != 0 {
		t.Fatalf("subagent must be dropped; got %+v", orphan)
	}
}

func TestClassifyClaudeProcsByAncestry_TrueOrphan(t *testing.T) {
	procs := []ClaudeProc{{PID: 300, PPID: 1, Args: "lingering"}}
	panePIDs := map[int]bool{100: true}
	ppidOf := map[int]int{300: 1, 100: 1}
	direct, orphan := classifyClaudeProcsByAncestry(procs, panePIDs, ppidOf)
	if len(direct) != 0 {
		t.Fatalf("no pane in chain; direct should be empty, got %v", direct)
	}
	if len(orphan) != 1 || orphan[0].PID != 300 {
		t.Fatalf("expected lingering claude in orphans; got %+v", orphan)
	}
}

func TestClassifyClaudeProcsByAncestry_CycleDefense(t *testing.T) {
	procs := []ClaudeProc{{PID: 400, PPID: 401, Args: "loop"}}
	panePIDs := map[int]bool{100: true}
	ppidOf := map[int]int{400: 401, 401: 400}
	direct, orphan := classifyClaudeProcsByAncestry(procs, panePIDs, ppidOf)
	if len(direct) != 0 {
		t.Fatalf("cycle without pane should produce no direct; got %v", direct)
	}
	if len(orphan) != 1 {
		t.Fatalf("cycle without pane should yield 1 orphan; got %+v", orphan)
	}
}

func TestClassifyClaudeProcsByAncestry_RehomeScenario(t *testing.T) {
	// The user-reported case: pane 1 has ccproxy-wrapped claude; two other
	// claude processes (e.g. leftovers from previous pane-1 sessions) are
	// orphans whose cwds happen to match current-window paths. Only the
	// pane-1-owned claude must show up under directByPaneShell. The orphans
	// must NOT silently inherit the current-window pane shell.
	procs := []ClaudeProc{
		{PID: 200, PPID: 150, Args: "claude main"},
		{PID: 300, PPID: 1, Args: "claude orphan-a"},
		{PID: 400, PPID: 1, Args: "claude orphan-b"},
	}
	panePIDs := map[int]bool{100: true}
	ppidOf := map[int]int{
		200: 150,
		150: 100,
		100: 1,
		300: 1,
		400: 1,
	}
	direct, orphan := classifyClaudeProcsByAncestry(procs, panePIDs, ppidOf)
	if got, want := direct[100], "claude main"; got != want {
		t.Fatalf("direct[100] = %q, want %q", got, want)
	}
	if len(direct) != 1 {
		t.Fatalf("only pane 1's claude should be direct; got %d entries (%v)", len(direct), direct)
	}
	if len(orphan) != 2 {
		t.Fatalf("two unattributable claudes should be orphans; got %+v", orphan)
	}
}
