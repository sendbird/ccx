package session

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EncodeProjectPath converts an absolute path to the Claude projects directory name.
// Claude replaces both '/' and '.' with '-'.
func EncodeProjectPath(path string) string {
	s := strings.ReplaceAll(path, "/", "-")
	s = strings.ReplaceAll(s, ".", "-")
	return s
}

// MoveProject moves a session's project directory to a new path.
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

	if err := rewriteCwdInDir(oldDir, oldPath, newPath); err != nil {
		return fmt.Errorf("rewrite cwd: %w", err)
	}

	if err := os.Rename(oldDir, newDir); err != nil {
		return fmt.Errorf("rename dir: %w", err)
	}

	decodedPathCache.Delete(oldEncoded)

	return nil
}

// MoveSession moves a single session's transcript (and its subagent/scratchpad
// data) from the project directory for oldPath to the project directory for
// newPath, leaving every other session under oldPath untouched. Unlike
// MoveProject, this does not rename the whole project directory.
func MoveSession(oldPath, newPath, sessionID string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("get home dir: %w", err)
	}

	oldEncoded := EncodeProjectPath(oldPath)
	newEncoded := EncodeProjectPath(newPath)
	projectsDir := filepath.Join(home, ".claude", "projects")
	oldDir := filepath.Join(projectsDir, oldEncoded)
	newDir := filepath.Join(projectsDir, newEncoded)

	oldFile := filepath.Join(oldDir, sessionID+".jsonl")
	if _, err := os.Stat(oldFile); os.IsNotExist(err) {
		return fmt.Errorf("session file not found: %s", oldFile)
	}
	newFile := filepath.Join(newDir, sessionID+".jsonl")
	if _, err := os.Stat(newFile); err == nil {
		return fmt.Errorf("target already exists: %s", newFile)
	}

	if err := os.MkdirAll(newDir, 0755); err != nil {
		return fmt.Errorf("create target project dir: %w", err)
	}

	if err := rewriteCwdInFile(oldFile, oldPath, newPath); err != nil {
		return fmt.Errorf("rewrite cwd: %w", err)
	}
	if err := os.Rename(oldFile, newFile); err != nil {
		return fmt.Errorf("rename session file: %w", err)
	}

	oldSubDir := filepath.Join(oldDir, sessionID)
	if info, err := os.Stat(oldSubDir); err == nil && info.IsDir() {
		if err := rewriteCwdInDir(oldSubDir, oldPath, newPath); err != nil {
			return fmt.Errorf("rewrite cwd in subagents: %w", err)
		}
		if err := os.Rename(oldSubDir, filepath.Join(newDir, sessionID)); err != nil {
			return fmt.Errorf("move subagents dir: %w", err)
		}
	}

	oldScratchDir := filepath.Join(ScratchpadBase(), oldEncoded, sessionID)
	if info, err := os.Stat(oldScratchDir); err == nil && info.IsDir() {
		newScratchParent := filepath.Join(ScratchpadBase(), newEncoded)
		if err := os.MkdirAll(newScratchParent, 0755); err != nil {
			return fmt.Errorf("create target scratchpad dir: %w", err)
		}
		if err := os.Rename(oldScratchDir, filepath.Join(newScratchParent, sessionID)); err != nil {
			return fmt.Errorf("move scratchpad dir: %w", err)
		}
	}

	decodedPathCache.Delete(oldEncoded)

	return nil
}

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
		return nil
	}
	return os.WriteFile(path, updated, 0644)
}

// decodeProjectPath tries to resolve an encoded directory name back to a real
// filesystem path.
func decodeProjectPath(dirName string) string {
	if !strings.HasPrefix(dirName, "-") {
		return ""
	}
	if cached, ok := decodedPathCache.Load(dirName); ok {
		return cached.(string)
	}
	parts := strings.Split(dirName[1:], "-")
	if len(parts) == 0 {
		return ""
	}

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
// Depth is limited to prevent exponential branching on long paths.
func tryResolvePath(base string, remaining []string) string {
	return tryResolvePathDepth(base, remaining, 0)
}

const maxResolveDepth = 20

func tryResolvePathDepth(base string, remaining []string, depth int) string {
	if len(remaining) == 0 {
		return base
	}
	if depth >= maxResolveDepth {
		return ""
	}

	candidate := filepath.Join(base, remaining[0])
	if info, err := os.Stat(candidate); err == nil && info.IsDir() {
		if result := tryResolvePathDepth(candidate, remaining[1:], depth+1); result != "" {
			return result
		}
	}

	if len(remaining) >= 2 {
		merged := remaining[0] + "-" + remaining[1]
		newRemaining := make([]string, 0, len(remaining)-1)
		newRemaining = append(newRemaining, merged)
		newRemaining = append(newRemaining, remaining[2:]...)
		if result := tryResolvePathDepth(base, newRemaining, depth+1); result != "" {
			return result
		}
	}

	if len(remaining) >= 2 {
		merged := remaining[0] + "." + remaining[1]
		newRemaining := make([]string, 0, len(remaining)-1)
		newRemaining = append(newRemaining, merged)
		newRemaining = append(newRemaining, remaining[2:]...)
		if result := tryResolvePathDepth(base, newRemaining, depth+1); result != "" {
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

// ListProjects returns all known project paths from ~/.claude/projects/.
// It decodes the encoded directory names back to real filesystem paths.
func ListProjects(claudeDir string) []string {
	projDir := filepath.Join(claudeDir, "projects")
	entries, err := os.ReadDir(projDir)
	if err != nil {
		return nil
	}
	var paths []string
	seen := make(map[string]bool)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := decodeProjectPath(e.Name())
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		paths = append(paths, p)
	}
	return paths
}

func isGitWorktree(projectPath string) bool {
	gitPath := filepath.Join(projectPath, ".git")
	info, err := os.Lstat(gitPath)
	if err == nil {
		return !info.IsDir()
	}
	// Fallback: detect by path pattern (worktree dir may no longer exist on disk)
	return strings.Contains(projectPath, "/.worktree/") || strings.Contains(projectPath, "/.worktrees/")
}

// ResolveBaseRepo returns the main repository root for a project path.
// For git worktrees, it reads the .git file to find the main repo's
// .git/worktrees/<name> pointer and resolves back to the repo root.
// For normal repos it returns the path unchanged.
// Falls back to path-based detection if git info isn't available.
func ResolveBaseRepo(projectPath string, worktreeDirs ...string) string {
	// Try git-based detection first: .git file in worktrees contains
	// "gitdir: /path/to/main-repo/.git/worktrees/<name>"
	gitPath := filepath.Join(projectPath, ".git")
	info, err := os.Lstat(gitPath)
	if err == nil && !info.IsDir() {
		data, err := os.ReadFile(gitPath)
		if err == nil {
			line := strings.TrimSpace(string(data))
			if strings.HasPrefix(line, "gitdir: ") {
				gitdir := line[len("gitdir: "):]
				// gitdir looks like: /path/to/repo/.git/worktrees/<name>
				// We want: /path/to/repo
				if idx := strings.Index(gitdir, "/.git/worktrees/"); idx >= 0 {
					return gitdir[:idx]
				}
			}
		}
	}

	// Fallback: path-based resolution (strip worktree dir suffix)
	return ResolveMainProjectPath(projectPath, worktreeDirs...)
}

func hasProjectMemory(projectPath, home string) bool {
	paths := []string{projectPath}
	if base := ResolveBaseRepo(projectPath); base != "" && base != projectPath {
		paths = append(paths, base)
	}
	if main := ResolveMainProjectPath(projectPath); main != "" && main != projectPath {
		paths = append(paths, main)
	}
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		encoded := EncodeProjectPath(p)
		memDir := filepath.Join(home, ".claude", "projects", encoded, "memory")
		entries, err := os.ReadDir(memDir)
		if err == nil && len(entries) > 0 {
			return true
		}
	}
	return false
}

// hasSessionScratchpad reports whether Claude Code allocated a per-session
// scratchpad directory with at least one file for this session. ReadDir only —
// bodies are not read, so the scan stays cheap.
func hasSessionScratchpad(projectPath, sessionID string) bool {
	if projectPath == "" || sessionID == "" {
		return false
	}
	dir := filepath.Join(ScratchpadBase(), EncodeProjectPath(projectPath), sessionID, "scratchpad")
	entries, err := os.ReadDir(dir)
	return err == nil && len(entries) > 0
}

func refreshSessionDerivedState(sess *Session, home string) {
	if sess == nil {
		return
	}
	if len(sess.PlanSlugs) == 0 && sess.PlanSlug != "" {
		sess.PlanSlugs = []string{sess.PlanSlug}
	}
	if sess.ProjectPath != "" {
		sess.IsWorktree = isGitWorktree(sess.ProjectPath)
		sess.HasMemory = hasProjectMemory(sess.ProjectPath, home)
		sess.HasScratchpad = hasSessionScratchpad(sess.ProjectPath, sess.ID)
	}
	if sess.FilePath != "" {
		sess.HasWorkflows = HasWorkflows(sess.FilePath)
	}
	sess.HasPlan = false
	for _, slug := range sess.PlanSlugs {
		if planFileExists(slug, home) {
			sess.HasPlan = true
			break
		}
	}
}

func hasSubagents(sessionFilePath string) bool {
	dir := filepath.Dir(sessionFilePath)
	sessID := strings.TrimSuffix(filepath.Base(sessionFilePath), ".jsonl")
	agentDir := filepath.Join(dir, sessID, "subagents")
	if _, err := os.Stat(agentDir); err != nil {
		return false
	}
	if matches, _ := filepath.Glob(filepath.Join(agentDir, "agent-*.jsonl")); len(matches) > 0 {
		return true
	}
	// Workflow-spawned agents nested one level deeper.
	wfMatches, _ := filepath.Glob(filepath.Join(agentDir, "workflows", "*", "agent-*.jsonl"))
	return len(wfMatches) > 0
}

func planFileExists(slug, home string) bool {
	if slug == "" {
		return false
	}
	path := filepath.Join(home, ".claude", "plans", slug+".md")
	_, err := os.Stat(path)
	return err == nil
}
