package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeLiveRegistryEntry writes a registry file for a session owned by this
// test process — clauderegistry.Read drops entries whose PID is dead, so the
// PID must be a live one.
func writeLiveRegistryEntry(t *testing.T, claudeDir, sessionID, cwd string) {
	t.Helper()
	dir := filepath.Join(claudeDir, "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	entry := map[string]any{
		"pid":       os.Getpid(),
		"sessionId": sessionID,
		"cwd":       cwd,
		"kind":      "interactive",
		"status":    "idle",
	}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sessionID+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeTranscript creates projects/<encoded cwd>/<id>.jsonl with one user
// message so MsgCount > 0 and the session survives the scanner's filter.
func writeTranscript(t *testing.T, claudeDir, encodedDir, sessionID, cwd string) string {
	t.Helper()
	dir := filepath.Join(claudeDir, "projects", encodedDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, sessionID+".jsonl")
	jsonl := `{"type":"user","isMeta":true,"cwd":"` + cwd + `","timestamp":"2026-08-12T00:00:00Z","message":{"role":"user","content":"hello"}}` + "\n"
	if err := os.WriteFile(path, []byte(jsonl), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func setupLiveFillHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, ".claude")
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	return claudeDir
}

func TestMergeLiveSessionsAddsSessionMissingFromCache(t *testing.T) {
	claudeDir := setupLiveFillHome(t)
	cwd := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, claudeDir, EncodeProjectPath(cwd), "live-1", cwd)
	writeLiveRegistryEntry(t, claudeDir, "live-1", cwd)

	got := MergeLiveSessions(claudeDir, nil)
	if len(got) != 1 {
		t.Fatalf("expected the live session to be merged in, got %d sessions", len(got))
	}
	if got[0].ID != "live-1" {
		t.Fatalf("expected session live-1, got %q", got[0].ID)
	}
	if got[0].ProjectPath != cwd {
		t.Fatalf("expected ProjectPath %q, got %q", cwd, got[0].ProjectPath)
	}
}

// Two live sessions can share one project path. A "newest transcript in the
// project dir" strategy would keep only one; resolution must be by session ID.
func TestMergeLiveSessionsKeepsEverySessionSharingAProjectPath(t *testing.T) {
	claudeDir := setupLiveFillHome(t)
	cwd := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	encoded := EncodeProjectPath(cwd)
	for _, id := range []string{"live-a", "live-b"} {
		writeTranscript(t, claudeDir, encoded, id, cwd)
		writeLiveRegistryEntry(t, claudeDir, id, cwd)
	}

	got := MergeLiveSessions(claudeDir, nil)
	if len(got) != 2 {
		t.Fatalf("expected both live sessions, got %d", len(got))
	}
	ids := map[string]bool{got[0].ID: true, got[1].ID: true}
	if !ids["live-a"] || !ids["live-b"] {
		t.Fatalf("expected live-a and live-b, got %v", ids)
	}
}

func TestMergeLiveSessionsDoesNotDuplicateKnownSessions(t *testing.T) {
	claudeDir := setupLiveFillHome(t)
	cwd := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTranscript(t, claudeDir, EncodeProjectPath(cwd), "live-1", cwd)
	writeLiveRegistryEntry(t, claudeDir, "live-1", cwd)

	existing := []Session{{ID: "live-1", ProjectPath: cwd, MsgCount: 1}}
	got := MergeLiveSessions(claudeDir, existing)
	if len(got) != 1 {
		t.Fatalf("expected no duplicate, got %d sessions", len(got))
	}
}

// The registry's cwd doesn't always encode to the directory holding the
// transcript (moved project, or a differing recorded cwd). The by-ID index
// fallback must still find it.
func TestMergeLiveSessionsFallsBackToIDIndexWhenCwdDirIsWrong(t *testing.T) {
	claudeDir := setupLiveFillHome(t)
	cwd := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	// Transcript lives under a different encoded dir than EncodeProjectPath(cwd).
	writeTranscript(t, claudeDir, "-some-other-encoded-dir", "live-1", cwd)
	writeLiveRegistryEntry(t, claudeDir, "live-1", cwd)

	got := MergeLiveSessions(claudeDir, nil)
	if len(got) != 1 || got[0].ID != "live-1" {
		t.Fatalf("expected live-1 found via ID index, got %+v", got)
	}
}

func TestMergeLiveSessionsIgnoresMissingTranscript(t *testing.T) {
	claudeDir := setupLiveFillHome(t)
	if err := os.MkdirAll(filepath.Join(claudeDir, "projects"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeLiveRegistryEntry(t, claudeDir, "ghost", "/nonexistent/proj")

	got := MergeLiveSessions(claudeDir, nil)
	if len(got) != 0 {
		t.Fatalf("expected no sessions for a registry entry with no transcript, got %d", len(got))
	}
}
