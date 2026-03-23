package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func FindSubagents(sessionFile string) ([]Subagent, error) {
	dir := filepath.Dir(sessionFile)
	sessID := strings.TrimSuffix(filepath.Base(sessionFile), ".jsonl")
	agentDir := filepath.Join(dir, sessID, "subagents")

	if _, err := os.Stat(agentDir); os.IsNotExist(err) {
		return nil, nil
	}

	matches, err := filepath.Glob(filepath.Join(agentDir, "agent-*.jsonl"))
	if err != nil {
		return nil, err
	}

	var agents []Subagent
	for _, p := range matches {
		// Skip auto-compaction files (agent-acompact-*.jsonl)
		base := filepath.Base(p)
		if strings.HasPrefix(base, "agent-acompact-") {
			continue
		}
		agents = append(agents, scanSubagentFile(p))
	}

	sort.Slice(agents, func(i, j int) bool {
		return agents[i].Timestamp.After(agents[j].Timestamp)
	})
	return agents, nil
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

func scanSubagentFile(path string) Subagent {
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
		return agent
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 10*1024*1024)

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
	return agent
}
