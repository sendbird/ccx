package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

// TestRefsExtractedReflectedInPreview guards the root cause of the recurring
// "stuck on Resolving…" bug. The async extract handler updates a.sessions[i].Refs
// but does NOT rebuild the session list, so selectedSession() (the list widget's
// snapshot copy) stays stale. The preview must read the authoritative session
// from a.sessions, or it renders "Resolving…" forever even after extraction
// completed.
func TestRefsExtractedReflectedInPreview(t *testing.T) {
	sessions := []session.Session{
		{ID: "aaa", ShortID: "aaa", ProjectPath: "/tmp/proj-a", ProjectName: "proj-a",
			ModTime: time.Now(), HasRefs: true, FilePath: "/tmp/proj-a/aaa.jsonl"},
	}
	a := newTestApp(sessions)
	a.sessionList.Select(0)
	a.sessSplit.Show = true

	// Enter refs mode (dispatches extract; arms refsInFlight).
	_ = a.setSessPreviewMode(sessPreviewRefs)

	// Simulate the offline extract landing with one PR ref.
	extracted := []session.SessionRef{
		{Kind: session.RefPR, URL: "https://github.com/sendbird/ccx/pull/52",
			Label: "sendbird/ccx#52", FirstSeen: time.Now()},
	}
	m, _ := a.Update(refsExtractedMsg{id: "aaa", refs: extracted})
	a = m.(*App)

	// a.sessions is the source of truth — it must carry the extracted ref.
	if len(a.sessions[0].Refs) != 1 {
		t.Fatalf("a.sessions[0].Refs = %d, want 1", len(a.sessions[0].Refs))
	}
	// The rendered preview must show the ref, not the stale "Resolving…" state.
	if !strings.Contains(a.sessRefsCache, "sendbird/ccx#52") {
		t.Errorf("preview missing extracted ref; got:\n%s", a.sessRefsCache)
	}
	if strings.Contains(a.sessRefsCache, "Resolving") {
		t.Errorf("preview stuck on Resolving after extract landed; got:\n%s", a.sessRefsCache)
	}
}

// TestRefsSurviveRescan guards the second half of the same root cause: a full
// rescan (manual refresh / new-session autorefresh) replaces a.sessions with a
// freshly-scanned slice that has HasRefs but empty Refs. Resolved status must
// carry over, or the preview flips back to "Resolving…" / "No resolvable".
func TestRefsSurviveRescan(t *testing.T) {
	sessions := []session.Session{
		{ID: "aaa", ShortID: "aaa", ProjectPath: "/tmp/proj-a", ProjectName: "proj-a",
			ModTime: time.Now(), HasRefs: true, FilePath: "/tmp/proj-a/aaa.jsonl"},
	}
	a := newTestApp(sessions)
	// Pretend a resolve pass already completed. Pin ModTime so the transcript
	// reads as unchanged across the rescan (the "grew since resolve" path is
	// covered separately by TestCarryOverRefStateReresolvesOnTranscriptGrowth).
	mtime := time.Now()
	a.sessions[0].ModTime = mtime
	a.sessions[0].Refs = []session.SessionRef{
		{Kind: session.RefPR, URL: "https://github.com/sendbird/ccx/pull/52",
			Label: "sendbird/ccx#52", State: session.RefStateOpen, Resolved: true},
	}
	a.sessions[0].RefsResolved = true

	// A fresh scan yields the same session but without resolved ref state.
	fresh := []session.Session{
		{ID: "aaa", ShortID: "aaa", ProjectPath: "/tmp/proj-a", ProjectName: "proj-a",
			ModTime: mtime, HasRefs: true, FilePath: "/tmp/proj-a/aaa.jsonl"},
	}
	a.carryOverRefState(fresh)

	if len(fresh[0].Refs) != 1 || !fresh[0].RefsResolved {
		t.Errorf("rescan dropped resolved refs: Refs=%d RefsResolved=%v",
			len(fresh[0].Refs), fresh[0].RefsResolved)
	}
}

// TestCarryOverRefStateReresolvesOnTranscriptGrowth: when a resolved session's
// transcript changed since resolution, the cached refs stay visible but
// RefsResolved is cleared so the next preview open re-resolves and picks up any
// newly-added links.
func TestCarryOverRefStateReresolvesOnTranscriptGrowth(t *testing.T) {
	a := &App{refsInFlight: map[string]bool{}}
	old := time.Now().Add(-time.Hour)
	a.sessions = []session.Session{{
		ID: "aaa", ModTime: old, HasRefs: true, RefsResolved: true,
		Refs: []session.SessionRef{{Kind: session.RefPR, Label: "x#1", Resolved: true}},
	}}
	fresh := []session.Session{{ID: "aaa", ModTime: time.Now(), HasRefs: true}} // grew
	a.carryOverRefState(fresh)

	if len(fresh[0].Refs) != 1 {
		t.Errorf("cached refs should stay visible across rescan, got %d", len(fresh[0].Refs))
	}
	if fresh[0].RefsResolved {
		t.Error("RefsResolved should be cleared so re-resolve picks up new links")
	}
}

// TestCarryOverRefStateKeepsMidFlight: a session still resolving (extracted Refs
// present, RefsResolved=false) must keep its Refs across a rescan so the in-
// flight refStatusMsg has a list to merge into — otherwise it strands on a bogus
// "No resolvable references".
func TestCarryOverRefStateKeepsMidFlight(t *testing.T) {
	a := &App{refsInFlight: map[string]bool{"aaa": true}}
	a.sessions = []session.Session{{
		ID: "aaa", ModTime: time.Now(), HasRefs: true, RefsResolved: false,
		Refs: []session.SessionRef{{Kind: session.RefPR, Label: "x#1"}},
	}}
	fresh := []session.Session{{ID: "aaa", ModTime: time.Now(), HasRefs: true}}
	a.carryOverRefState(fresh)

	if len(fresh[0].Refs) != 1 {
		t.Errorf("mid-flight refs dropped across rescan, got %d", len(fresh[0].Refs))
	}
}

// TestResolveVisibleRefsCmdOnlyVisibleUnresolved verifies async badge fill-in is
// scoped correctly: resolveVisibleRefsCmd dispatches an extract for on-screen
// sessions that have links but aren't resolved yet, and skips sessions that are
// already resolved, have no links, or have a resolve in flight. This is the
// guard against regressing into the fleet-wide sweep removed in #60.
func TestResolveVisibleRefsCmdOnlyVisibleUnresolved(t *testing.T) {
	sessions := []session.Session{
		{ID: "needs", ShortID: "needs", ProjectPath: "/tmp/a", ProjectName: "a",
			ModTime: time.Now(), HasRefs: true, FilePath: "/tmp/a/needs.jsonl"},
		{ID: "done", ShortID: "done", ProjectPath: "/tmp/b", ProjectName: "b",
			ModTime: time.Now().Add(-time.Minute), HasRefs: true, RefsResolved: true,
			FilePath: "/tmp/b/done.jsonl"},
		{ID: "norefs", ShortID: "norefs", ProjectPath: "/tmp/c", ProjectName: "c",
			ModTime: time.Now().Add(-2 * time.Minute), FilePath: "/tmp/c/norefs.jsonl"},
	}
	a := newTestApp(sessions)

	cmd := a.resolveVisibleRefsCmd()
	if cmd == nil {
		t.Fatal("expected an extract cmd for the unresolved visible session")
	}
	got := map[string]bool{}
	collectExtractedIDs(cmd, got)
	if !got["needs"] {
		t.Error("did not dispatch extract for the unresolved session with links")
	}
	if got["done"] {
		t.Error("re-extracted an already-resolved session")
	}
	if got["norefs"] {
		t.Error("extracted a session that has no links")
	}
	if !a.refsInFlight["needs"] {
		t.Error("refsInFlight guard not armed for the dispatched session")
	}

	// Second call must be a no-op: refsInFlight dedups so a row visible across
	// many ticks is only worked once (no fan-out storm).
	if cmd2 := a.resolveVisibleRefsCmd(); cmd2 != nil {
		got2 := map[string]bool{}
		collectExtractedIDs(cmd2, got2)
		if got2["needs"] {
			t.Error("re-dispatched extract for an in-flight session")
		}
	}
}

// TestSyncSessionRefsToListUpdatesBadge verifies that once a ref resolves in
// a.sessions, syncing it to the list row makes the open-PR badge count reflect
// it — without a full list rebuild.
func TestSyncSessionRefsToListUpdatesBadge(t *testing.T) {
	sessions := []session.Session{
		{ID: "aaa", ShortID: "aaa", ProjectPath: "/tmp/a", ProjectName: "a",
			ModTime: time.Now(), HasRefs: true, FilePath: "/tmp/a/aaa.jsonl"},
	}
	a := newTestApp(sessions)

	// Before resolve: the list row shows no open PRs.
	if row, ok := a.sessionByID("aaa"); ok {
		if prs, _ := row.sess.OpenRefCounts(); prs != 0 {
			t.Fatalf("precondition: expected 0 open PRs, got %d", prs)
		}
	}

	// Resolve an open PR into the source of truth, then sync to the list.
	a.sessions[0].Refs = []session.SessionRef{
		{Kind: session.RefPR, URL: "https://github.com/sendbird/ccx/pull/52",
			Label: "sendbird/ccx#52", State: session.RefStateOpen, Resolved: true},
	}
	a.sessions[0].RefsResolved = true
	if !a.syncSessionRefsToList("aaa") {
		t.Fatal("syncSessionRefsToList did not find the row")
	}

	row, ok := a.sessionByID("aaa")
	if !ok {
		t.Fatal("row missing after sync")
	}
	if prs, _ := row.sess.OpenRefCounts(); prs != 1 {
		t.Errorf("badge count not updated: expected 1 open PR, got %d", prs)
	}
}
