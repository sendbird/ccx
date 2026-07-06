package session

import (
	"testing"
	"time"
)

// waitSession builds a live session that is idle with unfinished work → Wait.
func waitSession(id string) Session {
	return Session{
		ID:          id,
		ProjectName: "proj-" + id,
		IsLive:      true,
		ModTime:     time.Now(),
		Tasks:       []TaskItem{{Status: "in_progress"}},
	}
}

// doneSession builds a session whose work is all completed → Done.
func doneSession(id string) Session {
	return Session{
		ID:          id,
		ProjectName: "proj-" + id,
		Tasks:       []TaskItem{{Status: "completed"}},
	}
}

func TestDiffLifecycle_FiresOnceOnTransition(t *testing.T) {
	prev := map[string]LifecycleState{}
	now := time.Now()

	// First appearance already in Wait → fires once.
	ev := DiffLifecycle(prev, []Session{waitSession("a")}, now)
	if len(ev) != 1 || ev[0].To != LifecycleWait || ev[0].SessionID != "a" {
		t.Fatalf("first scan: got %+v", ev)
	}

	// Same state next tick → no event.
	ev = DiffLifecycle(prev, []Session{waitSession("a")}, now)
	if len(ev) != 0 {
		t.Errorf("unchanged Wait should not re-fire, got %+v", ev)
	}
}

func TestDiffLifecycle_WaitToDone(t *testing.T) {
	prev := map[string]LifecycleState{}
	now := time.Now()

	DiffLifecycle(prev, []Session{waitSession("a")}, now)
	// Now the same session completes its work.
	ev := DiffLifecycle(prev, []Session{doneSession("a")}, now)
	if len(ev) != 1 || ev[0].From != LifecycleWait || ev[0].To != LifecycleDone {
		t.Fatalf("Wait→Done: got %+v", ev)
	}
}

func TestDiffLifecycle_IgnoresBusyChurn(t *testing.T) {
	prev := map[string]LifecycleState{}
	now := time.Now()

	busy := Session{ID: "a", IsResponding: true} // → Busy, not worthy
	if ev := DiffLifecycle(prev, []Session{busy}, now); len(ev) != 0 {
		t.Errorf("Busy should not fire, got %+v", ev)
	}
}

func TestDiffLifecycle_PrunesGoneSessions(t *testing.T) {
	prev := map[string]LifecycleState{}
	now := time.Now()
	DiffLifecycle(prev, []Session{waitSession("a")}, now)
	if _, ok := prev["a"]; !ok {
		t.Fatal("expected a in prev")
	}
	// Session a is gone; b appears.
	DiffLifecycle(prev, []Session{waitSession("b")}, now)
	if _, ok := prev["a"]; ok {
		t.Error("gone session a should be pruned from prev")
	}
	if _, ok := prev["b"]; !ok {
		t.Error("b should be tracked")
	}
}

func TestLifecycleLabel(t *testing.T) {
	if LifecycleLabel(LifecycleWait) != "WAIT" || LifecycleLabel(LifecycleDone) != "DONE" {
		t.Error("bad lifecycle labels")
	}
	if LifecycleLabel(LifecycleNone) != "" {
		t.Error("None should have empty label")
	}
}
