package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

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

func writeLiveDetectionStubs(t *testing.T, binDir string, livePaths ...string) {
	t.Helper()

	pgrepScript := "#!/bin/sh\nexit 1\n"
	lsofScript := "#!/bin/sh\nexit 1\n"
	if len(livePaths) > 0 {
		pgrepScript = "#!/bin/sh\necho 123\n"
		lsofScript = "#!/bin/sh\ncat <<'EOF'\n"
		for _, path := range livePaths {
			lsofScript += "n" + path + "\n"
		}
		lsofScript += "EOF\n"
	}

	for name, content := range map[string]string{
		"pgrep": pgrepScript,
		"lsof":  lsofScript,
	} {
		path := filepath.Join(binDir, name)
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatalf("write %s stub: %v", name, err)
		}
	}
}

func TestDoRefreshRebuildsFilteredSessionItemsWhenLiveStateChanges(t *testing.T) {
	home := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	binDir := filepath.Join(home, "bin")
	projectPath := filepath.Join(home, "proj-b")

	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir claude dir: %v", err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin dir: %v", err)
	}

	writeLiveDetectionStubs(t, binDir)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
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

	writeLiveDetectionStubs(t, binDir, projectPath)
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
	binDir := filepath.Join(home, "bin")
	projectPath := filepath.Join(home, "proj-b")

	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir claude dir: %v", err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin dir: %v", err)
	}

	writeLiveDetectionStubs(t, binDir)
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("TMUX", "")

	filePath := writeTestSessionFile(t, claudeDir, projectPath, "sess-b")
	stale := time.Now().Add(-time.Minute)
	if err := os.Chtimes(filePath, stale, stale); err != nil {
		t.Fatalf("set stale modtime: %v", err)
	}

	app := newConfiguredTestApp([]session.Session{{
		ID:           "sess-b",
		ShortID:      "sess-b",
		FilePath:     filePath,
		ProjectPath:  projectPath,
		ProjectName:  "proj-b",
		ModTime:      stale,
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

	now := time.Now()
	if err := os.Chtimes(filePath, now, now); err != nil {
		t.Fatalf("touch session file: %v", err)
	}

	app.refreshRespondingState()

	selected, ok = app.selectedSession()
	if !ok {
		t.Fatal("expected selected session after responding refresh")
	}
	if !selected.IsResponding {
		t.Fatal("expected filtered selected session to reflect refreshed responding state")
	}
}
