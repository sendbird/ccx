package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	bRoleUser      = []byte(`"role":"user"`)
	bRoleUserS     = []byte(`"role": "user"`)
	bIsMeta        = []byte(`"isMeta":true`)
	bIsMetaSpaced  = []byte(`"isMeta": true`)
	bCwd           = []byte(`"cwd"`)
	bGitBranch     = []byte(`"gitBranch"`)
	bTodosNonEmpty = []byte(`"todos":[{`)
	bSlug          = []byte(`"slug"`)
	bTypeProgress  = []byte(`"type":"progress"`)
	bTypeProgressS = []byte(`"type": "progress"`)
	bTypeFileHist  = []byte(`"type":"file-history-snapshot"`)
	bTypeFileHistS = []byte(`"type": "file-history-snapshot"`)
	bTeamName      = []byte(`"teamName":"`)
	bTeamNameS     = []byte(`"teamName": "`)
	bAgentName     = []byte(`"agentName":"`)
	bAgentNameS    = []byte(`"agentName": "`)

	// Feature detection markers
	bIsCompactSummary  = []byte(`"isCompactSummary":true`)
	bIsCompactSummaryS = []byte(`"isCompactSummary": true`)
	bSkillTool         = []byte(`"name":"Skill"`)
	bSkillToolS        = []byte(`"name": "Skill"`)
	bMCPTool           = []byte(`"name":"mcp__`)
	bMCPToolS          = []byte(`"name": "mcp__`)
	bTaskCreate        = []byte(`"name":"TaskCreate"`)
	bTaskCreateS       = []byte(`"name": "TaskCreate"`)

	decodedPathCache sync.Map // dirName → decoded path (string, "" if unresolvable)
)

// ScanSessions scans for Claude Code sessions. If claudeDir is empty,
// defaults to ~/.claude.
func ScanSessions(claudeDir string) ([]Session, error) {
	if claudeDir == "" {
		claudeDir = os.Getenv("CLAUDE_CONFIG_DIR")
	}
	if claudeDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("get home dir: %w", err)
		}
		claudeDir = filepath.Join(home, ".claude")
	}

	// Derive home dir for path decoding (claudeDir is typically ~/.claude)
	home := filepath.Dir(claudeDir)

	projectsDir := filepath.Join(claudeDir, "projects")
	if _, statErr := os.Stat(projectsDir); os.IsNotExist(statErr) {
		return nil, nil
	}

	type fileEntry struct {
		path    string
		modTime time.Time
		size    int64
	}
	var files []fileEntry
	var err error
	err = filepath.Walk(projectsDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if info.IsDir() {
			if info.Name() == "subagents" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".jsonl") || strings.HasPrefix(info.Name(), "agent-") {
			return nil
		}
		files = append(files, fileEntry{path: path, modTime: info.ModTime(), size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk projects dir: %w", err)
	}

	const numWorkers = 12
	fileCh := make(chan fileEntry, len(files))
	resultCh := make(chan Session, len(files))

	var wg sync.WaitGroup
	for range numWorkers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for fe := range fileCh {
				sess := scanSessionStream(fe.path, fe.modTime, home)
				if sess.MsgCount > 0 {
					resultCh <- sess
				}
			}
		}()
	}

	for _, f := range files {
		fileCh <- f
	}
	close(fileCh)

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	sessions := make([]Session, 0, len(files))
	for sess := range resultCh {
		sessions = append(sessions, sess)
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModTime.After(sessions[j].ModTime)
	})

	return sessions, nil
}

// scanSessionStream uses buffered line-by-line scanning for large files to avoid OOM.
func scanSessionStream(path string, modTime time.Time, home string) Session {
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

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
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
		// Fallback: decode the project directory name back to a real path.
		// The dir name is EncodeProjectPath(cwd) which replaces / and . with -.
		// We resolve ambiguity by walking the filesystem.
		dirName := filepath.Base(filepath.Dir(path))
		if p := decodeProjectPath(dirName); p != "" {
			sess.ProjectPath = p
			sess.ProjectName = ShortenPath(p, home)
		} else {
			// Directory may not exist (renamed/deleted). Derive path from dir name.
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

	return sess
}

func extractFirstPromptFast(line []byte) (prompt string, ts time.Time) {
	// Extract timestamp
	ts = extractTimestamp(line)

	// Try simple string content: "content":"text..."
	prompt = extractSimpleContent(line)
	if prompt != "" {
		return prompt, ts
	}

	// Array content: fall back to full parse
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

func extractTimestamp(line []byte) time.Time {
	markers := [][]byte{[]byte(`"timestamp":"`), []byte(`"timestamp": "`)}
	for _, marker := range markers {
		idx := bytes.Index(line, marker)
		if idx < 0 {
			continue
		}
		start := idx + len(marker)
		if start+40 > len(line) {
			continue
		}
		end := bytes.IndexByte(line[start:], '"')
		if end <= 0 || end > 40 {
			continue
		}
		tsStr := string(line[start : start+end])
		if t, err := time.Parse(time.RFC3339Nano, tsStr); err == nil {
			return t
		}
		if t, err := time.Parse(time.RFC3339, tsStr); err == nil {
			return t
		}
	}
	return time.Time{}
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
		// Make sure this isn't array content
		// The marker already ends with `"` so we're inside the string value
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

func extractJSONString(b []byte) string {
	var buf []byte
	limit := min(len(b), 200)
	for i := 0; i < limit; i++ {
		if b[i] == '\\' && i+1 < limit {
			next := b[i+1]
			switch next {
			case '"':
				buf = append(buf, '"')
			case '\\':
				buf = append(buf, '\\')
			case 'n':
				buf = append(buf, '\n')
			case 't':
				buf = append(buf, '\t')
			case 'r':
				buf = append(buf, '\r')
			default:
				buf = append(buf, '\\', next)
			}
			i++
			continue
		}
		if b[i] == '"' {
			return string(buf)
		}
		buf = append(buf, b[i])
	}
	if len(buf) > 0 {
		return string(buf)
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

func extractJSONFieldValue(b []byte) string {
	i := 0
	for i < len(b) && b[i] != ':' {
		i++
	}
	i++ // skip colon
	for i < len(b) && (b[i] == ' ' || b[i] == '\t') {
		i++
	}
	if i >= len(b) || b[i] != '"' {
		return ""
	}
	i++ // skip opening quote
	start := i
	for i < len(b) {
		if b[i] == '\\' {
			i += 2
			continue
		}
		if b[i] == '"' {
			return string(b[start:i])
		}
		i++
	}
	return ""
}

// extractQuotedAfter finds one of the markers in line and returns the quoted string value after it.
func extractQuotedAfter(line []byte, markers ...[]byte) string {
	for _, m := range markers {
		idx := bytes.Index(line, m)
		if idx < 0 {
			continue
		}
		start := idx + len(m) // points right after the opening quote
		end := bytes.IndexByte(line[start:], '"')
		if end > 0 {
			return string(line[start : start+end])
		}
	}
	return ""
}

func extractTodos(line []byte) []TodoItem {
	idx := bytes.Index(line, bTodosNonEmpty)
	if idx < 0 {
		return nil
	}
	start := idx + 8 // skip `"todos":`
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

// EncodeProjectPath converts an absolute path to the Claude projects directory name.
// Claude replaces both '/' and '.' with '-'.
func EncodeProjectPath(path string) string {
	s := strings.ReplaceAll(path, "/", "-")
	s = strings.ReplaceAll(s, ".", "-")
	return s
}

// MoveProject moves a session's project directory to a new path.
// It renames the ~/.claude/projects/<encoded>/ directory and rewrites
// the "cwd" field in all JSONL files (including subagent dirs).
func MoveProject(oldPath, newPath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home dir: %w", err)
	}

	oldEncoded := EncodeProjectPath(oldPath)
	newEncoded := EncodeProjectPath(newPath)
	projectsDir := filepath.Join(home, ".claude", "projects")
	oldDir := filepath.Join(projectsDir, oldEncoded)
	newDir := filepath.Join(projectsDir, newEncoded)

	if _, err := os.Stat(oldDir); os.IsNotExist(err) {
		return fmt.Errorf("project dir not found: %s", oldDir)
	}
	if _, err := os.Stat(newDir); err == nil {
		return fmt.Errorf("target already exists: %s", newDir)
	}

	// Rewrite "cwd" in all JSONL files under oldDir
	if err := rewriteCwdInDir(oldDir, oldPath, newPath); err != nil {
		return fmt.Errorf("rewrite cwd: %w", err)
	}

	// Rename the directory
	if err := os.Rename(oldDir, newDir); err != nil {
		return fmt.Errorf("rename dir: %w", err)
	}

	// Clear decoded path cache since paths changed
	decodedPathCache.Delete(oldEncoded)

	return nil
}

// rewriteCwdInDir replaces old cwd with new cwd in all JSONL files under dir.
func rewriteCwdInDir(dir, oldCwd, newCwd string) error {
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		return rewriteCwdInFile(path, oldCwd, newCwd)
	})
}

func rewriteCwdInFile(path, oldCwd, newCwd string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	oldPattern := []byte(`"cwd":"` + oldCwd + `"`)
	newPattern := []byte(`"cwd":"` + newCwd + `"`)
	oldPatternSpaced := []byte(`"cwd": "` + oldCwd + `"`)
	newPatternSpaced := []byte(`"cwd": "` + newCwd + `"`)

	updated := bytes.ReplaceAll(data, oldPattern, newPattern)
	updated = bytes.ReplaceAll(updated, oldPatternSpaced, newPatternSpaced)

	if bytes.Equal(data, updated) {
		return nil // no changes needed
	}
	return os.WriteFile(path, updated, 0644)
}

func loadFileTodos(sessionID, home string) []TodoItem {
	path := filepath.Join(home, ".claude", "todos", sessionID+"-agent-"+sessionID+".json")
	data, err := os.ReadFile(path)
	if err != nil || len(data) <= 2 { // "[]" is 2 bytes
		return nil
	}
	var todos []TodoItem
	if json.Unmarshal(data, &todos) != nil {
		return nil
	}
	return todos
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

func planFileExists(slug, home string) bool {
	if slug == "" {
		return false
	}
	path := filepath.Join(home, ".claude", "plans", slug+".md")
	_, err := os.Stat(path)
	return err == nil
}

func hasProjectMemory(projectPath, home string) bool {
	encoded := EncodeProjectPath(projectPath)
	memDir := filepath.Join(home, ".claude", "projects", encoded, "memory")
	entries, err := os.ReadDir(memDir)
	return err == nil && len(entries) > 0
}

func hasSubagents(sessionFilePath string) bool {
	dir := filepath.Dir(sessionFilePath)
	sessID := strings.TrimSuffix(filepath.Base(sessionFilePath), ".jsonl")
	agentDir := filepath.Join(dir, sessID, "subagents")
	if _, err := os.Stat(agentDir); err != nil {
		return false
	}
	matches, _ := filepath.Glob(filepath.Join(agentDir, "agent-*.jsonl"))
	return len(matches) > 0
}

func isGitWorktree(projectPath string) bool {
	gitPath := filepath.Join(projectPath, ".git")
	info, err := os.Lstat(gitPath)
	if err != nil {
		return false
	}
	return !info.IsDir()
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

// decodeProjectPath tries to resolve an encoded directory name back to a real
// filesystem path. The encoding replaces both '/' and '.' with '-', so the
// decode is ambiguous. We resolve by walking the filesystem segment by segment:
// for each '-', try '/' first (new segment), then '.' (dot in current segment).
func decodeProjectPath(dirName string) string {
	if !strings.HasPrefix(dirName, "-") {
		return ""
	}
	if cached, ok := decodedPathCache.Load(dirName); ok {
		return cached.(string)
	}
	// Split on '-' to get candidate segments. First element is empty (leading '-').
	parts := strings.Split(dirName[1:], "-") // e.g. "Users","gavin","jeong","src",...
	if len(parts) == 0 {
		return ""
	}

	// Recursively try building a valid path
	result := tryResolvePath("/", parts)
	if result != "" {
		if info, err := os.Stat(result); err == nil && info.IsDir() {
			decodedPathCache.Store(dirName, result)
			return result
		}
	}
	decodedPathCache.Store(dirName, "")
	return ""
}

// tryResolvePath recursively resolves path segments.
// For each '-' boundary, it tries: '/' (new dir), '-' (literal hyphen), '.' (dot).
func tryResolvePath(base string, remaining []string) string {
	if len(remaining) == 0 {
		return base
	}

	// Try appending next segment as a new directory component (was '/')
	candidate := filepath.Join(base, remaining[0])
	if info, err := os.Stat(candidate); err == nil && info.IsDir() {
		if result := tryResolvePath(candidate, remaining[1:]); result != "" {
			return result
		}
	}

	// Try merging with '-' (the '-' was a literal hyphen in the name)
	if len(remaining) >= 2 {
		merged := remaining[0] + "-" + remaining[1]
		newRemaining := make([]string, 0, len(remaining)-1)
		newRemaining = append(newRemaining, merged)
		newRemaining = append(newRemaining, remaining[2:]...)
		if result := tryResolvePath(base, newRemaining); result != "" {
			return result
		}
	}

	// Try merging with '.' (the '-' was originally a '.')
	if len(remaining) >= 2 {
		merged := remaining[0] + "." + remaining[1]
		newRemaining := make([]string, 0, len(remaining)-1)
		newRemaining = append(newRemaining, merged)
		newRemaining = append(newRemaining, remaining[2:]...)
		if result := tryResolvePath(base, newRemaining); result != "" {
			return result
		}
	}

	return ""
}

func decodeDirName(dirName, home string) string {
	if !strings.HasPrefix(dirName, "-") {
		return dirName
	}
	decoded := strings.ReplaceAll(dirName, "-", "/")
	if strings.HasPrefix(decoded, "/Users/") {
		return ShortenPath(decoded, home)
	}
	return decoded
}

func ShortenPath(path, home string) string {
	if strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}

func LoadMessages(filePath string) ([]Entry, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	var entries []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}

		entry, parseErr := ParseEntry(line)
		if parseErr != nil {
			continue
		}

		if entry.IsMeta || entry.Type == "progress" || entry.Type == "file-history-snapshot" {
			continue
		}
		if entry.Role == "user" || entry.Role == "assistant" {
			entries = append(entries, entry)
		}
	}
	return entries, sc.Err()
}

// LoadMessagesSummary loads only the first headN and last tailN messages from a
// session file, returning them along with the total message count. This avoids
// parsing the entire file for preview purposes.
func LoadMessagesSummary(filePath string, headN, tailN int) (head []Entry, tail []Entry, total int, err error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 10*1024*1024)

	ringIdx := 0

	// Keep raw lines for tail in a string ring buffer to defer parsing
	rawRing := make([]string, tailN)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}

		// Fast skip: meta, progress, file-history-snapshot, and non-message
		// lines without full JSON parse. Must match LoadMessages filtering
		// so that entry indices are consistent between summary and full load.
		if bytes.Contains(line, bIsMeta) || bytes.Contains(line, bIsMetaSpaced) {
			continue
		}
		if bytes.Contains(line, bTypeProgress) || bytes.Contains(line, bTypeProgressS) ||
			bytes.Contains(line, bTypeFileHist) || bytes.Contains(line, bTypeFileHistS) {
			continue
		}
		hasRole := bytes.Contains(line, bRoleUser) || bytes.Contains(line, bRoleUserS) ||
			bytes.Contains(line, []byte(`"role":"assistant"`)) || bytes.Contains(line, []byte(`"role": "assistant"`))
		if !hasRole {
			continue
		}

		total++

		// Fully parse only head entries
		if total <= headN {
			entry, parseErr := ParseEntry(string(line))
			if parseErr != nil {
				total--
				continue
			}
			head = append(head, entry)
		}

		// Store raw line for tail (cheap - no parsing)
		rawRing[ringIdx%tailN] = string(line)
		ringIdx++
	}

	if err := sc.Err(); err != nil {
		return nil, nil, 0, err
	}

	// Extract tail from ring buffer (avoid duplicating head entries)
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
		agents = append(agents, scanSubagentFile(p))
	}

	sort.Slice(agents, func(i, j int) bool {
		return agents[i].Timestamp.After(agents[j].Timestamp)
	})
	return agents, nil
}

func scanSubagentFile(path string) Subagent {
	name := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	id := strings.TrimPrefix(name, "agent-")
	shortID := id
	if len(shortID) > 8 {
		shortID = shortID[:8]
	}

	agent := Subagent{ID: id, ShortID: shortID, FilePath: path}

	f, err := os.Open(path)
	if err != nil {
		return agent
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 10*1024*1024)

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
			agent.MsgCount++
			if agent.Timestamp.IsZero() && !entry.Timestamp.IsZero() {
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
