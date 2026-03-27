package remote

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CreateConfigTarball creates a tar.gz archive of the full Claude config.
// Includes settings, memory, skills, agents, commands, hooks, project config,
// and optionally the session JSONL file for --resume.
// remoteWorkDir is the working directory on the pod (e.g. "/workspace").
func CreateConfigTarball(claudeDir, projectPath, sessionFile, remoteWorkDir string) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	// Core config files (except .claude.json which needs modification)
	coreFiles := []string{
		"CLAUDE.md",
		"settings.json",
		"settings.local.json",
	}
	for _, name := range coreFiles {
		addFileToTar(tw, filepath.Join(claudeDir, name), ".claude/"+name)
	}

	// .claude.json: inject trust entry for remote workdir
	// Claude reads from both ~/.claude/.claude.json AND ~/.claude.json
	addClaudeJSON(tw, filepath.Join(claudeDir, ".claude.json"), remoteWorkDir)

	// Directories to mirror fully: skills, agents, commands, contexts, rules, memory
	dirs := []string{"skills", "agents", "commands", "contexts", "rules", "memory"}
	for _, dir := range dirs {
		addDirToTar(tw, filepath.Join(claudeDir, dir), ".claude/"+dir)
	}

	// Project-specific config (CLAUDE.md, memory, etc.)
	if projectPath != "" {
		encoded := encodeProjectPath(projectPath)
		projDir := filepath.Join(claudeDir, "projects", encoded)
		addDirToTar(tw, projDir, ".claude/projects/"+encoded)
	}

	// Session JSONL file (for --resume)
	// Must be placed under the REMOTE workdir's encoded path, not the local one.
	if sessionFile != "" && remoteWorkDir != "" {
		remoteEncoded := encodeProjectPath(remoteWorkDir)
		sessionFileName := filepath.Base(sessionFile)
		remotePath := ".claude/projects/" + remoteEncoded + "/" + sessionFileName
		addFileToTar(tw, sessionFile, remotePath)
	}

	tw.Close()
	gw.Close()
	return buf.Bytes(), nil
}

const (
	maxTarballSize = 500 * 1024 * 1024 // 500MB
	maxFileCount   = 50000
)

// ValidateWorkdir checks if the directory is safe to sync.
// Rejects root, home, and other dangerous paths.
func ValidateWorkdir(dir string) error {
	home, _ := os.UserHomeDir()
	abs, _ := filepath.Abs(dir)

	dangerous := []string{"/", "/etc", "/usr", "/var", "/tmp", "/opt", "/bin", "/sbin"}
	for _, d := range dangerous {
		if abs == d {
			return fmt.Errorf("refusing to sync dangerous path: %s", abs)
		}
	}
	if abs == home {
		return fmt.Errorf("refusing to sync home directory: %s (use a project subdirectory)", abs)
	}

	// Must be a git repo or have a reasonable structure
	gitDir := filepath.Join(abs, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		// Not a git repo — check file count to be safe
		count := 0
		filepath.Walk(abs, func(_ string, info os.FileInfo, _ error) error {
			if info != nil && !info.IsDir() {
				count++
			}
			if count > maxFileCount {
				return fmt.Errorf("too many files")
			}
			return nil
		})
		if count > maxFileCount {
			return fmt.Errorf("directory has >%d files and is not a git repo — too risky to sync", maxFileCount)
		}
	}

	return nil
}

// CreateWorkdirTarball creates a tar.gz of the local working directory.
// Respects .gitignore by using git ls-files if available, otherwise walks the dir.
// Returns error if the directory is dangerous or the tarball exceeds size limits.
func CreateWorkdirTarball(localDir string) ([]byte, error) {
	if err := ValidateWorkdir(localDir); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	// Try git ls-files for .gitignore-aware listing
	files, err := gitListFiles(localDir)
	if err != nil {
		// Fallback: walk directory (skip .git, node_modules, etc.)
		files, err = walkDir(localDir)
		if err != nil {
			gw.Close()
			return nil, err
		}
	}

	if len(files) > maxFileCount {
		gw.Close()
		return nil, fmt.Errorf("too many files (%d > %d limit)", len(files), maxFileCount)
	}

	for _, relPath := range files {
		absPath := filepath.Join(localDir, relPath)
		addFileToTar(tw, absPath, relPath)
		if buf.Len() > maxTarballSize {
			tw.Close()
			gw.Close()
			return nil, fmt.Errorf("tarball exceeds %dMB limit", maxTarballSize/1024/1024)
		}
	}

	tw.Close()
	gw.Close()
	return buf.Bytes(), nil
}

// addClaudeJSON reads .claude.json, injects a trust entry for the remote workdir,
// and writes the modified version to the tarball.
func addClaudeJSON(tw *tar.Writer, srcPath, remoteWorkDir string) {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return
	}

	var config map[string]interface{}
	if json.Unmarshal(data, &config) != nil {
		addFileToTar(tw, srcPath, ".claude/.claude.json")
		return
	}

	projects, _ := config["projects"].(map[string]interface{})
	if projects == nil {
		projects = make(map[string]interface{})
		config["projects"] = projects
	}

	if remoteWorkDir != "" {
		if _, exists := projects[remoteWorkDir]; !exists {
			projects[remoteWorkDir] = map[string]interface{}{
				"allowedTools":                  []interface{}{},
				"hasTrustDialogAccepted":        true,
				"hasCompletedProjectOnboarding": true,
				"projectOnboardingSeenCount":    1,
			}
		}
	}

	modified, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		addFileToTar(tw, srcPath, ".claude/.claude.json")
		return
	}

	// Write to both locations — Claude reads ~/.claude.json (root) and ~/.claude/.claude.json
	for _, name := range []string{".claude.json", ".claude/.claude.json"} {
		header := &tar.Header{
			Name: name,
			Size: int64(len(modified)),
			Mode: 0644,
		}
		if tw.WriteHeader(header) == nil {
			tw.Write(modified)
		}
	}
}

// UploadTarball extracts a tarball into a directory on the pod.
func UploadTarball(ctx context.Context, cfg Config, podName, container, destDir string, tarball []byte) error {
	cmd := exec.CommandContext(ctx, "kubectl",
		"--context", cfg.Context,
		"-n", cfg.Namespace,
		"exec", "-i", podName, "-c", container,
		"--", "tar", "xzf", "-", "-C", destDir)
	cmd.Stdin = bytes.NewReader(tarball)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("upload: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func addFileToTar(tw *tar.Writer, srcPath, tarName string) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.IsDir() {
		return nil
	}

	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = tarName

	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}

func addDirToTar(tw *tar.Writer, srcDir, tarPrefix string) {
	filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(srcDir, path)
		return addFileToTar(tw, path, tarPrefix+"/"+rel)
	})
}

func gitListFiles(dir string) ([]string, error) {
	cmd := exec.Command("git", "-C", dir, "ls-files", "-co", "--exclude-standard")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true,
	".worktree": true, "tmp": true, "__pycache__": true,
	".next": true, "dist": true, "build": true,
}

func walkDir(dir string) ([]string, error) {
	var files []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		// Skip large files (>10MB)
		if info.Size() > 10*1024*1024 {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		files = append(files, rel)
		return nil
	})
	return files, err
}

func encodeProjectPath(path string) string {
	s := strings.ReplaceAll(path, "/", "-")
	s = strings.ReplaceAll(s, ".", "-")
	if len(s) > 0 && s[0] == '-' {
		s = s[1:]
	}
	return s
}
