package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
)

func newConfiguredTestApp(sessions []session.Session, cfg Config) *App {
	app := NewApp(sessions, cfg)
	m, _ := app.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	a := m.(*App)
	a.state = viewSessions
	a.sessPreviewMode = sessPreviewConversation
	// Hermetic: clear the default startup state filter so visible-item and
	// selection assertions don't depend on the sessions' lifecycle.
	a.config.SearchQuery = ""
	a.sessionList.ResetFilter()
	a.rebuildSessionList()
	return a
}

func writeTestSessionFile(t *testing.T, claudeDir, projectPath, sessionID string) string {
	t.Helper()

	if err := os.MkdirAll(projectPath, 0o755); err != nil {
		t.Fatalf("mkdir project path: %v", err)
	}

	projectDir := filepath.Join(claudeDir, "projects", session.EncodeProjectPath(projectPath))
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}

	filePath := filepath.Join(projectDir, sessionID+".jsonl")
	content := fmt.Sprintf("{\"isMeta\":true,\"cwd\":%q,\"gitBranch\":\"main\"}\n{\"role\":\"user\",\"content\":\"hello\"}\n", projectPath)
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		t.Fatalf("write session file: %v", err)
	}
	return filePath
}

// writeRegistryEntry creates a Claude registry file for the given session.
// CLAUDE_CONFIG_DIR must already be pointed at configDir by the caller.
// Uses the current process PID so the entry passes the kill(pid,0) liveness
// check in clauderegistry.Read().
func writeRegistryEntry(t *testing.T, configDir, sessionID, cwd, status string) {
	t.Helper()
	dir := filepath.Join(configDir, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	pid := os.Getpid()
	entry := map[string]any{
		"pid":       pid,
		"sessionId": sessionID,
		"cwd":       cwd,
		"status":    status,
		"kind":      "interactive",
	}
	body, _ := json.Marshal(entry)
	path := filepath.Join(dir, fmt.Sprintf("%d-%s.json", pid, sessionID))
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write registry: %v", err)
	}
}

// clearRegistry removes every file in $CLAUDE_CONFIG_DIR/sessions.
func clearRegistry(t *testing.T, configDir string) {
	t.Helper()
	dir := filepath.Join(configDir, "sessions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
}

func TestDoRefreshRebuildsFilteredSessionItemsWhenLiveStateChanges(t *testing.T) {
	home := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	configDir := filepath.Join(home, "config")
	projectPath := filepath.Join(home, "proj-b")

	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir claude dir: %v", err)
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}

	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	t.Setenv("TMUX", "")

	writeTestSessionFile(t, claudeDir, projectPath, "sess-b")
	sessions, err := session.ScanSessions(claudeDir)
	if err != nil {
		t.Fatalf("scan sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}

	app := newConfiguredTestApp(sessions, Config{ClaudeDir: claudeDir, TmuxEnabled: true})
	applyListFilter(&app.sessionList, sessions[0].ProjectName)

	selected, ok := app.selectedSession()
	if !ok {
		t.Fatal("expected selected session before refresh")
	}
	if selected.IsLive {
		t.Fatal("expected initial selected session to be non-live")
	}

	writeRegistryEntry(t, configDir, "sess-b", projectPath, "idle")
	app.doRefresh()

	selected, ok = app.selectedSession()
	if !ok {
		t.Fatal("expected selected session after refresh")
	}
	if !selected.IsLive {
		t.Fatal("expected filtered selected session to reflect refreshed live state")
	}
}

func TestRefreshRespondingStateRebuildsFilteredSessionItems(t *testing.T) {
	home := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	configDir := filepath.Join(home, "config")
	projectPath := filepath.Join(home, "proj-b")

	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir claude dir: %v", err)
	}
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}

	t.Setenv("CLAUDE_CONFIG_DIR", configDir)
	t.Setenv("TMUX", "")

	filePath := writeTestSessionFile(t, claudeDir, projectPath, "sess-b")

	// Initial state: live but idle.
	writeRegistryEntry(t, configDir, "sess-b", projectPath, "idle")

	app := newConfiguredTestApp([]session.Session{{
		ID:           "sess-b",
		ShortID:      "sess-b",
		FilePath:     filePath,
		ProjectPath:  projectPath,
		ProjectName:  "proj-b",
		MsgCount:     1,
		IsLive:       true,
		IsResponding: false,
	}}, Config{ClaudeDir: claudeDir, TmuxEnabled: true})
	applyListFilter(&app.sessionList, "proj-b")

	selected, ok := app.selectedSession()
	if !ok {
		t.Fatal("expected selected session before responding refresh")
	}
	if selected.IsResponding {
		t.Fatal("expected initial selected session to be idle")
	}

	// Flip registry status to busy.
	clearRegistry(t, configDir)
	writeRegistryEntry(t, configDir, "sess-b", projectPath, "busy")

	app.refreshRespondingState()

	selected, ok = app.selectedSession()
	if !ok {
		t.Fatal("expected selected session after responding refresh")
	}
	if !selected.IsResponding {
		t.Fatal("expected filtered selected session to reflect refreshed responding state")
	}
}

// TestInvalidateOpenPreviewResetsRefsState verifies that a manual refresh while
// the refs preview is open clears the selected session's resolution state, the
// in-flight latch, and the render cache keys so the preview re-resolves.
func TestInvalidateOpenPreviewResetsRefsState(t *testing.T) {
	sess := session.Session{ID: "sess-r", ShortID: "sess-r", ProjectName: "proj", HasRefs: true, RefsResolved: true}
	sess.Refs = []session.SessionRef{{URL: "https://example.test/pr/9", Resolved: true}}
	app := newConfiguredTestApp([]session.Session{sess}, Config{})
	app.sessPreviewMode = sessPreviewRefs
	app.refsInFlight[sess.ID] = true
	app.sessRefsCacheKey = "stale"
	app.sessSplit.CacheKey = "stale"

	app.invalidateOpenPreviewCaches()

	// updateSessionRefsPreview reads the authoritative store copy, so verify
	// there (the sessionList widget holds an independent snapshot).
	got, ok := app.sessionByIDFromStore(sess.ID)
	if !ok {
		t.Fatal("session missing from store")
	}
	if got.RefsResolved {
		t.Error("RefsResolved not cleared")
	}
	if len(got.Refs) != 0 {
		t.Errorf("Refs not cleared: %d", len(got.Refs))
	}
	if app.refsInFlight[sess.ID] {
		t.Error("refsInFlight latch not released")
	}
	if app.sessRefsCacheKey != "" || app.sessSplit.CacheKey != "" {
		t.Errorf("cache keys not cleared: refs=%q split=%q", app.sessRefsCacheKey, app.sessSplit.CacheKey)
	}
}

// TestInvalidateOpenPreviewResetsStatsCache verifies a non-refs mode drops its
// dedicated cache so updateSessionPreview re-reads the transcript.
func TestInvalidateOpenPreviewResetsStatsCache(t *testing.T) {
	app := newConfiguredTestApp([]session.Session{{ID: "sess-s", ShortID: "sess-s", ProjectName: "proj"}}, Config{})
	app.sessPreviewMode = sessPreviewStats
	app.sessStatsCache = &session.SessionStats{}
	app.sessStatsCacheKey = "sess-s"
	app.sessSplit.CacheKey = "stale"

	app.invalidateOpenPreviewCaches()

	if app.sessStatsCache != nil {
		t.Error("sessStatsCache not cleared")
	}
	if app.sessStatsCacheKey != "" || app.sessSplit.CacheKey != "" {
		t.Errorf("cache keys not cleared: stats=%q split=%q", app.sessStatsCacheKey, app.sessSplit.CacheKey)
	}
}

// TestRefreshSkipsPreviewInvalidationForLiveMode verifies that R in live/remote
// preview mode does not run the preview-cache invalidation (which would flash
// "(connecting…)" and re-find the tmux pane). The refs state of the selected
// session must stay intact as a proxy for "invalidateOpenPreviewCaches was not
// called".
func TestRefreshSkipsPreviewInvalidationForLiveMode(t *testing.T) {
	// Isolate the scan dir so doRefresh's ScanSessions can't replace the store
	// with the developer's real sessions.
	claudeDir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("TMUX", "")
	sess := session.Session{ID: "sess-l", ShortID: "sess-l", ProjectName: "proj", HasRefs: true, RefsResolved: true}
	sess.Refs = []session.SessionRef{{URL: "https://example.test/pr/3", Resolved: true}}
	app := newConfiguredTestApp([]session.Session{sess}, Config{ClaudeDir: claudeDir})
	app.sessPreviewMode = sessPreviewLive
	app.sessSplit.CacheKey = "keep-me"

	m, _ := app.handleSessionKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}})
	app = m.(*App)

	got, ok := app.sessionByIDFromStore("sess-l")
	if !ok {
		t.Fatal("session missing from store")
	}
	if !got.RefsResolved || len(got.Refs) != 1 {
		t.Errorf("live-mode R reset refs state: RefsResolved=%v Refs=%d", got.RefsResolved, len(got.Refs))
	}
	if app.sessSplit.CacheKey != "keep-me" {
		t.Errorf("live-mode R invalidated the preview cache key: %q", app.sessSplit.CacheKey)
	}
}
