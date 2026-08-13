package tmux

import (
	"testing"
	"time"
)

func TestProcTreeFindsWrappedClaude(t *testing.T) {
	// The snapshot must find claude anywhere in a pane's subtree, not just as a
	// direct child — it is routinely launched behind a wrapper.
	tree := &procTree{
		cmd: map[int]string{
			10: "-fish",
			11: "ccproxy run",
			12: "node /usr/local/bin/claude --resume abc",
			20: "-fish",
			21: "vim",
		},
		children: map[int][]int{
			10: {11},
			11: {12},
			20: {21},
		},
	}
	if !tree.hasClaudeUnder(10) {
		t.Error("expected a wrapped claude two levels down to be found")
	}
	if tree.hasClaudeUnder(20) {
		t.Error("expected a pane with no claude to report false")
	}
	if tree.hasClaudeUnder(999) {
		t.Error("expected an unknown pid to report false")
	}
}

func TestProcTreeSurvivesCycles(t *testing.T) {
	// A malformed ps snapshot must not hang the startup path.
	tree := &procTree{
		cmd:      map[int]string{1: "a", 2: "b"},
		children: map[int][]int{1: {2}, 2: {1}},
	}
	done := make(chan bool, 1)
	go func() { done <- tree.hasClaudeUnder(1) }()
	select {
	case got := <-done:
		if got {
			t.Error("expected no claude in this tree")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("hasClaudeUnder did not terminate on a cyclic tree")
	}
}

func TestInvalidateWindowClaudesDropsBothCaches(t *testing.T) {
	// Refresh is the user asking for current reality, so neither the window
	// scan nor the process snapshot may survive it.
	storeWindowClaudes([]string{"/tmp/x"})
	procTreeMu.Lock()
	procTreeVal = &procTree{cmd: map[int]string{}, children: map[int][]int{}}
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
