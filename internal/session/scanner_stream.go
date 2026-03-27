package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// scanSessionStream uses buffered line-by-line scanning for large files to avoid OOM.
func scanSessionStream(path string, modTime time.Time, home string, badgeStore *BadgeStore) Session {
	id := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	shortID := id
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}

	sess := Session{
		ID:       id,
		ShortID:  shortID,
		FilePath: path,
		ModTime:  modTime,
	}

	dir := filepath.Dir(path)
	dirName := filepath.Base(dir)
	sess.ProjectName = decodeDirName(dirName, home)

	f, err := os.Open(path)
	if err != nil {
		return sess
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 256*1024), 10*1024*1024)

	userMsgCount := 0
	gotPrompt := false
	checkedFork := false

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}

		// Fork detection: look for "forkedFrom":{"sessionId":"<uuid>",...}
		if !checkedFork {
			if bytes.Contains(line, bForkedFrom) {
				sess.ParentSessionID = extractForkedFromSessionID(line)
				checkedFork = true
			}
		}

		isMeta := bytes.Contains(line, bIsMeta) || bytes.Contains(line, bIsMetaSpaced)
		if isMeta {
			cwd, branch := extractMetadataFast(line)
			if cwd != "" {
				sess.ProjectPath = cwd
				sess.ProjectName = ShortenPath(cwd, home)
			}
			if branch != "" {
				sess.GitBranch = branch
			}
			continue
		}

		if bytes.Contains(line, bRoleUser) || bytes.Contains(line, bRoleUserS) {
			userMsgCount++
			if !gotPrompt {
				prompt, ts := extractFirstPromptFast(line)
				if !ts.IsZero() && sess.Created.IsZero() {
					sess.Created = ts
				}
				if prompt != "" {
					sess.FirstPrompt = prompt
					gotPrompt = true
				}
			}
		}

		// Track latest non-empty todos snapshot
		if bytes.Contains(line, bTodosNonEmpty) {
			if todos := extractTodos(line); len(todos) > 0 {
				sess.Todos = todos
			}
		}

		// Extract plan slugs (collect all distinct slugs in order)
		if idx := bytes.Index(line, bSlug); idx >= 0 {
			if v := extractJSONFieldValue(line[idx+len(bSlug):]); v != "" {
				seen := false
				for _, s := range sess.PlanSlugs {
					if s == v {
						seen = true
						break
					}
				}
				if !seen {
					sess.PlanSlugs = append(sess.PlanSlugs, v)
					if sess.PlanSlug == "" {
						sess.PlanSlug = v
					}
				}
			}
		}

		// Feature detection (cheap byte-level checks, skip once detected)
		if !sess.HasCompaction {
			if bytes.Contains(line, bIsCompactSummary) || bytes.Contains(line, bIsCompactSummaryS) {
				sess.HasCompaction = true
			}
		}
		if !sess.HasSkills {
			if bytes.Contains(line, bSkillTool) || bytes.Contains(line, bSkillToolS) {
				sess.HasSkills = true
			}
		}
		if !sess.HasMCP {
			if bytes.Contains(line, bMCPTool) || bytes.Contains(line, bMCPToolS) {
				sess.HasMCP = true
			}
		}
		if !sess.HasTasks {
			if bytes.Contains(line, bTaskCreate) || bytes.Contains(line, bTaskCreateS) {
				sess.HasTasks = true
			}
		}

		// Team detection (check any line for teamName/agentName)
		if sess.TeamName == "" {
			if bytes.Contains(line, bTeamName) || bytes.Contains(line, bTeamNameS) {
				sess.TeamName = extractQuotedAfter(line, bTeamName, bTeamNameS)
			}
		}
		if sess.TeammateName == "" {
			if bytes.Contains(line, bAgentName) || bytes.Contains(line, bAgentNameS) {
				sess.TeammateName = extractQuotedAfter(line, bAgentName, bAgentNameS)
			}
		}
	}

	sess.MsgCount = userMsgCount

	// Determine team role
	if sess.TeamName != "" {
		if sess.TeammateName != "" {
			sess.TeamRole = "teammate"
		} else {
			sess.TeamRole = "leader"
		}
	}

	if sess.ProjectPath == "" {
		dirName := filepath.Base(filepath.Dir(path))
		if p := decodeProjectPath(dirName); p != "" {
			sess.ProjectPath = p
			sess.ProjectName = ShortenPath(p, home)
		} else {
			sess.ProjectPath = strings.ReplaceAll(dirName, "-", "/")
		}
	}
	if sess.ProjectPath != "" {
		sess.IsWorktree = isGitWorktree(sess.ProjectPath)
		sess.HasMemory = hasProjectMemory(sess.ProjectPath, home)
	}

	// Load richer todos from ~/.claude/todos/ files if JSONL had none
	if len(sess.Todos) == 0 {
		sess.Todos = loadFileTodos(sess.ID, home)
	}
	for _, t := range sess.Todos {
		if t.Status == "pending" || t.Status == "in_progress" {
			sess.HasTodos = true
			break
		}
	}

	// Load tasks from ~/.claude/tasks/{sessionID}/
	sess.Tasks = loadFileTasks(sess.ID, home)
	for _, t := range sess.Tasks {
		if t.Status == "pending" || t.Status == "in_progress" {
			sess.HasTasks = true
			break
		}
	}

	// Check plan file existence (any slug)
	for _, slug := range sess.PlanSlugs {
		if planFileExists(slug, home) {
			sess.HasPlan = true
			break
		}
	}

	// Check for subagents
	sess.HasAgents = hasSubagents(path)

	// Load custom badges
	if badgeStore != nil {
		sess.CustomBadges = badgeStore.Get(sess.ID)
	}

	return sess
}

func extractFirstPromptFast(line []byte) (prompt string, ts time.Time) {
	ts = extractTimestamp(line)

	prompt = extractSimpleContent(line)
	if prompt != "" {
		return prompt, ts
	}

	if bytes.Contains(line, []byte(`"content":[`)) || bytes.Contains(line, []byte(`"content": [`)) {
		entry, parseErr := ParseEntry(string(line))
		if parseErr != nil {
			return "", ts
		}
		preview := EntryPreview(entry)
		if preview != "" && preview != "(no content)" && !isSystemPrompt(preview) {
			return preview, ts
		}
	}

	return "", ts
}

func extractSimpleContent(line []byte) string {
	markers := [][]byte{[]byte(`"content":"`), []byte(`"content": "`)}
	for _, marker := range markers {
		idx := bytes.Index(line, marker)
		if idx < 0 {
			continue
		}
		start := idx + len(marker)
		if start >= len(line) {
			continue
		}
		text := extractJSONString(line[start:])
		if text == "" {
			continue
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		text = strings.ReplaceAll(text, "\n", " ")
		text = ansiRegex.ReplaceAllString(text, "")
		text = xmlTagRegex.ReplaceAllString(text, "")
		if isSystemPrompt(text) {
			return ""
		}
		if len(text) > 100 {
			text = text[:97] + "..."
		}
		return text
	}
	return ""
}

func extractMetadataFast(line []byte) (cwd, gitBranch string) {
	if idx := bytes.Index(line, bCwd); idx >= 0 {
		cwd = extractJSONFieldValue(line[idx+len(bCwd):])
	}
	if idx := bytes.Index(line, bGitBranch); idx >= 0 {
		gitBranch = extractJSONFieldValue(line[idx+len(bGitBranch):])
	}
	return cwd, gitBranch
}

func extractTodos(line []byte) []TodoItem {
	idx := bytes.Index(line, bTodosNonEmpty)
	if idx < 0 {
		return nil
	}
	start := idx + 8 // skip "todos":
	depth := 0
	for i := start; i < len(line); i++ {
		if line[i] == '[' {
			depth++
		} else if line[i] == ']' {
			depth--
			if depth == 0 {
				var todos []TodoItem
				if json.Unmarshal(line[start:i+1], &todos) == nil {
					return todos
				}
				return nil
			}
		}
	}
	return nil
}

func isSystemPrompt(s string) bool {
	prefixes := []string{"<command-", "[Request interrupted", "{\"type\""}
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
