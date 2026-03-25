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
	return strings.Contains(projectPath, "/.worktree/")
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

func planFileExists(slug, home string) bool {
	if slug == "" {
		return false
	}
	path := filepath.Join(home, ".claude", "plans", slug+".md")
	_, err := os.Stat(path)
	return err == nil
}
