package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func LoadMessages(filePath string) ([]Entry, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	var entries []Entry
	// Collect hook progress entries: toolUseID → []HookInfo
	hookMap := make(map[string][]HookInfo)

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}

		entry, parseErr := ParseEntry(string(line))
		if parseErr != nil {
			continue
		}

		// Collect hook progress entries
		if entry.Type == "progress" {
			if toolID, hook, ok := parseHookProgress(line); ok {
				hookMap[toolID] = append(hookMap[toolID], hook)
			}
			continue
		}

		if entry.IsMeta || entry.Type == "file-history-snapshot" {
			continue
		}
		if entry.Role == "user" || entry.Role == "assistant" {
			entries = append(entries, entry)
		}
	}

	// Attach hooks to matching tool_use blocks
	if len(hookMap) > 0 {
		for i := range entries {
			for j := range entries[i].Content {
				if entries[i].Content[j].Type == "tool_use" && entries[i].Content[j].ID != "" {
					if hooks, ok := hookMap[entries[i].Content[j].ID]; ok {
						entries[i].Content[j].Hooks = hooks
					}
				}
			}
		}
	}

	// Attach Stop hooks to the last assistant entry's last block.
	// Stop hooks use internal UUIDs (not toolu_01... IDs) so they don't match tool_use blocks.
	var stopHooks []HookInfo
	for _, hooks := range hookMap {
		for _, h := range hooks {
			if h.Event == "Stop" {
				stopHooks = append(stopHooks, h)
			}
		}
	}
	if len(stopHooks) > 0 {
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].Role == "assistant" && len(entries[i].Content) > 0 {
				last := len(entries[i].Content) - 1
				entries[i].Content[last].Hooks = append(entries[i].Content[last].Hooks, stopHooks...)
				break
			}
		}
	}

	return entries, sc.Err()
}

// LoadMessagesSummary loads only the first headN and last tailN messages from a
// session file, returning them along with the total message count.
func LoadMessagesSummary(filePath string, headN, tailN int) (head []Entry, tail []Entry, total int, err error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	ringIdx := 0
	rawRing := make([]string, tailN)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}

		if bytes.Contains(line, bIsMeta) || bytes.Contains(line, bIsMetaSpaced) {
			continue
		}
		if bytes.Contains(line, bTypeProgress) || bytes.Contains(line, bTypeProgressS) ||
			bytes.Contains(line, bTypeFileHist) || bytes.Contains(line, bTypeFileHistS) {
			continue
		}
		hasRole := bytes.Contains(line, bRoleUser) || bytes.Contains(line, bRoleUserS) ||
			bytes.Contains(line, bRoleAsst) || bytes.Contains(line, bRoleAsstS)
		if !hasRole {
			continue
		}

		total++

		if total <= headN {
			entry, parseErr := ParseEntry(string(line))
			if parseErr != nil {
				total--
				continue
			}
			head = append(head, entry)
		}

		rawRing[ringIdx%tailN] = string(line)
		ringIdx++
	}

	if err := sc.Err(); err != nil {
		return nil, nil, 0, err
	}

	if total <= headN {
		return head, nil, total, nil
	}
	tailStart := max(total-tailN, headN)
	tailCount := total - tailStart
	tail = make([]Entry, 0, tailCount)
	for i := total - tailCount; i < total; i++ {
		raw := rawRing[i%tailN]
		if entry, parseErr := ParseEntry(raw); parseErr == nil {
			tail = append(tail, entry)
		}
	}
	return head, tail, total, nil
}

func loadFileTodos(sessionID, home string) []TodoItem {
	path := filepath.Join(home, ".claude", "todos", sessionID+"-agent-"+sessionID+".json")
	data, err := os.ReadFile(path)
	if err != nil || len(data) <= 2 {
		return nil
	}
	var todos []TodoItem
	if json.Unmarshal(data, &todos) != nil {
		return nil
	}
	return todos
}

// LoadTasksFromEntries extracts the latest task states from parsed conversation entries.
// This is used as a fallback when task files have been cleaned up from disk.
func LoadTasksFromEntries(entries []Entry) []TaskItem {
	tasks := make(map[string]*TaskItem)
	for _, e := range entries {
		for _, b := range e.Content {
			if b.Type != "tool_use" {
				continue
			}
			if b.ToolName != "TaskCreate" && b.ToolName != "TaskUpdate" {
				continue
			}
			// Parse task data from tool input JSON
			var input struct {
				ID          string `json:"id"`
				Subject     string `json:"subject"`
				Status      string `json:"status"`
				Description string `json:"description"`
			}
			if json.Unmarshal([]byte(b.ToolInput), &input) != nil || input.Subject == "" {
				continue
			}
			if existing, ok := tasks[input.ID]; ok {
				// Update existing task
				if input.Status != "" {
					existing.Status = input.Status
				}
				if input.Subject != "" {
					existing.Subject = input.Subject
				}
				if input.Description != "" {
					existing.Description = input.Description
				}
			} else {
				tasks[input.ID] = &TaskItem{
					ID:          input.ID,
					Subject:     input.Subject,
					Status:      input.Status,
					Description: input.Description,
				}
			}
		}
	}
	var result []TaskItem
	for _, t := range tasks {
		result = append(result, *t)
	}
	return result
}

func LoadCronsFromEntries(entries []Entry) []CronItem {
	crons := make(map[string]*CronItem)
	var order []string
	pendingCreateByToolUse := make(map[string]*CronItem)

	for _, e := range entries {
		for _, b := range e.Content {
			if b.Type == "tool_use" {
				switch b.ToolName {
				case "CronCreate":
					var input struct {
						Cron      string `json:"cron"`
						Prompt    string `json:"prompt"`
						Recurring bool   `json:"recurring"`
					}
					if json.Unmarshal([]byte(b.ToolInput), &input) != nil {
						continue
					}
					pendingCreateByToolUse[b.ID] = &CronItem{
						Cron:      input.Cron,
						Prompt:    input.Prompt,
						Recurring: input.Recurring,
						Status:    "active",
						CreatedAt: e.Timestamp,
					}
				case "CronDelete":
					var input struct {
						ID string `json:"id"`
					}
					if json.Unmarshal([]byte(b.ToolInput), &input) != nil || input.ID == "" {
						continue
					}
					cron := crons[input.ID]
					if cron == nil {
						cron = &CronItem{ID: input.ID}
						crons[input.ID] = cron
						order = append(order, input.ID)
					}
					cron.Status = "deleted"
					cron.DeletedAt = e.Timestamp
				}
				continue
			}
			if b.Type != "tool_result" || b.ID == "" {
				continue
			}
			pending := pendingCreateByToolUse[b.ID]
			if pending == nil {
				continue
			}
			delete(pendingCreateByToolUse, b.ID)
			id := extractCronID(b.Text)
			if id == "" {
				id = pending.ID
			}
			if id == "" {
				continue
			}
			existing := crons[id]
			if existing == nil {
				pending.ID = id
				crons[id] = pending
				order = append(order, id)
				continue
			}
			if pending.Cron != "" {
				existing.Cron = pending.Cron
			}
			if pending.Prompt != "" {
				existing.Prompt = pending.Prompt
			}
			existing.Recurring = pending.Recurring
			if existing.CreatedAt.IsZero() {
				existing.CreatedAt = pending.CreatedAt
			}
			if existing.Status == "" {
				existing.Status = "active"
			}
		}
	}

	var result []CronItem
	for _, id := range order {
		if cron := crons[id]; cron != nil {
			if cron.Status == "" {
				cron.Status = "active"
			}
			result = append(result, *cron)
		}
	}
	return result
}

func extractCronID(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	var payload struct {
		ID string `json:"id"`
	}
	if json.Unmarshal([]byte(text), &payload) == nil && payload.ID != "" {
		return payload.ID
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "id:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		}
		if strings.Contains(line, "\"id\"") {
			if json.Unmarshal([]byte(line), &payload) == nil && payload.ID != "" {
				return payload.ID
			}
		}
	}
	return ""
}

func loadFileTasks(sessionID, home string) []TaskItem {
	dir := filepath.Join(home, ".claude", "tasks", sessionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var tasks []TaskItem
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || e.Name() == ".lock" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var t TaskItem
		if json.Unmarshal(data, &t) == nil && t.Subject != "" {
			tasks = append(tasks, t)
		}
	}
	return tasks
}
