package tmux

import (
	"testing"
	"time"

	tk "github.com/keyolk/tuikit/tmux"
)

func TestHasClaudeFindsWrappedClaude(t *testing.T) {
	// The snapshot must find claude anywhere in a pane's subtree, not just as a
	// direct child — it is routinely launched behind a wrapper.
	withFake(t, tk.NewFake(), `10 1 -fish
11 10 ccproxy run
12 11 node /usr/local/bin/claude --resume abc
20 1 -fish
21 20 vim
`)
	if !HasClaude(10) {
		t.Error("expected a wrapped claude two levels down to be found")
	}
	if !HasClaudeSession(10, "abc") || HasClaudeSession(10, "zzz") {
		t.Error("session match must follow the wrapped claude's args")
	}
	if HasClaude(20) {
		t.Error("expected a pane with no claude to report false")
	}
	if HasClaude(999) {
		t.Error("expected an unknown pid to report false")
	}
}

func TestHasClaudeSurvivesCycles(t *testing.T) {
	// A malformed ps snapshot must not hang the startup path.
	withFake(t, tk.NewFake(), "1 2 a\n2 1 b\n")
	done := make(chan bool, 1)
	go func() { done <- HasClaude(1) }()
	select {
	case got := <-done:
		if got {
			t.Error("expected no claude in this tree")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HasClaude did not terminate on a cyclic tree")
	}
}

func TestInvalidateWindowClaudesDropsBothCaches(t *testing.T) {
	// Refresh is the user asking for current reality, so neither the window
	// scan nor the process snapshot may survive it.
	storeWindowClaudes([]string{"/tmp/x"})
	procTreeMu.Lock()
	procTreeVal = tk.Procs{}
	procTreeAt = time.Now()
	procTreeMu.Unlock()

	InvalidateWindowClaudes()

	if _, ok := cachedWindowClaudes(); ok {
		t.Error("expected the window scan cache to be dropped")
	}
	procTreeMu.Lock()
	stale := procTreeVal != nil
	procTreeMu.Unlock()
	if stale {
		t.Error("expected the process snapshot to be dropped")
	}
}

func TestWindowClaudesCacheExpires(t *testing.T) {
	storeWindowClaudes([]string{"/tmp/x"})
	if _, ok := cachedWindowClaudes(); !ok {
		t.Fatal("expected a fresh store to be a cache hit")
	}
	windowClaudesMu.Lock()
	windowClaudesAt = time.Now().Add(-windowClaudesTTL - time.Second)
	windowClaudesMu.Unlock()
	if _, ok := cachedWindowClaudes(); ok {
		t.Error("expected an expired entry to miss so live state is re-read")
	}
}
