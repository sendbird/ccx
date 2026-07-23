package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
)

// mustSessionList builds the visible item list the same way the running app
// does (autoSelectSession always sees a project-centric list, per app.go),
// so that "most recent wins" ordering guarantees hold.
func mustSessionList(t *testing.T, sessions []session.Session) []list.Item {
	t.Helper()
	items := buildGroupedItems(sessions, groupProjectCentric, nil)
	return items
}

// realTempDir returns t.TempDir() with symlinks resolved. On macOS,
// t.TempDir() lives under /var/folders, a symlink to /private/var/folders;
// os.Getwd() after os.Chdir resolves through it, so paths compared against
// it (session.ProjectPath in these tests) must be pre-resolved too.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestFirstSessionAtPath(t *testing.T) {
	now := time.Now()
	sessions := []session.Session{
		{ID: "old", ShortID: "old", ProjectPath: "/tmp/proj", ProjectName: "proj", ModTime: now.Add(-time.Hour)},
		{ID: "new", ShortID: "new", ProjectPath: "/tmp/proj", ProjectName: "proj", ModTime: now},
		{ID: "other", ShortID: "other", ProjectPath: "/tmp/other", ProjectName: "other", ModTime: now},
	}
	visible := mustSessionList(t, sessions)

	i, ok := firstSessionAtPath(visible, "/tmp/proj")
	if !ok {
		t.Fatalf("expected match for /tmp/proj")
	}
	si := visible[i].(sessionItem)
	if si.sess.ID != "new" {
		t.Fatalf("expected most recent session 'new' to win, got %q", si.sess.ID)
	}

	if _, ok := firstSessionAtPath(visible, "/tmp/nope"); ok {
		t.Fatalf("expected no match for unrelated path")
	}
}

func TestAutoSelectByCWD_MatchesExactCWD(t *testing.T) {
	tmp := realTempDir(t)
	proj := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	sessions := []session.Session{
		{ID: "s1", ShortID: "s1", ProjectPath: proj, ProjectName: "proj", ModTime: time.Now()},
	}
	visible := mustSessionList(t, sessions)

	restore := chdir(t, proj)
	defer restore()

	i, ok := autoSelectByCWD(visible)
	if !ok {
		t.Fatalf("expected match at exact CWD")
	}
	if visible[i].(sessionItem).sess.ID != "s1" {
		t.Fatalf("expected s1 selected")
	}
}

func TestAutoSelectByCWD_WalksUpToParentProjectRoot(t *testing.T) {
	tmp := realTempDir(t)
	proj := filepath.Join(tmp, "proj")
	sub := filepath.Join(proj, "a", "b", "c")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	sessions := []session.Session{
		{ID: "s1", ShortID: "s1", ProjectPath: proj, ProjectName: "proj", ModTime: time.Now()},
	}
	visible := mustSessionList(t, sessions)

	restore := chdir(t, sub)
	defer restore()

	i, ok := autoSelectByCWD(visible)
	if !ok {
		t.Fatalf("expected match walking up from %s to %s", sub, proj)
	}
	if visible[i].(sessionItem).sess.ID != "s1" {
		t.Fatalf("expected s1 selected")
	}
}

func TestAutoSelectByCWD_StopsAtGitWorktreeRoot(t *testing.T) {
	tmp := realTempDir(t)
	repo := filepath.Join(tmp, "repo")
	sub := filepath.Join(repo, "deep", "dir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v: %s", err, out)
	}

	// No session anywhere under repo, and repo's parent (tmp) has a session
	// that must NOT be picked up because repo is a git worktree root boundary.
	sessions := []session.Session{
		{ID: "outside", ShortID: "outside", ProjectPath: tmp, ProjectName: "tmp", ModTime: time.Now()},
	}
	visible := mustSessionList(t, sessions)

	restore := chdir(t, sub)
	defer restore()

	if _, ok := autoSelectByCWD(visible); ok {
		t.Fatalf("expected no match: search should stop at git worktree root %s before reaching %s", repo, tmp)
	}
}

func TestAutoSelectByCWD_MatchesAtGitWorktreeRootItself(t *testing.T) {
	tmp := realTempDir(t)
	repo := filepath.Join(tmp, "repo")
	sub := filepath.Join(repo, "deep", "dir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v: %s", err, out)
	}

	sessions := []session.Session{
		{ID: "s1", ShortID: "s1", ProjectPath: repo, ProjectName: "repo", ModTime: time.Now()},
	}
	visible := mustSessionList(t, sessions)

	restore := chdir(t, sub)
	defer restore()

	i, ok := autoSelectByCWD(visible)
	if !ok {
		t.Fatalf("expected match at git worktree root itself")
	}
	if visible[i].(sessionItem).sess.ID != "s1" {
		t.Fatalf("expected s1 selected")
	}
}

func TestGitWorktreeRoot(t *testing.T) {
	tmp := realTempDir(t)
	repo := filepath.Join(tmp, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v: %s", err, out)
	}
	if got := gitWorktreeRoot(repo); got != repo {
		t.Fatalf("gitWorktreeRoot(%s) = %s, want %s", repo, got, repo)
	}

	if got := gitWorktreeRoot(tmp); got != "" {
		t.Fatalf("expected no git root for non-repo dir, got %s", got)
	}
}

// TestAutoSelectSession_ClearsAutoFilterWhenItHidesCWDMatch reproduces the
// bug where the startup active-state filter (is:live,is:input,is:mon) hides
// the CWD session from autoSelectByCWD's search (VisibleItems() only), so
// auto-focus falls through to an unrelated "most recent" session instead of
// the one the user is actually sitting in.
func TestAutoSelectSession_ClearsAutoFilterWhenItHidesCWDMatch(t *testing.T) {
	tmp := realTempDir(t)
	proj := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	sessions := []session.Session{
		// Distractor: live, so it survives the default filter and the
		// filtered view isn't empty (keeping the unrelated blank-guard out
		// of this test).
		{ID: "distractor", ShortID: "distractor", ProjectPath: "/tmp/other-proj", ProjectName: "other-proj", ModTime: time.Now(), IsLive: true},
		// The CWD session: idle, so is:live,is:input,is:mon hides it.
		{ID: "cwd-sess", ShortID: "cwd-sess", ProjectPath: proj, ProjectName: "proj", ModTime: time.Now().Add(-time.Hour)},
	}

	restore := chdir(t, proj)
	defer restore()

	app := NewApp(sessions, Config{TmuxEnabled: true, InitialFocus: initialFocusCWD})
	// Force the exact scenario regardless of the developer's local
	// ~/.config/ccx/config.yaml: the auto-applied default active-state
	// filter is in effect.
	app.config.SearchQuery = defaultActiveStateFilter
	app.autoStateFilter = true

	m, _ := app.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	app = m.(*App)

	if app.autoStateFilter {
		t.Fatal("expected the auto-default filter to be cleared once the CWD match was found only outside it")
	}
	sel, ok := app.selectedSession()
	if !ok || sel.ID != "cwd-sess" {
		t.Fatalf("expected cwd-sess to be auto-selected, got %+v (ok=%v)", sel, ok)
	}
	if !strings.Contains(app.copiedMsg, "filter cleared") {
		t.Fatalf("expected status message to mention the filter was cleared, got %q", app.copiedMsg)
	}
}

// TestAutoSelectSession_RespectsExplicitUserFilter checks the flip side: when
// the active filter was explicitly chosen by the user (not the auto-applied
// default), autoSelectSession must NOT clear it even if that hides the CWD
// match — the user's explicit choice wins.
func TestAutoSelectSession_RespectsExplicitUserFilter(t *testing.T) {
	tmp := realTempDir(t)
	proj := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	sessions := []session.Session{
		{ID: "distractor", ShortID: "distractor", ProjectPath: "/tmp/other-proj", ProjectName: "other-proj", ModTime: time.Now(), IsLive: true},
		{ID: "cwd-sess", ShortID: "cwd-sess", ProjectPath: proj, ProjectName: "proj", ModTime: time.Now().Add(-time.Hour)},
	}

	restore := chdir(t, proj)
	defer restore()

	app := NewApp(sessions, Config{TmuxEnabled: true, InitialFocus: initialFocusCWD})
	// An explicit user filter, not the auto-default.
	app.config.SearchQuery = "is:live"
	app.autoStateFilter = false

	m, _ := app.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	app = m.(*App)

	if app.autoStateFilter {
		t.Fatal("autoStateFilter should remain false — it was never the auto default")
	}
	if app.config.SearchQuery != "is:live" {
		t.Fatalf("expected explicit filter to survive untouched, got %q", app.config.SearchQuery)
	}
	sel, ok := app.selectedSession()
	if !ok || sel.ID != "distractor" {
		t.Fatalf("expected fallback to the visible distractor session, got %+v (ok=%v)", sel, ok)
	}
	if app.copiedMsg != "Focused: most recent session" {
		t.Fatalf("expected fallback status message, got %q", app.copiedMsg)
	}
}

func chdir(t *testing.T, dir string) func() {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	return func() { _ = os.Chdir(old) }
}
