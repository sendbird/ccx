package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MemoryFile represents a memory file from a project's memory directory.
type MemoryFile struct {
	Name     string // filename (e.g., "user_role.md")
	Path     string // full path
	Size     int64
	ExistsIn bool // true if a file with the same name exists in the target
}

// ListProjectMemory returns all .md files in a project's memory directory.
func ListProjectMemory(projectPath string) ([]MemoryFile, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	encoded := EncodeProjectPath(projectPath)
	memDir := filepath.Join(home, ".claude", "projects", encoded, "memory")
	return listMemoryDir(memDir)
}

// ListProjectMemoryConflicts lists memory files from src and marks which ones
// already exist in dst project.
func ListProjectMemoryConflicts(srcPath, dstPath string) ([]MemoryFile, error) {
	files, err := ListProjectMemory(srcPath)
	if err != nil {
		return nil, err
	}

	home, _ := os.UserHomeDir()
	dstEncoded := EncodeProjectPath(dstPath)
	dstMemDir := filepath.Join(home, ".claude", "projects", dstEncoded, "memory")

	for i, f := range files {
		dstFile := filepath.Join(dstMemDir, f.Name)
		if _, err := os.Stat(dstFile); err == nil {
			files[i].ExistsIn = true
		}
	}
	return files, nil
}

// ImportMemoryFiles copies selected memory files from src project to dst project.
// If a file already exists in dst, it is overwritten.
// Returns the number of files copied.
func ImportMemoryFiles(srcPath, dstPath string, fileNames []string) (int, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0, err
	}

	srcEncoded := EncodeProjectPath(srcPath)
	dstEncoded := EncodeProjectPath(dstPath)
	srcMemDir := filepath.Join(home, ".claude", "projects", srcEncoded, "memory")
	dstMemDir := filepath.Join(home, ".claude", "projects", dstEncoded, "memory")

	// Ensure dst memory dir exists
	if err := os.MkdirAll(dstMemDir, 0755); err != nil {
		return 0, fmt.Errorf("create memory dir: %w", err)
	}

	copied := 0
	for _, name := range fileNames {
		srcFile := filepath.Join(srcMemDir, name)
		dstFile := filepath.Join(dstMemDir, name)

		data, err := os.ReadFile(srcFile)
		if err != nil {
			continue
		}
		if err := os.WriteFile(dstFile, data, 0644); err != nil {
			return copied, fmt.Errorf("write %s: %w", name, err)
		}
		copied++
	}

	// Update MEMORY.md index in dst if it exists
	if copied > 0 {
		updateMemoryIndex(dstMemDir, fileNames)
	}

	return copied, nil
}

// ResolveMainProjectPath returns the main (non-worktree) project path for a
// worktree project path. It strips the worktree dir suffix (e.g. .worktree/{name}).
// Returns the path unchanged if the pattern is not found.
func ResolveMainProjectPath(worktreePath string, worktreeDirs ...string) string {
	patterns := []string{".worktree", ".worktrees"}
	for _, dir := range worktreeDirs {
		if dir == "" {
			continue
		}
		found := false
		for _, existing := range patterns {
			if existing == dir {
				found = true
				break
			}
		}
		if !found {
			patterns = append(patterns, dir)
		}
	}
	for _, dir := range patterns {
		needle := "/" + dir + "/"
		if idx := strings.Index(worktreePath, needle); idx >= 0 {
			return worktreePath[:idx]
		}
	}
	return worktreePath
}

// DeleteMemoryFiles removes memory files from a project's memory directory.
// Returns the number of files deleted.
func DeleteMemoryFiles(projectPath string, fileNames []string) (int, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return 0, err
	}

	encoded := EncodeProjectPath(projectPath)
	memDir := filepath.Join(home, ".claude", "projects", encoded, "memory")

	deleted := 0
	for _, name := range fileNames {
		path := filepath.Join(memDir, name)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			continue
		}
		deleted++
	}
	return deleted, nil
}

func listMemoryDir(dir string) ([]MemoryFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var files []MemoryFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		if strings.EqualFold(e.Name(), "MEMORY.md") {
			continue // skip index file
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, MemoryFile{
			Name: e.Name(),
			Path: filepath.Join(dir, e.Name()),
			Size: info.Size(),
		})
	}
	return files, nil
}

// updateMemoryIndex appends entries to MEMORY.md for newly imported files.
func updateMemoryIndex(memDir string, newFiles []string) {
	indexPath := filepath.Join(memDir, "MEMORY.md")
	existing, _ := os.ReadFile(indexPath)
	content := string(existing)

	for _, name := range newFiles {
		// Skip if already referenced
		if strings.Contains(content, name) {
			continue
		}
		entry := fmt.Sprintf("- [%s](./%s)\n", strings.TrimSuffix(name, ".md"), name)
		content += entry
	}

	os.WriteFile(indexPath, []byte(content), 0644)
}
