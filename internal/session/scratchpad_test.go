package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeScratchpadFixture writes the given files into the session's scratchpad
// directory under ScratchpadBase() (which honors $TMPDIR, set by the caller),
// returning the project path + session ID pair LoadScratchpadFiles expects.
func writeScratchpadFixture(t *testing.T, projectPath, sessionID string, files map[string]string) (string, string) {
	t.Helper()
	dir := filepath.Join(ScratchpadBase(), EncodeProjectPath(projectPath), sessionID, "scratchpad")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for name, content := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return projectPath, sessionID
}

func TestLoadScratchpadFiles_MissingDir(t *testing.T) {
	if got := LoadScratchpadFiles("/nonexistent/project", "no-such-session"); got != nil {
		t.Fatalf("expected nil for missing dir, got %d files", len(got))
	}
}

func TestLoadScratchpadFiles_EmptyArgs(t *testing.T) {
	if got := LoadScratchpadFiles("", "sid"); got != nil {
		t.Fatalf("expected nil for empty projectPath, got %d", len(got))
	}
	if got := LoadScratchpadFiles("/p", ""); got != nil {
		t.Fatalf("expected nil for empty sessionID, got %d", len(got))
	}
}

func TestLoadScratchpadFiles_LoadsTextSorted(t *testing.T) {
	base := t.TempDir()
	orig := scratchpadBaseOverride
	scratchpadBaseOverride = base
	defer func() { scratchpadBaseOverride = orig }()

	projectPath := "/Users/me/src/repo"
	sessionID := "sess-uuid-1"
	writeScratchpadFixture(t, projectPath, sessionID, map[string]string{
		"notes.md":  "# plan\nsome scratch text\n",
		"alpha.txt": "first file\n",
	})

	files := LoadScratchpadFiles(projectPath, sessionID)
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	if files[0].Name != "alpha.txt" {
		t.Errorf("expected sorted: alpha.txt first, got %q", files[0].Name)
	}
	if !files[0].IsText {
		t.Errorf("alpha.txt should be text")
	}
	if files[0].Body != "first file\n" {
		t.Errorf("unexpected body %q", files[0].Body)
	}
	if files[1].Name != "notes.md" || !strings.Contains(files[1].Body, "scratch text") {
		t.Errorf("notes.md body mismatch: %q", files[1].Body)
	}
	// Path is absolute and lives under the fake base.
	if !filepath.IsAbs(files[0].Path) {
		t.Errorf("expected absolute path, got %q", files[0].Path)
	}
}

func TestLoadScratchpadFiles_BinaryPlaceholder(t *testing.T) {
	base := t.TempDir()
	orig := scratchpadBaseOverride
	scratchpadBaseOverride = base
	defer func() { scratchpadBaseOverride = orig }()

	projectPath := "/Users/me/src/repo2"
	sessionID := "sess-bin"
	dir := filepath.Join(ScratchpadBase(), EncodeProjectPath(projectPath), sessionID, "scratchpad")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// NUL byte → not valid text.
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	files := LoadScratchpadFiles(projectPath, sessionID)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if files[0].IsText {
		t.Errorf("blob.bin should not be text")
	}
	if files[0].Body != "(binary file)" {
		t.Errorf("expected binary placeholder, got %q", files[0].Body)
	}
}

func TestLoadScratchpadFiles_TruncatesLargeText(t *testing.T) {
	base := t.TempDir()
	orig := scratchpadBaseOverride
	scratchpadBaseOverride = base
	defer func() { scratchpadBaseOverride = orig }()

	projectPath := "/Users/me/src/big"
	sessionID := "sess-big"
	dir := filepath.Join(ScratchpadBase(), EncodeProjectPath(projectPath), sessionID, "scratchpad")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	big := make([]byte, scratchpadMaxBody+1024)
	for i := range big {
		big[i] = 'a'
	}
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), big, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	files := LoadScratchpadFiles(projectPath, sessionID)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
	if !files[0].IsText {
		t.Errorf("big.txt should be text")
	}
	if !files[0].Truncated {
		t.Errorf("expected Truncated=true for oversized file")
	}
	if int64(len(files[0].Body)) > scratchpadMaxBody {
		t.Errorf("body exceeds cap: %d", len(files[0].Body))
	}
	if files[0].Size != int64(len(big)) {
		t.Errorf("Size should reflect full file: got %d want %d", files[0].Size, len(big))
	}
}

// scratchpadNames returns the listed names, so a test can assert on the listing
// without caring about bodies.
func scratchpadNames(files []ScratchpadFile) []string {
	out := make([]string, 0, len(files))
	for _, f := range files {
		out = append(out, f.Name)
	}
	return out
}

// Agents organize their scratchpad into subdirectories ("kp/pr/pr.md"), and a
// top-level-only listing dropped every one of them: a session whose own summary
// pointed at scratchpad/kp/pr/pr.md showed nothing in the preview.
func TestLoadScratchpadFiles_Recurses(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	defer SetScratchpadBaseOverride(t.TempDir())()
	proj, sid := writeScratchpadFixture(t, "/p", "sid", map[string]string{
		"reply.txt":      "top level",
		"kp/pr/issue.md": "issue body",
		"kp/pr/pr.md":    "pr body",
	})

	got := scratchpadNames(LoadScratchpadFiles(proj, sid))
	want := []string{"kp/pr/issue.md", "kp/pr/pr.md", "reply.txt"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("index %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// A repository cloned into the scratchpad is upstream code, not session output.
// Walking it buried the session's own files: one karpenter checkout filled every
// listing slot and pushed kp/pr/issue.md out entirely.
func TestLoadScratchpadFiles_ListsClonedRepoAsOneRow(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	defer SetScratchpadBaseOverride(t.TempDir())()
	files := map[string]string{"kp/pr/pr.md": "pr body"}
	for i := 0; i < 20; i++ {
		files[filepath.Join("kp/upstream/pkg", "f"+itoa(i)+".go")] = "package upstream"
	}
	files["kp/upstream/.git/HEAD"] = "ref: refs/heads/main"
	proj, sid := writeScratchpadFixture(t, "/p", "sid", files)

	got := LoadScratchpadFiles(proj, sid)
	names := scratchpadNames(got)
	want := []string{"kp/pr/pr.md", "kp/upstream/"}
	if len(names) != len(want) {
		t.Fatalf("got %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("index %d: got %q, want %q", i, names[i], want[i])
		}
	}
	for _, f := range got {
		if f.Name == "kp/upstream/" && !f.IsRepo {
			t.Error("cloned repo row should set IsRepo")
		}
	}
}

// Build/dependency trees are not session products; descending into them buries
// what is.
func TestLoadScratchpadFiles_SkipsDependencyDirs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	defer SetScratchpadBaseOverride(t.TempDir())()
	proj, sid := writeScratchpadFixture(t, "/p", "sid", map[string]string{
		"notes.md":                 "keep",
		"node_modules/left-pad.js": "drop",
		"__pycache__/x.pyc":        "drop",
	})

	got := scratchpadNames(LoadScratchpadFiles(proj, sid))
	if len(got) != 1 || got[0] != "notes.md" {
		t.Fatalf("got %v, want [notes.md]", got)
	}
}

// When the listing cannot cover everything, saying so beats a silently short
// list that reads as "this is everything".
func TestLoadScratchpadFiles_MarksTruncation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	defer SetScratchpadBaseOverride(t.TempDir())()
	files := make(map[string]string, scratchpadMaxFiles+10)
	for i := 0; i < scratchpadMaxFiles+10; i++ {
		files["f"+itoa(i)+".txt"] = "x"
	}
	proj, sid := writeScratchpadFixture(t, "/p", "sid", files)

	got := LoadScratchpadFiles(proj, sid)
	if len(got) != scratchpadMaxFiles+1 {
		t.Fatalf("got %d rows, want %d files + 1 truncation note", len(got), scratchpadMaxFiles)
	}
	found := false
	for _, f := range got {
		if f.Name == ScratchpadTruncatedMarker {
			found = true
		}
	}
	if !found {
		t.Errorf("truncated listing must carry %q; got %v", ScratchpadTruncatedMarker, scratchpadNames(got))
	}
}
