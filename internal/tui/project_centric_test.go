package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
)

func TestProjectCentricPreviewShowsProjectSummary(t *testing.T) {
	sessions := []session.Session{
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", GitBranch: "main", ModTime: time.Now(), MsgCount: 10, FirstPrompt: "first prompt", IsLive: true},
		{ID: "a2", ShortID: "a2", ProjectPath: "/tmp/repo-a/.worktree/feat", ProjectName: "feat", ModTime: time.Now().Add(-time.Hour), MsgCount: 3, FirstPrompt: "second prompt", IsWorktree: true, HasMonitorJobs: true, HasShellJobs: true, IsLive: true},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupProjectCentric
	app.rebuildSessionList()
	items := app.sessionList.VisibleItems()
	if len(items) == 0 {
		t.Fatal("expected visible items")
	}
	if _, ok := items[0].(projectItem); !ok {
		t.Fatalf("expected first item to be projectItem, got %T", items[0])
	}
	app.sessionList.Select(0)
	app.sessSplit.Show = true
	if cmd := app.updateSessionPreview(); cmd != nil {
		t.Fatal("expected project preview update to be synchronous")
	}
	content := app.sessSplit.Preview.View()
	for _, want := range []string{"repo-a", "/tmp/repo-a", "Sessions: 2", "Worktrees: 1", "Sessions in project", "a1", "a2"} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected project preview to contain %q, got:\n%s", want, content)
		}
	}
}

func TestProjectCentricEnterTogglesProjectFold(t *testing.T) {
	sessions := []session.Session{
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: time.Now(), MsgCount: 10},
		{ID: "a2", ShortID: "a2", ProjectPath: "/tmp/repo-a/.worktree/feat", ProjectName: "feat", ModTime: time.Now().Add(-time.Hour), MsgCount: 3, IsWorktree: true},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupProjectCentric
	app.rebuildSessionList()
	app.sessionList.Select(0)
	before := len(app.sessionList.VisibleItems())
	m, _ := app.handleSessionKeys(tea.KeyMsg{Type: tea.KeyEnter})
	app = m.(*App)
	after := len(app.sessionList.VisibleItems())
	if after >= before {
		t.Fatalf("expected enter on project row to fold children (before=%d after=%d)", before, after)
	}
}

func TestProjectCentricSelectTogglesAllChildSessions(t *testing.T) {
	sessions := []session.Session{
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: time.Now(), MsgCount: 10},
		{ID: "a2", ShortID: "a2", ProjectPath: "/tmp/repo-a/.worktree/feat", ProjectName: "feat", ModTime: time.Now().Add(-time.Hour), MsgCount: 3, IsWorktree: true},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupProjectCentric
	app.rebuildSessionList()
	app.sessionList.Select(0)
	m, _ := app.handleSessionKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	app = m.(*App)
	if len(app.selectedSet) != 2 {
		t.Fatalf("expected selecting a project row to toggle all 2 child sessions, got %d", len(app.selectedSet))
	}
}

func TestProjectCentricPickReturnsAllChildSessions(t *testing.T) {
	sessions := []session.Session{
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: time.Now(), MsgCount: 10},
		{ID: "a2", ShortID: "a2", ProjectPath: "/tmp/repo-a/.worktree/feat", ProjectName: "feat", ModTime: time.Now().Add(-time.Hour), MsgCount: 3, IsWorktree: true},
	}
	app := NewApp(sessions, Config{TmuxEnabled: true, PickMode: true})
	app.width = 160
	app.height = 50
	app.sessSplit = SplitPane{List: &app.sessionList, ItemHeight: 2}
	// Hermetic: clear any persisted startup filter from the developer's local
	// ~/.config/ccx/config.yaml so the project row is actually visible.
	app.config.SearchQuery = ""
	app.sessionList.ResetFilter()
	app.sessGroupMode = groupProjectCentric
	app.rebuildSessionList()
	app.state = viewSessions
	app.sessionList.Select(0)
	m, _ := app.handleSessionKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}})
	app = m.(*App)
	res, ok := app.pickResult.(SessionsResult)
	if !ok {
		t.Fatalf("expected SessionsResult, got %T", app.pickResult)
	}
	if len(res.Items) != 2 {
		t.Fatalf("expected pick on project row to include all child sessions, got %d", len(res.Items))
	}
}

func TestCompletedProjectsToggleKey(t *testing.T) {
	sessions := []session.Session{
		{ID: "done1", ShortID: "done1", ProjectPath: "/tmp/repo-done", ProjectName: "repo-done", ModTime: time.Now(), Tasks: []session.TaskItem{{ID: "1", Status: "completed"}}, HasTasks: true},
		{ID: "wait1", ShortID: "wait1", ProjectPath: "/tmp/repo-wait", ProjectName: "repo-wait", ModTime: time.Now().Add(-time.Hour), Tasks: []session.TaskItem{{ID: "1", Status: "in_progress"}}, HasTasks: true},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupProjectCentric
	app.rebuildSessionList()
	m, _ := app.handleSessionKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	app = m.(*App)
	if got := strings.TrimSpace(app.activeFilterValue()); got != "is:done" {
		t.Fatalf("expected D to apply is:done filter, got %q", got)
	}
	if app.copiedMsg != "Showing completed projects" {
		t.Fatalf("expected completed filter status message, got %q", app.copiedMsg)
	}
	m, _ = app.handleSessionKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	app = m.(*App)
	if got := strings.TrimSpace(app.activeFilterValue()); got != "" {
		t.Fatalf("expected second D to clear filter, got %q", got)
	}
	if app.copiedMsg != "Completed filter cleared" {
		t.Fatalf("expected clear status message, got %q", app.copiedMsg)
	}
}

func TestCompletedProjectsToggleFallsBackWhenNoDoneMatches(t *testing.T) {
	sessions := []session.Session{
		{ID: "wait1", ShortID: "wait1", ProjectPath: "/tmp/repo-wait", ProjectName: "repo-wait", ModTime: time.Now(), Tasks: []session.TaskItem{{ID: "1", Status: "in_progress"}}, HasTasks: true},
	}
	app := newTestApp(sessions)
	app.sessGroupMode = groupProjectCentric
	app.rebuildSessionList()
	m, _ := app.handleSessionKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
	app = m.(*App)
	if got := strings.TrimSpace(app.activeFilterValue()); got != "" {
		t.Fatalf("expected D to clear back out when there are no done matches, got %q", got)
	}
	if app.copiedMsg != "No completed projects found" {
		t.Fatalf("expected no-match status message, got %q", app.copiedMsg)
	}
}

func TestNewAppDefaultsToProjectsBrowserModeOnStartup(t *testing.T) {
	app := NewApp(fakeSessions(), Config{TmuxEnabled: true})
	if app.sessGroupMode != groupProjectCentric {
		t.Fatalf("expected default startup group mode to be project-centric, got %d", app.sessGroupMode)
	}
	prefs := app.capturePreferences()
	if prefs.GroupMode != "projects" {
		t.Fatalf("expected persisted group mode to be projects, got %q", prefs.GroupMode)
	}
}

func TestNewAppHonorsExplicitGroupModeOnStartup(t *testing.T) {
	app := NewApp(fakeSessions(), Config{TmuxEnabled: true, GroupMode: "repo"})
	if app.sessGroupMode != groupBaseProject {
		t.Fatalf("expected explicit --group repo to be honored at startup, got %d", app.sessGroupMode)
	}
	prefs := app.capturePreferences()
	if prefs.GroupMode != "repo" {
		t.Fatalf("expected persisted group mode to reflect actual mode (repo), got %q", prefs.GroupMode)
	}
}
