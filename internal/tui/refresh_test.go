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
