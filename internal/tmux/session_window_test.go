package tmux

import (
	"slices"
	"testing"

	"github.com/sendbird/ccx/internal/clauderegistry"
)

// Panes of one window routinely report the same pane_current_path while their
// Claude sessions belong to different projects — the shell simply never left
// the directory the agent was launched from. Path-based attribution collapsed
// those panes into a single project and surfaced only one arbitrary session,
// which is how the refs picker ended up listing another pane's PRs. Every
// session in the window must be attributed by process ancestry instead.
func TestClaudeSessionsInWindowSeparatesPanesSharingACwd(t *testing.T) {
	panes := []Pane{
		{PID: 100, Session: "local", Window: "5", Path: "/exp/cohome"},
		{PID: 200, Session: "local", Window: "5", Path: "/exp/cohome"},
		{PID: 300, Session: "local", Window: "5", Path: "/exp/cohome"},
	}
	live := []clauderegistry.LiveSession{
		{PID: 101, SessionID: "sess-a", CWD: "/exp/cohome"},
		{PID: 201, SessionID: "sess-b", CWD: "/exp/cohome"},
		// Wrapped by ccproxy (299) — the walk must skip past it.
		{PID: 302, SessionID: "sess-c", CWD: "/src/soda-k8s/.worktree/cohome"},
	}
	ppidOf := map[int]int{101: 100, 201: 200, 302: 299, 299: 300, 100: 1, 200: 1, 300: 1}

	got := claudeSessionsInWindow(panes, live, "local", "5", ppidOf)

	want := []string{"sess-a", "sess-b", "sess-c"}
	if !slices.Equal(got, want) {
		t.Errorf("claudeSessionsInWindow = %v, want %v", got, want)
	}
}

// Sessions running in other windows (or outside tmux entirely) must not leak
// into this window's list — that was the original wrong-session bug.
func TestClaudeSessionsInWindowExcludesOtherWindows(t *testing.T) {
	panes := []Pane{
		{PID: 100, Session: "local", Window: "5"},
		{PID: 200, Session: "local", Window: "9"},
		{PID: 300, Session: "other", Window: "5"},
	}
	live := []clauderegistry.LiveSession{
		{PID: 101, SessionID: "mine"},
		{PID: 201, SessionID: "other-window"},
		{PID: 301, SessionID: "other-tmux-session"},
		{PID: 999, SessionID: "orphan"}, // no pane in its ancestry
	}
	ppidOf := map[int]int{101: 100, 201: 200, 301: 300, 999: 1, 100: 1, 200: 1, 300: 1}

	got := claudeSessionsInWindow(panes, live, "local", "5", ppidOf)

	if !slices.Equal(got, []string{"mine"}) {
		t.Errorf("claudeSessionsInWindow = %v, want [mine]", got)
	}
}

// One pane can host nested Claude processes reporting the same session; the
// list is keyed by session ID so a session is never offered twice.
func TestClaudeSessionsInWindowDedupesBySessionID(t *testing.T) {
	panes := []Pane{{PID: 100, Session: "local", Window: "5"}}
	live := []clauderegistry.LiveSession{
		{PID: 101, SessionID: "dup"},
		{PID: 102, SessionID: "dup"},
		{PID: 103, SessionID: ""}, // malformed registry entry
	}
	ppidOf := map[int]int{101: 100, 102: 100, 103: 100, 100: 1}

	got := claudeSessionsInWindow(panes, live, "local", "5", ppidOf)

	if !slices.Equal(got, []string{"dup"}) {
		t.Errorf("claudeSessionsInWindow = %v, want [dup]", got)
	}
}
