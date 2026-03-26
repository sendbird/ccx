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

// CreateConfigTarball creates a tar.gz archive of Claude config files.
func CreateConfigTarball(claudeDir, projectPath string) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	// Files to include from ~/.claude/
	files := []string{
		"CLAUDE.md",
		"settings.json",
		"settings.local.json",
		".claude.json",
	}
	for _, name := range files {
		path := filepath.Join(claudeDir, name)
		if err := addFileToTar(tw, path, ".claude/"+name); err != nil {
			continue // skip missing files
		}
	}

	// Project-specific config
	if projectPath != "" {
		encoded := encodeProjectPath(projectPath)
		projDir := filepath.Join(claudeDir, "projects", encoded)

		// Project CLAUDE.md
		addFileToTar(tw, filepath.Join(projDir, "CLAUDE.md"), ".claude/projects/"+encoded+"/CLAUDE.md")

		// Project memory files
		memDir := filepath.Join(projDir, "memory")
		entries, err := os.ReadDir(memDir)
		if err == nil {
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				src := filepath.Join(memDir, e.Name())
				dst := ".claude/projects/" + encoded + "/memory/" + e.Name()
				addFileToTar(tw, src, dst)
			}
		}
	}

	tw.Close()
	gw.Close()
	return buf.Bytes(), nil
}

// UploadConfig uploads the config tarball to the pod.
func UploadConfig(ctx context.Context, cfg Config, podName string, tarball []byte) error {
	cmd := exec.CommandContext(ctx, "kubectl",
		"--context", cfg.Context,
		"-n", cfg.Namespace,
		"exec", "-i", podName, "-c", "main",
		"--", "tar", "xzf", "-", "-C", "/root")
	cmd.Stdin = bytes.NewReader(tarball)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("upload config: %s: %w", strings.TrimSpace(string(out)), err)
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

// encodeProjectPath matches session.EncodeProjectPath convention.
func encodeProjectPath(path string) string {
	s := strings.ReplaceAll(path, "/", "-")
	s = strings.ReplaceAll(s, ".", "-")
	if len(s) > 0 && s[0] == '-' {
		s = s[1:]
	}
	return s
}
