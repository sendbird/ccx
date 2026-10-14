package tui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sendbird/ccx/internal/session"
	"github.com/sendbird/ccx/internal/tmux"
)

// stubTmux replaces the tmux indirections and counts calls, so a test can
// prove nothing touches tmux on the Update path.
func stubTmux(t *testing.T, found bool, sendErr error) (finds, sends *int) {
	t.Helper()
	finds, sends = new(int), new(int)
	origFind, origHas, origSend := findPane, paneHasClaude, sendInterruptK
	t.Cleanup(func() { findPane, paneHasClaude, sendInterruptK = origFind, origHas, origSend })
	findPane = func(string, ...string) (tmux.Pane, bool) {
		*finds++
		return tmux.Pane{Session: "s", Window: "1", Pane: "0", PID: 42}, found
	}
	paneHasClaude = func(int) bool { return true }
	sendInterruptK = func(string) error {
		*sends++
		return sendErr
	}
	return finds, sends
}

func TestKillLiveSessionRunsTmuxOffUpdateAndReportsSendError(t *testing.T) {
	finds, sends := stubTmux(t, true, errors.New("no server running"))
	a := newTestApp(fakeSessions())
	sess := a.sessions[0]

	_, cmd := a.killLiveSession(sess)
	if *finds != 0 || *sends != 0 {
		t.Fatalf("tmux touched on Update path: finds=%d sends=%d", *finds, *sends)
	}
	if cmd == nil {
		t.Fatal("expected a kill command")
	}
	a.Update(cmd())
	if *sends != 2 {
		t.Fatalf("sends = %d, want 2 (Ctrl-C twice)", *sends)
	}
	if !strings.Contains(a.copiedMsg, "Kill failed") || !strings.Contains(a.copiedMsg, "no server running") {
		t.Fatalf("send-keys error not surfaced: %q", a.copiedMsg)
	}
}

func TestBulkKillCountsOnlySuccessfulKills(t *testing.T) {
	stubTmux(t, true, nil)
	a := newTestApp(fakeSessions())
	live := []session.Session{a.sessions[0], a.sessions[1]}
	for i := range live {
		live[i].IsLive = true
	}
	_, cmd := a.bulkKill(live)
	a.Update(cmd())
	if a.copiedMsg != "Killed 2" {
		t.Fatalf("copiedMsg = %q, want %q", a.copiedMsg, "Killed 2")
	}

	stubTmux(t, true, errors.New("boom"))
	_, cmd = a.bulkKill(live)
	a.Update(cmd())
	if !strings.HasPrefix(a.copiedMsg, "Killed 0, 2 failed") {
		t.Fatalf("copiedMsg = %q, want failure count", a.copiedMsg)
	}
}

func TestOpenLiveInputLooksUpPaneOffUpdateAndDropsStale(t *testing.T) {
	t.Setenv("TMUX", "/tmp/fake,1,0")
	finds, _ := stubTmux(t, true, nil)
	a := newTestApp(fakeSessions())
	a.editorInput = false

	_, first := a.openLiveInput("/p", "id1")
	_, second := a.openLiveInput("/p", "id2")
	if *finds != 0 {
		t.Fatalf("FindPane ran on Update path %d time(s)", *finds)
	}
	a.Update(first())
	if a.liveInputActive {
		t.Fatal("stale lookup result opened the input modal")
	}
	a.Update(second())
	if !a.liveInputActive {
		t.Fatal("latest lookup result should open the input modal")
	}
}

func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.email=t@example.com", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

// `git worktree add` must not run inside Update: it can take seconds on a
// large repo and froze the UI for that long.
func TestWorktreeNewRunsGitOffUpdate(t *testing.T) {
	t.Setenv("TMUX", "")                               // never spawn a real tmux window from a test
	repo, err := filepath.EvalSymlinks(initGitRepo(t)) // git reports the resolved path
	if err != nil {
		t.Fatal(err)
	}
	a := newTestApp(fakeSessions())
	sel := a.sessions[0]
	sel.ProjectPath = repo

	_, cmd := a.executeNewWorktreeSession(sel, "feat-x")
	wtPath := filepath.Join(repo, a.config.WorktreeDir, "feat-x")
	if _, err := os.Stat(wtPath); err == nil {
		t.Fatal("worktree created synchronously inside Update")
	}
	if cmd == nil {
		t.Fatal("expected a worktree command")
	}
	msg, ok := cmd().(worktreeDoneMsg)
	if !ok {
		t.Fatalf("cmd returned %T", msg)
	}
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("worktree not created by the command: %v (status %q)", err, msg.status)
	}
	if msg.execDir != wtPath {
		t.Fatalf("non-tmux path should take over the terminal in %q, got %q", wtPath, msg.execDir)
	}
}
