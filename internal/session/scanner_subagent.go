package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func FindSubagents(sessionFile string) ([]Subagent, error) {
	dir := filepath.Dir(sessionFile)
	sessID := strings.TrimSuffix(filepath.Base(sessionFile), ".jsonl")
	agentDir := filepath.Join(dir, sessID, "subagents")

	if _, err := os.Stat(agentDir); os.IsNotExist(err) {
		return nil, nil
	}

	var agents []Subagent
	// Lifecycle terminal signals often live inside the agent transcripts we are
	// already parsing here (a nested agent's tool_result or task-notification).
	// Collect them during that single pass so applySubagentLifecycles only has to
	// read the root transcript once, instead of re-reading every agent file.
	var signals []lifecycleSignal

	// 1. Ordinary Task/Agent subagents directly under subagents/.
	matches, err := filepath.Glob(filepath.Join(agentDir, "agent-*.jsonl"))
	if err != nil {
		return nil, err
	}
	for _, p := range matches {
		base := filepath.Base(p)
		if strings.HasPrefix(base, "agent-acompact-") {
			continue
		}
		sa, sigs := scanSubagentFile(p)
		agents = append(agents, sa)
		signals = append(signals, sigs...)
	}

	// 2. Workflow-spawned agents nested under subagents/workflows/{runId}/.
	// These were previously invisible: the non-recursive glob above never
	// descended into the per-run directories.
	wfDir := filepath.Join(agentDir, "workflows")
	if runDirs, derr := os.ReadDir(wfDir); derr == nil {
		for _, rd := range runDirs {
			if !rd.IsDir() {
				continue
			}
			runID := rd.Name()
			runMatches, _ := filepath.Glob(filepath.Join(wfDir, runID, "agent-*.jsonl"))
			for _, p := range runMatches {
				base := filepath.Base(p)
				if strings.HasPrefix(base, "agent-acompact-") {
					continue
				}
				sa, sigs := scanSubagentFile(p)
				sa.WorkflowRunID = runID
				agents = append(agents, sa)
				signals = append(signals, sigs...)
			}
		}
	}

	applySubagentLifecycles(sessionFile, agents, signals)
	sort.Slice(agents, func(i, j int) bool {
		return agents[i].Timestamp.After(agents[j].Timestamp)
	})
	return agents, nil
}

// lifecycleSignal is a terminal-completion marker for a subagent, extracted
// while scanning some transcript: agentID ends at ts.
type lifecycleSignal struct {
	agentID string
	ts      time.Time
}

// lifecycleSignalsFromEntry returns any terminal-completion markers carried by a
// single parsed entry: a synchronous spawning tool_result, or a terminal
// <task-notification> for a background agent.
func lifecycleSignalsFromEntry(entry Entry) []lifecycleSignal {
	var out []lifecycleSignal
	if entry.ToolResultAgentID != "" && !entry.ToolResultAsync {
		out = append(out, lifecycleSignal{entry.ToolResultAgentID, entry.Timestamp})
	}
	for _, b := range entry.Content {
		if b.Type != "system_tag" && b.Type != "text" {
			continue
		}
		id := xmlValue(b.Text, "task-id")
		if id != "" && terminalTaskStatus(xmlValue(b.Text, "status")) {
			out = append(out, lifecycleSignal{id, entry.Timestamp})
		}
	}
	return out
}

func applySubagentLifecycles(sessionFile string, agents []Subagent, signals []lifecycleSignal) {
	byID := make(map[string]*Subagent, len(agents))
	for i := range agents {
		byID[agents[i].ID] = &agents[i]
	}
	apply := func(sig lifecycleSignal) {
		if agent := byID[sig.agentID]; agent != nil {
			setAgentEnd(agent, sig.ts)
		}
	}
	// Signals gathered from the agent transcripts during the discovery pass.
	for _, sig := range signals {
		apply(sig)
	}
	// The root transcript is not scanned elsewhere here, so read it once for the
	// common case: a synchronous agent whose spawning tool_result lands in root.
	f, err := os.Open(sessionFile)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 10*1024*1024)
	for sc.Scan() {
		entry, err := ParseEntry(sc.Text())
		if err != nil {
			continue
		}
		for _, sig := range lifecycleSignalsFromEntry(entry) {
			apply(sig)
		}
	}
}

func xmlValue(text, tag string) string {
	startTag := "<" + tag + ">"
	endTag := "</" + tag + ">"
	start := strings.Index(text, startTag)
	if start < 0 {
		return ""
	}
	start += len(startTag)
	end := strings.Index(text[start:], endTag)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(text[start : start+end])
}

func terminalTaskStatus(status string) bool {
	switch strings.ToLower(status) {
	case "completed", "failed", "error", "cancelled", "stopped":
		return true
	default:
		return false
	}
}

func setAgentEnd(agent *Subagent, at time.Time) {
	if at.IsZero() || (!agent.StartedAt.IsZero() && at.Before(agent.StartedAt)) ||
		(!agent.EndedAt.IsZero() && !at.Before(agent.EndedAt)) {
		return
	}
	agent.EndedAt = at
}

// isContextContinuation returns true if the entry is an injected context summary
// (first message in subagents that carries the parent session's compacted context).
func isContextContinuation(e Entry) bool {
	for _, b := range e.Content {
		if b.Type == "text" && strings.HasPrefix(b.Text, "This session is being continued from a previous conversation") {
			return true
		}
	}
	// Also check raw string content (some subagents have content as a string, not blocks)
	if len(e.Content) == 1 && e.Content[0].Text != "" {
		return strings.HasPrefix(e.Content[0].Text, "This session is being continued")
	}
	return false
}

func isHexString(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return len(s) > 0
}

func scanSubagentFile(path string) (Subagent, []lifecycleSignal) {
	name := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	id := strings.TrimPrefix(name, "agent-")

	// Extract agent type from filename: "aside_question-<hash>" → type="aside_question", id=hash
	agentType := ""
	shortID := id
	if idx := strings.LastIndex(id, "-"); idx > 0 {
		prefix := id[:idx]
		suffix := id[idx+1:]
		// If the suffix looks like a hex hash (12+ chars), the prefix is the type
		if len(suffix) >= 12 && isHexString(suffix) {
			agentType = prefix
			shortID = suffix[:8]
		} else if len(shortID) > 8 {
			shortID = shortID[:8]
		}
	} else if len(shortID) > 8 {
		shortID = shortID[:8]
	}

	agent := Subagent{ID: id, ShortID: shortID, FilePath: path, AgentType: agentType}

	// Read meta.json for agent type if available (more reliable than filename heuristic)
	metaPath := strings.TrimSuffix(path, ".jsonl") + ".meta.json"
	if metaData, err := os.ReadFile(metaPath); err == nil {
		var meta struct {
			AgentType string `json:"agentType"`
		}
		if json.Unmarshal(metaData, &meta) == nil && meta.AgentType != "" {
			agent.AgentType = meta.AgentType
		}
	}

	f, err := os.Open(path)
	if err != nil {
		return agent, nil
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	var signals []lifecycleSignal
	firstUser := true // track first user message for context-skip
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		entry, parseErr := ParseEntry(line)
		if parseErr != nil {
			continue
		}
		// A nested agent's transcript can carry the terminal signal for the child
		// it spawned; collect those here so we needn't re-read this file later.
		signals = append(signals, lifecycleSignalsFromEntry(entry)...)
		if entry.IsMeta {
			continue
		}
		if entry.Role == "user" || entry.Role == "assistant" {
			// Skip context-continuation first message for timestamp/prompt
			// (subagents inherit parent context as first user entry with old timestamp)
			if firstUser && entry.Role == "user" {
				firstUser = false
				if isContextContinuation(entry) {
					continue
				}
			}
			agent.MsgCount++
			if !entry.Timestamp.IsZero() {
				if agent.StartedAt.IsZero() {
					agent.StartedAt = entry.Timestamp
				}
				agent.Timestamp = entry.Timestamp
			}
			if agent.FirstPrompt == "" && entry.Role == "user" {
				preview := EntryPreview(entry)
				if preview != "" && preview != "(no content)" {
					agent.FirstPrompt = preview
				}
			}
		}
	}
	return agent, signals
}
