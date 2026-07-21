package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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

// LoadTodoSnapshotFromEntries returns the latest TodoWrite snapshot and whether
// any snapshot was present. The boolean distinguishes "all todos deleted" from
// transcripts that never wrote todos.
func LoadTodoSnapshotFromEntries(entries []Entry) ([]TodoItem, bool) {
	var latest []TodoItem
	found := false
	for _, entry := range entries {
		for _, block := range entry.Content {
			if block.Type != "tool_use" || block.ToolName != "TodoWrite" {
				continue
			}
			var input struct {
				Todos []TodoItem `json:"todos"`
			}
			if json.Unmarshal([]byte(block.ToolInput), &input) == nil && input.Todos != nil {
				latest = append([]TodoItem(nil), input.Todos...)
				found = true
			}
		}
	}
	return latest, found
}

// LoadTodosFromEntries returns the latest TodoWrite snapshot.
func LoadTodosFromEntries(entries []Entry) []TodoItem {
	latest, _ := LoadTodoSnapshotFromEntries(entries)
	return latest
}

var taskCreatedResultRE = regexp.MustCompile(`(?i)\bTask\s+#?([A-Za-z0-9._:-]+)\s+created successfully\b`)

// LoadTasksFromEntries extracts the latest task states from parsed conversation
// entries. Current TaskCreate inputs omit the assigned ID, so creates are held
// by tool_use_id until the matching result supplies "Task #N created successfully".
func LoadTasksFromEntries(entries []Entry) []TaskItem {
	tasks := make(map[string]*TaskItem)
	order := make([]string, 0)
	pendingCreate := make(map[string]string) // tool_use_id -> temporary task key
	synthetic := 0

	moveTask := func(from, to string) {
		if from == "" || to == "" || from == to {
			return
		}
		task := tasks[from]
		if task == nil {
			return
		}
		if existing := tasks[to]; existing != nil {
			mergeTaskItem(existing, *task)
			delete(tasks, from)
		} else {
			delete(tasks, from)
			task.ID = to
			tasks[to] = task
		}
		for i := range order {
			if order[i] == from {
				order[i] = to
				break
			}
		}
	}

	for _, e := range entries {
		for _, b := range e.Content {
			switch {
			case b.Type == "tool_use" && (b.ToolName == "TaskCreate" || b.ToolName == "TaskUpdate"):
				var input TaskItem
				var ids struct {
					ID     string `json:"id"`
					TaskID string `json:"taskId"`
				}
				if json.Unmarshal([]byte(b.ToolInput), &input) != nil || json.Unmarshal([]byte(b.ToolInput), &ids) != nil {
					continue
				}
				id := ids.ID
				if id == "" {
					id = ids.TaskID
				}
				if b.ToolName == "TaskCreate" && id == "" {
					synthetic++
					id = fmt.Sprintf("create:%s:%d", b.ID, synthetic)
					pendingCreate[b.ID] = id
				}
				if id == "" || (input.Subject == "" && b.ToolName == "TaskCreate") {
					continue
				}
				input.ID = id
				if existing := tasks[id]; existing != nil {
					mergeTaskItem(existing, input)
				} else {
					copy := input
					tasks[id] = &copy
					order = append(order, id)
				}

			case b.Type == "tool_result":
				key := pendingCreate[b.ID]
				if key == "" {
					continue
				}
				if id := taskIDFromCreateResult(b.Text); id != "" {
					moveTask(key, id)
					delete(pendingCreate, b.ID)
				}
			}
		}
	}

	result := make([]TaskItem, 0, len(order))
	seen := make(map[string]bool, len(order))
	for _, id := range order {
		if seen[id] || tasks[id] == nil {
			continue
		}
		seen[id] = true
		result = append(result, *tasks[id])
	}
	return result
}

func taskIDFromCreateResult(text string) string {
	if match := taskCreatedResultRE.FindStringSubmatch(text); len(match) == 2 {
		return match[1]
	}
	var result struct {
		ID     string `json:"id"`
		TaskID string `json:"taskId"`
	}
	if json.Unmarshal([]byte(text), &result) == nil {
		if result.ID != "" {
			return result.ID
		}
		return result.TaskID
	}
	return ""
}

func mergeTaskItem(dst *TaskItem, src TaskItem) {
	if src.Subject != "" {
		dst.Subject = src.Subject
	}
	if src.Status != "" {
		dst.Status = src.Status
	}
	if src.Description != "" {
		dst.Description = src.Description
	}
	if src.ActiveForm != "" {
		dst.ActiveForm = src.ActiveForm
	}
	if src.Blocks != nil {
		dst.Blocks = src.Blocks
	}
	if src.BlockedBy != nil {
		dst.BlockedBy = src.BlockedBy
	}
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
