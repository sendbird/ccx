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
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// SnapshotMeta describes a persisted remote session snapshot. The matching
// payload files (session.jsonl, workdir.tgz) live next to meta.yaml under
// snapshotDir/<Name>/.
type SnapshotMeta struct {
	Name        string    `yaml:"name"`
	CreatedAt   time.Time `yaml:"created_at"`
	SourcePod   string    `yaml:"source_pod"`
	Context     string    `yaml:"context"`
	Namespace   string    `yaml:"namespace"`
	Image       string    `yaml:"image"`
	WorkDir     string    `yaml:"work_dir"`
	LocalDir    string    `yaml:"local_dir,omitempty"`
	SessionID   string    `yaml:"session_id,omitempty"`
	HasSession  bool      `yaml:"has_session"`
	HasWorkdir  bool      `yaml:"has_workdir"`
	WorkdirSize int64     `yaml:"workdir_size,omitempty"`
}

func snapshotsRoot() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "ccx", "snapshots")
}

func snapshotDir(name string) string {
	return filepath.Join(snapshotsRoot(), name)
}

// ListSnapshots returns metadata for every snapshot on disk, newest first.
func ListSnapshots() []SnapshotMeta {
	root := snapshotsRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []SnapshotMeta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		meta, err := loadMeta(snapshotDir(e.Name()))
		if err != nil {
			continue
		}
		out = append(out, meta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// LoadSnapshot returns the metadata and on-disk payload paths for a named snapshot.
func LoadSnapshot(name string) (SnapshotMeta, string, string, error) {
	dir := snapshotDir(name)
	meta, err := loadMeta(dir)
	if err != nil {
		return SnapshotMeta{}, "", "", err
	}
	sessionPath := ""
	if meta.HasSession {
		sessionPath = filepath.Join(dir, "session.jsonl")
	}
	workdirPath := ""
	if meta.HasWorkdir {
		workdirPath = filepath.Join(dir, "workdir.tgz")
	}
	return meta, sessionPath, workdirPath, nil
}

// DeleteSnapshot removes a snapshot directory.
func DeleteSnapshot(name string) error {
	if name == "" || strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("invalid snapshot name")
	}
	return os.RemoveAll(snapshotDir(name))
}

// ExportSnapshot writes a portable tar.gz bundle for a snapshot directory.
// The archive contains files under a single top-level <snapshot-name>/ prefix.
func ExportSnapshot(name, outputPath string) error {
	if name == "" || strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("invalid snapshot name")
	}
	if outputPath == "" {
		return fmt.Errorf("output path required")
	}
	dir := snapshotDir(name)
	if _, err := loadMeta(dir); err != nil {
		return fmt.Errorf("load snapshot: %w", err)
	}

	out, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create export: %w", err)
	}
	defer out.Close()
	gw := gzip.NewWriter(out)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		return addFileToTar(tw, path, filepath.Join(name, rel))
	})
}

// ImportSnapshot extracts an exported tar.gz bundle into the snapshots root.
// If overrideName is non-empty, the imported directory and meta name are renamed.
func ImportSnapshot(inputPath, overrideName string) (SnapshotMeta, error) {
	if inputPath == "" {
		return SnapshotMeta{}, fmt.Errorf("input path required")
	}
	f, err := os.Open(inputPath)
	if err != nil {
		return SnapshotMeta{}, fmt.Errorf("open import: %w", err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return SnapshotMeta{}, fmt.Errorf("read gzip: %w", err)
	}
	defer gr.Close()

	if err := os.MkdirAll(snapshotsRoot(), 0755); err != nil {
		return SnapshotMeta{}, fmt.Errorf("mkdir snapshots root: %w", err)
	}
	tmp, err := os.MkdirTemp(snapshotsRoot(), ".import-*")
	if err != nil {
		return SnapshotMeta{}, fmt.Errorf("mktemp import: %w", err)
	}
	defer os.RemoveAll(tmp)

	tr := tar.NewReader(gr)
	top := ""
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return SnapshotMeta{}, fmt.Errorf("read tar: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		parts := strings.Split(filepath.Clean(header.Name), string(os.PathSeparator))
		if len(parts) < 2 || parts[0] == "." || parts[0] == ".." {
			return SnapshotMeta{}, fmt.Errorf("invalid archive path: %s", header.Name)
		}
		if top == "" {
			top = parts[0]
		} else if top != parts[0] {
			return SnapshotMeta{}, fmt.Errorf("archive has multiple top-level dirs")
		}
		rel := filepath.Join(parts[1:]...)
		if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			return SnapshotMeta{}, fmt.Errorf("unsafe archive path: %s", header.Name)
		}
		dest := filepath.Join(tmp, rel)
		if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
			return SnapshotMeta{}, err
		}
		out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode))
		if err != nil {
			return SnapshotMeta{}, err
		}
		_, copyErr := io.Copy(out, tr)
		closeErr := out.Close()
		if copyErr != nil {
			return SnapshotMeta{}, copyErr
		}
		if closeErr != nil {
			return SnapshotMeta{}, closeErr
		}
	}

	meta, err := loadMeta(tmp)
	if err != nil {
		return SnapshotMeta{}, fmt.Errorf("import meta: %w", err)
	}
	name := meta.Name
	if overrideName != "" {
		if strings.ContainsAny(overrideName, "/\\") {
			return SnapshotMeta{}, fmt.Errorf("invalid snapshot name")
		}
		name = overrideName
		meta.Name = overrideName
	}
	if name == "" || strings.ContainsAny(name, "/\\") {
		return SnapshotMeta{}, fmt.Errorf("invalid snapshot name")
	}

	dest := snapshotDir(name)
	if err := os.RemoveAll(dest); err != nil {
		return SnapshotMeta{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return SnapshotMeta{}, err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return SnapshotMeta{}, err
	}
	if overrideName != "" {
		if err := writeMeta(dest, meta); err != nil {
			return SnapshotMeta{}, err
		}
	}
	return meta, nil
}

// SaveSnapshot captures the JSONL transcript and workdir tarball from a live
// remote pod into snapshotsRoot/<name>/. Returns the resolved metadata.
func SaveSnapshot(ctx context.Context, cfg Config, podName, name string, src SavedSession) (SnapshotMeta, error) {
	if name == "" {
		name = fmt.Sprintf("%s-%s", podName, time.Now().Format("20060102-150405"))
	}
	if strings.ContainsAny(name, "/\\") {
		return SnapshotMeta{}, fmt.Errorf("invalid snapshot name: %q", name)
	}

	dir := snapshotDir(name)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return SnapshotMeta{}, fmt.Errorf("mkdir snapshot: %w", err)
	}

	meta := SnapshotMeta{
		Name:      name,
		CreatedAt: time.Now(),
		SourcePod: podName,
		Context:   cfg.Context,
		Namespace: cfg.Namespace,
		Image:     cfg.Image,
		WorkDir:   cfg.WorkDir,
		LocalDir:  src.LocalDir,
		SessionID: src.SessionID,
	}

	// Session JSONL — best effort, missing pod state shouldn't fail the snapshot.
	t := cfg.BuildTransport()
	if kt, ok := t.(*kubectlTransport); ok {
		kt.podName = podName
	}
	if data, err := FetchSessionJSONL(cfg, t); err == nil && len(data) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "session.jsonl"), data, 0644); err == nil {
			meta.HasSession = true
		}
	}

	// Workdir tarball.
	if tarball, err := fetchRemoteWorkdir(ctx, cfg, podName); err == nil && len(tarball) > 0 {
		if err := os.WriteFile(filepath.Join(dir, "workdir.tgz"), tarball, 0644); err == nil {
			meta.HasWorkdir = true
			meta.WorkdirSize = int64(len(tarball))
		}
	}

	if !meta.HasSession && !meta.HasWorkdir {
		os.RemoveAll(dir)
		return SnapshotMeta{}, fmt.Errorf("snapshot %q is empty (no session, no workdir)", name)
	}

	if err := writeMeta(dir, meta); err != nil {
		return SnapshotMeta{}, fmt.Errorf("write meta: %w", err)
	}
	return meta, nil
}

// FetchWorkdirToDir extracts the pod's workdir tarball into destDir on disk.
// Used by `remote:pull` to bring guest changes back to the host LocalDir.
func FetchWorkdirToDir(ctx context.Context, cfg Config, podName, destDir string) error {
	if destDir == "" {
		return fmt.Errorf("destination directory required")
	}
	if err := ValidateWorkdir(destDir); err != nil {
		return err
	}
	tarball, err := fetchRemoteWorkdir(ctx, cfg, podName)
	if err != nil {
		return err
	}
	if len(tarball) == 0 {
		return fmt.Errorf("empty workdir tarball")
	}
	cmd := exec.CommandContext(ctx, "tar", "xzf", "-", "-C", destDir)
	cmd.Stdin = bytes.NewReader(tarball)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("tar extract: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// fetchRemoteWorkdir tars+gzips cfg.WorkDir on the pod and streams it back.
func fetchRemoteWorkdir(ctx context.Context, cfg Config, podName string) ([]byte, error) {
	if cfg.WorkDir == "" {
		return nil, fmt.Errorf("work_dir unset")
	}
	// Tar the workdir contents (not the parent), excluding noisy dirs.
	script := fmt.Sprintf(
		"cd %s 2>/dev/null && tar czf - --exclude=node_modules --exclude=.git --exclude=vendor --exclude=tmp --exclude=__pycache__ --exclude=dist --exclude=build . 2>/dev/null",
		shellQuote(cfg.WorkDir))
	args := []string{
		"--context", cfg.Context,
		"-n", cfg.Namespace,
		"exec", podName,
	}
	if cfg.Container != "" {
		args = append(args, "-c", cfg.Container)
	}
	args = append(args, "--", "sh", "-c", script)
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("fetch workdir: %w", err)
	}
	return stdout.Bytes(), nil
}

func loadMeta(dir string) (SnapshotMeta, error) {
	data, err := os.ReadFile(filepath.Join(dir, "meta.yaml"))
	if err != nil {
		return SnapshotMeta{}, err
	}
	var meta SnapshotMeta
	if err := yaml.Unmarshal(data, &meta); err != nil {
		return SnapshotMeta{}, err
	}
	return meta, nil
}

func writeMeta(dir string, meta SnapshotMeta) error {
	data, err := yaml.Marshal(meta)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "meta.yaml"), data, 0644)
}
