package remote

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
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
func CreateConfigTarball(claudeDir, projectPath, sessionFile string) ([]byte, error) {
	home, _ := os.UserHomeDir()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	// Core config files
	coreFiles := []string{
		"CLAUDE.md",
		"settings.json",
		"settings.local.json",
		".claude.json",
	}
	for _, name := range coreFiles {
		addFileToTar(tw, filepath.Join(claudeDir, name), ".claude/"+name)
	}

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
	if sessionFile != "" {
		// Session files live under ~/.claude/projects/<encoded>/sessions/
		// We need to preserve the relative path from claudeDir
		rel, err := filepath.Rel(home, sessionFile)
		if err == nil {
			addFileToTar(tw, sessionFile, rel)
		}
	}

	tw.Close()
	gw.Close()
	return buf.Bytes(), nil
}

// CreateWorkdirTarball creates a tar.gz of the local working directory.
// Respects .gitignore by using git ls-files if available, otherwise walks the dir.
func CreateWorkdirTarball(localDir string) ([]byte, error) {
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

	for _, relPath := range files {
		absPath := filepath.Join(localDir, relPath)
		addFileToTar(tw, absPath, relPath)
	}

	tw.Close()
	gw.Close()
	return buf.Bytes(), nil
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
