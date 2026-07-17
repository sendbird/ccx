package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WorkflowRun is the parsed summary of one Workflow execution, read from
// {sessionID}/workflows/{runId}.json. Claude Code's Workflow tool writes this
// file when a workflow (dynamic multi-agent orchestration) completes. The
// spawned agents' full transcripts live separately under
// {sessionID}/subagents/workflows/{runId}/agent-*.jsonl.
type WorkflowRun struct {
	RunID          string
	Name           string // workflowName / meta.name
	Summary        string
	Status         string // "completed", "running", "error", ...
	AgentCount     int
	DurationMS     int64
	TotalTokens    int64
	TotalToolCalls int64
	DefaultModel   string
	Phases         []WorkflowPhase
	Agents         []WorkflowAgent
	// Result is the workflow's final synthesized output. The on-disk `result`
	// field is EITHER a plain string OR an object like {"results":[...]}; both
	// shapes are normalized to text here.
	Result   string
	FilePath string
}

// WorkflowPhase is one phase declared in the workflow script's meta.phases.
type WorkflowPhase struct {
	Index  int
	Title  string
	Detail string
}

// WorkflowAgent is one agent invocation within a workflow run, from the
// workflowProgress array (type=workflow_agent entries).
type WorkflowAgent struct {
	Index         int
	Label         string
	PhaseIndex    int
	PhaseTitle    string
	AgentID       string
	Model         string
	State         string // "done", "error", "running", ...
	Tokens        int64
	ToolCalls     int64
	DurationMS    int64
	Attempt       int
	LastToolName  string
	PromptPreview string
	ResultPreview string
}

// wfProgressEntry mirrors one element of the workflowProgress array. It is a
// union of phase and agent entries, discriminated by Type.
type wfProgressEntry struct {
	Type          string `json:"type"`
	Index         int    `json:"index"`
	Title         string `json:"title"`
	Detail        string `json:"detail"`
	Label         string `json:"label"`
	PhaseIndex    int    `json:"phaseIndex"`
	PhaseTitle    string `json:"phaseTitle"`
	AgentID       string `json:"agentId"`
	Model         string `json:"model"`
	State         string `json:"state"`
	Tokens        int64  `json:"tokens"`
	ToolCalls     int64  `json:"toolCalls"`
	DurationMS    int64  `json:"durationMs"`
	Attempt       int    `json:"attempt"`
	LastToolName  string `json:"lastToolName"`
	PromptPreview string `json:"promptPreview"`
	ResultPreview string `json:"resultPreview"`
}

// wfFile is the raw on-disk shape of a workflow run JSON file. `result` and
// `phases` use json.RawMessage because their shapes vary across runs (result:
// string|object; phases may be absent on minimal/older runs).
type wfFile struct {
	RunID          string          `json:"runId"`
	WorkflowName   string          `json:"workflowName"`
	Summary        string          `json:"summary"`
	Status         string          `json:"status"`
	AgentCount     int             `json:"agentCount"`
	DurationMS     int64           `json:"durationMs"`
	TotalTokens    int64           `json:"totalTokens"`
	TotalToolCalls int64           `json:"totalToolCalls"`
	DefaultModel   string          `json:"defaultModel"`
	Phases         json.RawMessage `json:"phases"`
	Progress       []wfProgressEntry `json:"workflowProgress"`
	Result         json.RawMessage `json:"result"`
}

// FindWorkflows returns the workflow runs recorded for a session, newest first.
// Reads {sessionID}/workflows/*.json. Returns nil (no error) when the session
// has no workflows directory.
func FindWorkflows(sessionFile string) ([]WorkflowRun, error) {
	dir := filepath.Dir(sessionFile)
	sessID := strings.TrimSuffix(filepath.Base(sessionFile), ".jsonl")
	wfDir := filepath.Join(dir, sessID, "workflows")

	if _, err := os.Stat(wfDir); os.IsNotExist(err) {
		return nil, nil
	}

	matches, err := filepath.Glob(filepath.Join(wfDir, "*.json"))
	if err != nil {
		return nil, err
	}

	var runs []WorkflowRun
	for _, p := range matches {
		run, ok := parseWorkflowFile(p)
		if ok {
			runs = append(runs, run)
		}
	}

	// Newest first by RunID as a stable fallback (files carry no reliable mtime
	// ordering guarantee); the caller can re-sort by any WorkflowAgent field.
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].RunID > runs[j].RunID
	})
	return runs, nil
}

// HasWorkflows reports whether a session has any recorded workflow runs.
func HasWorkflows(sessionFile string) bool {
	dir := filepath.Dir(sessionFile)
	sessID := strings.TrimSuffix(filepath.Base(sessionFile), ".jsonl")
	wfDir := filepath.Join(dir, sessID, "workflows")
	matches, _ := filepath.Glob(filepath.Join(wfDir, "*.json"))
	return len(matches) > 0
}

// JoinWorkflowAgents matches workflow-spawned subagents (from FindSubagents,
// carrying WorkflowRunID) against the run summaries and fills in each agent's
// WorkflowLabel from the summary's workflowProgress. Returns only the
// workflow-nested agents, ordered by run (newest run first) then by the
// summary's agent order, so drill-down lists read like the workflow itself.
func JoinWorkflowAgents(runs []WorkflowRun, agents []Subagent) []Subagent {
	// agentID → summary entry, and agentID → run order index.
	waByID := make(map[string]WorkflowAgent)
	runOrder := make(map[string]int)
	agentOrder := make(map[string]int)
	for ri, r := range runs {
		for ai, wa := range r.Agents {
			if wa.AgentID == "" {
				continue
			}
			waByID[wa.AgentID] = wa
			runOrder[wa.AgentID] = ri
			agentOrder[wa.AgentID] = ai
		}
	}

	var out []Subagent
	for _, a := range agents {
		if a.WorkflowRunID == "" {
			continue
		}
		if wa, ok := waByID[a.ID]; ok {
			a.WorkflowLabel = wa.Label
			a.WorkflowPhaseIndex = wa.PhaseIndex
			a.WorkflowPhaseTitle = wa.PhaseTitle
		}
		out = append(out, a)
	}

	sort.Slice(out, func(i, j int) bool {
		ri, rj := runOrder[out[i].ID], runOrder[out[j].ID]
		if ri != rj {
			return ri < rj
		}
		return agentOrder[out[i].ID] < agentOrder[out[j].ID]
	})
	return out
}

func parseWorkflowFile(path string) (WorkflowRun, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return WorkflowRun{}, false
	}
	var raw wfFile
	if err := json.Unmarshal(data, &raw); err != nil {
		return WorkflowRun{}, false
	}

	run := WorkflowRun{
		RunID:          raw.RunID,
		Name:           raw.WorkflowName,
		Summary:        raw.Summary,
		Status:         raw.Status,
		AgentCount:     raw.AgentCount,
		DurationMS:     raw.DurationMS,
		TotalTokens:    raw.TotalTokens,
		TotalToolCalls: raw.TotalToolCalls,
		DefaultModel:   raw.DefaultModel,
		Result:         normalizeWorkflowResult(raw.Result),
		FilePath:       path,
	}
	if run.RunID == "" {
		// Fall back to the filename (sans .json) as the run id.
		run.RunID = strings.TrimSuffix(filepath.Base(path), ".json")
	}

	// Phases: prefer the top-level `phases` array; fall back to phase entries in
	// workflowProgress if absent.
	run.Phases = parseWorkflowPhases(raw.Phases)

	phaseOrder := 0
	for _, e := range raw.Progress {
		switch e.Type {
		case "workflow_phase":
			if len(run.Phases) == 0 {
				phaseOrder++
				idx := e.Index
				if idx == 0 {
					idx = phaseOrder
				}
				run.Phases = append(run.Phases, WorkflowPhase{Index: idx, Title: e.Title, Detail: e.Detail})
			}
		case "workflow_agent":
			run.Agents = append(run.Agents, WorkflowAgent{
				Index:         e.Index,
				Label:         e.Label,
				PhaseIndex:    e.PhaseIndex,
				PhaseTitle:    e.PhaseTitle,
				AgentID:       e.AgentID,
				Model:         e.Model,
				State:         e.State,
				Tokens:        e.Tokens,
				ToolCalls:     e.ToolCalls,
				DurationMS:    e.DurationMS,
				Attempt:       e.Attempt,
				LastToolName:  e.LastToolName,
				PromptPreview: e.PromptPreview,
				ResultPreview: e.ResultPreview,
			})
		}
	}
	return run, true
}

func parseWorkflowPhases(raw json.RawMessage) []WorkflowPhase {
	if len(raw) == 0 {
		return nil
	}
	var arr []struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil
	}
	phases := make([]WorkflowPhase, 0, len(arr))
	for i, p := range arr {
		phases = append(phases, WorkflowPhase{Index: i + 1, Title: p.Title, Detail: p.Detail})
	}
	return phases
}

// normalizeWorkflowResult flattens the polymorphic `result` field to text.
// Handles a plain JSON string, an object {"results":["...", ...]}, or any other
// object (rendered as pretty JSON as a last resort).
func normalizeWorkflowResult(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Case 1: plain string.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Case 2: {"results": [...]}.
	var obj struct {
		Results []string `json:"results"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && len(obj.Results) > 0 {
		return strings.Join(obj.Results, "\n\n---\n\n")
	}
	// Case 3: any other object — pretty-print so nothing is silently lost.
	var pretty map[string]any
	if err := json.Unmarshal(raw, &pretty); err == nil {
		if b, err := json.MarshalIndent(pretty, "", "  "); err == nil {
			return string(b)
		}
	}
	return string(raw)
}
