package tui

import (
	"testing"
	"time"
)

// The watcher must stay silent while visibility is unchanged and only emit a
// message on a transition, so an idle app gets no 250ms Update/View churn.
func TestKittyWatchEmitsOnlyOnTransition(t *testing.T) {
	seq := []bool{true, true, true, false}
	calls := 0
	probe := func() bool {
		v := seq[min(calls, len(seq)-1)]
		calls++
		return v
	}
	msg := kittyWatchCmd(true, probe, time.Millisecond)()
	vm, ok := msg.(kittyVisibilityMsg)
	if !ok {
		t.Fatalf("msg = %T, want kittyVisibilityMsg", msg)
	}
	if vm.visible {
		t.Fatal("expected visible=false transition")
	}
	if calls != 4 {
		t.Fatalf("probe calls = %d, want 4 (unchanged polls must not return)", calls)
	}
}

func TestKittyVisibilityMsgTogglesFocusAndRearms(t *testing.T) {
	a := newTestApp(fakeSessions())
	a.termFocused = true

	_, cmd := a.Update(kittyVisibilityMsg{visible: false})
	if a.termFocused {
		t.Fatal("hidden pane should clear termFocused")
	}
	if cmd == nil {
		t.Fatal("watcher must be re-armed")
	}

	_, cmd = a.Update(kittyVisibilityMsg{visible: true})
	if !a.termFocused {
		t.Fatal("visible pane should restore termFocused")
	}
	if cmd == nil {
		t.Fatal("watcher must be re-armed")
	}
}
