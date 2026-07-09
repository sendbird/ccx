package tui

import (
	"testing"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

// These tests cover the "References stuck on Resolving… / no badge" bugs for the
// current-window live session, which reaches the refs preview through paths the
// earlier TestRefsExtractedReflectedInPreview (plain flat-mode session) misses:
//   1. projectCentric group mode (the default) routes a project head row's refs
//      preview — the extract must still be dispatched.
//   2. the phase-2 full scan replaces a.sessions and must carry over refs.
//   3. refsInFlight must not latch when no extract is actually dispatched.
//   4. the open-PR badge must fill in for a projectCentric project row.

// selectSessionRow selects the list row that resolves to the given session ID,
// skipping section headers (e.g. the "Current Window" divider at index 0).
func selectSessionRow(t *testing.T, a *App, id string) {
	t.Helper()
	n := len(a.sessionList.VisibleItems())
	for i := 0; i < n; i++ {
		a.sessionList.Select(i)
		if s, ok := a.selectedSession(); ok && s.ID == id {
			return
		}
	}
	t.Fatalf("no list row resolves to session %q", id)
}

// TestRefsLiveSessionFullFlow drives the real Update loop for a current-window
// live session: enter refs mode → the extract must be dispatched → feeding the
// extract message back must surface the refs (not stay stuck on "Resolving…").
func TestRefsLiveSessionFullFlow(t *testing.T) {
	sessions := []session.Session{
		{ID: "aaa", ShortID: "aaa", ProjectPath: "/tmp/proj-a", ProjectName: "proj-a",
			ModTime: time.Now(), HasRefs: true, FilePath: "/tmp/proj-a/aaa.jsonl",
			IsLive: true, IsCurrentWindow: true},
	}
	a := newTestApp(sessions)
	selectSessionRow(t, a, "aaa")

	cmd := a.setSessPreviewMode(sessPreviewRefs)
	if cmd == nil {
		t.Fatal("entering refs mode returned no cmd — extract never dispatched (stuck on Resolving)")
	}

	extracted := []session.SessionRef{
		{Kind: session.RefPR, URL: "https://github.com/sendbird/ccx/pull/62",
			Label: "sendbird/ccx#62", FirstSeen: time.Now()},
	}
	m, _ := a.Update(refsExtractedMsg{id: "aaa", refs: extracted})
	a = m.(*App)

	if len(a.sessions[0].Refs) != 1 {
		t.Fatalf("a.sessions[0].Refs = %d, want 1", len(a.sessions[0].Refs))
	}
	if got := a.sessRefsCache; !contains(got, "sendbird/ccx#62") {
		t.Errorf("preview missing extracted ref; got:\n%s", got)
	}
	if contains(a.sessRefsCache, "Resolving") {
		t.Errorf("preview STUCK on Resolving after extract landed; got:\n%s", a.sessRefsCache)
	}
}

// TestRefsProjectCentricDispatchesExtract is the primary repro: in the default
// projectCentric group mode the selected row is a projectItem, and refs mode
// must route through the session path so the extract is dispatched. Before the
// fix updateSessionPreview took the project-summary branch and returned nil —
// the extract never fired and the preview stuck on "Resolving…".
func TestRefsProjectCentricDispatchesExtract(t *testing.T) {
	sessions := []session.Session{
		{ID: "aaa", ShortID: "aaa", ProjectPath: "/tmp/proj-a", ProjectName: "proj-a",
			ModTime: time.Now(), HasRefs: true, FilePath: "/tmp/proj-a/aaa.jsonl",
			IsLive: true, IsCurrentWindow: true},
	}
	a := newTestApp(sessions)
	a.sessGroupMode = groupProjectCentric
	a.rebuildSessionList()
	selectSessionRow(t, a, "aaa")

	if _, ok := a.sessionList.SelectedItem().(projectItem); !ok {
		t.Fatalf("precondition: expected the selected row to be a projectItem, got %T",
			a.sessionList.SelectedItem())
	}

	if cmd := a.setSessPreviewMode(sessPreviewRefs); cmd == nil {
		t.Fatal("projectCentric refs mode dispatched no extract cmd — stuck on Resolving")
	}
}

// TestRefsProjectCentricBadgeFillsIn verifies the open-PR badge appears on a
// projectCentric project head row once a ref resolves, via syncSessionRefsToList
// re-summing the row's openPRs.
func TestRefsProjectCentricBadgeFillsIn(t *testing.T) {
	sessions := []session.Session{
		{ID: "aaa", ShortID: "aaa", ProjectPath: "/tmp/proj-a", ProjectName: "proj-a",
			ModTime: time.Now(), HasRefs: true, FilePath: "/tmp/proj-a/aaa.jsonl",
			IsLive: true, IsCurrentWindow: true},
	}
	a := newTestApp(sessions)
	a.sessGroupMode = groupProjectCentric
	a.rebuildSessionList()

	// Resolve an open PR into the source of truth, then sync to the list.
	a.sessions[0].Refs = []session.SessionRef{
		{Kind: session.RefPR, URL: "https://github.com/sendbird/ccx/pull/62",
			Label: "sendbird/ccx#62", State: session.RefStateOpen, Resolved: true},
	}
	a.sessions[0].RefsResolved = true
	if !a.syncSessionRefsToList("aaa") {
		t.Fatal("syncSessionRefsToList did not find the projectItem row")
	}

	for _, item := range a.sessionList.Items() {
		if pi, ok := item.(projectItem); ok {
			if pi.openPRs != 1 {
				t.Errorf("project row openPRs = %d, want 1 (badge would not show)", pi.openPRs)
			}
			return
		}
	}
	t.Fatal("no projectItem row found")
}

// TestRefsSurvivePhase2Scan reproduces the startup race: refs extracted during
// phase-1 must survive the phase-2 full scan (sessionsScannedMsg) replacing
// a.sessions, or the preview reverts to a permanent "Resolving…".
func TestRefsSurvivePhase2Scan(t *testing.T) {
	sessions := []session.Session{
		{ID: "aaa", ShortID: "aaa", ProjectPath: "/tmp/proj-a", ProjectName: "proj-a",
			ModTime: time.Now(), HasRefs: true, FilePath: "/tmp/proj-a/aaa.jsonl",
			IsLive: true, IsCurrentWindow: true},
	}
	a := newTestApp(sessions)

	a.sessions[0].Refs = []session.SessionRef{
		{Kind: session.RefPR, URL: "https://github.com/sendbird/ccx/pull/62",
			Label: "sendbird/ccx#62", State: session.RefStateOpen, Resolved: true},
	}
	a.sessions[0].RefsResolved = true

	fresh := []session.Session{
		{ID: "aaa", ShortID: "aaa", ProjectPath: "/tmp/proj-a", ProjectName: "proj-a",
			ModTime: a.sessions[0].ModTime, HasRefs: true, FilePath: "/tmp/proj-a/aaa.jsonl"},
	}
	m, _ := a.Update(sessionsScannedMsg{sessions: fresh})
	a = m.(*App)

	got, ok := a.sessionByIDFromStore("aaa")
	if !ok {
		t.Fatal("session gone after phase-2 scan")
	}
	if len(got.Refs) != 1 || !got.RefsResolved {
		t.Errorf("phase-2 scan wiped extracted refs: Refs=%d RefsResolved=%v (want 1, true) → stuck on Resolving",
			len(got.Refs), got.RefsResolved)
	}
}

// TestRefsInFlightNotStrandedWhenExtractDropped guards the latch-leak: a session
// with no FilePath yields a nil extract cmd, so refsInFlight must NOT be armed —
// otherwise the guard latches with nothing to clear it and the row is stuck.
func TestRefsInFlightNotStrandedWhenExtractDropped(t *testing.T) {
	sessions := []session.Session{
		{ID: "aaa", ShortID: "aaa", ProjectPath: "/tmp/proj-a", ProjectName: "proj-a",
			ModTime: time.Now(), HasRefs: true, FilePath: "",
			IsLive: true, IsCurrentWindow: true},
	}
	a := newTestApp(sessions)
	selectSessionRow(t, a, "aaa")

	a.setSessPreviewMode(sessPreviewRefs)
	if a.refsInFlight["aaa"] {
		t.Fatal("LEAK: refsInFlight armed but extract cmd was nil — stuck on Resolving forever")
	}
}
