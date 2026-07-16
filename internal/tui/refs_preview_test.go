package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sendbird/ccx/internal/session"
)

// TestSetRefsPreviewModeDispatchesExtract guards the "stuck on Resolving…" bug:
// entering References mode must return a tea.Cmd that actually runs the offline
// extract for the selected session. Previously setSessPreviewMode returned no
// cmd and relied on the View render path (which discards cmds) plus a tick
// flush, so a session could sit on "Resolving…" indefinitely.
func TestSetRefsPreviewModeDispatchesExtract(t *testing.T) {
	sessions := fakeSessions()
	sessions[0].HasRefs = true                       // selected session has links to extract
	sessions[0].FilePath = "/tmp/proj-a/aaa.jsonl"   // non-empty so the extract cmd is created (file need not exist)
	a := newTestApp(sessions)
	a.sessionList.Select(0)

	cmd := a.setSessPreviewMode(sessPreviewRefs)
	if cmd == nil {
		t.Fatal("setSessPreviewMode(refs) returned nil cmd — extract never dispatched, pane stays on Resolving…")
	}
	got := map[string]bool{}
	collectExtractedIDs(cmd, got)
	if !got[sessions[0].ID] {
		t.Errorf("no refsExtractedMsg dispatched for selected session %q", sessions[0].ID)
	}
	// The dedup guard must be armed so repeated renders don't re-parse.
	if !a.refsInFlight[sessions[0].ID] {
		t.Errorf("refsInFlight not set for %q after dispatching extract", sessions[0].ID)
	}
}

// TestLiveSessionDispatchesExtractWithoutHasRefs guards the stale-HasRefs bug:
// a live (in-progress) session grows after the scan that set HasRefs, so a PR
// URL written moments ago (e.g. a PR just created in that very session) hasn't
// flipped HasRefs yet. Entering References mode must still dispatch the offline
// extract for a live session even when HasRefs is false, so freshly-added links
// surface without waiting for a rescan/refresh.
func TestLiveSessionDispatchesExtractWithoutHasRefs(t *testing.T) {
	sessions := fakeSessions()
	a := newTestApp(sessions)
	// Force the hermetic state the bug is about: selected session is live but its
	// HasRefs snapshot is stale (false). Set on a.sessions (the store that
	// updateSessionRefsPreview reads via sessionByIDFromStore), and pin the id.
	a.sessions[0].HasRefs = false
	a.sessions[0].IsLive = true
	a.sessions[0].FilePath = "/tmp/proj-a/aaa.jsonl" // non-empty so extract cmd is created
	a.sessions[0].RefsResolved = false
	a.sessionList.Select(0)

	cmd := a.setSessPreviewMode(sessPreviewRefs)
	if cmd == nil {
		t.Fatal("setSessPreviewMode(refs) returned nil cmd for live session with stale HasRefs — extract never dispatched")
	}
	got := map[string]bool{}
	collectExtractedIDs(cmd, got)
	if !got[sessions[0].ID] {
		t.Errorf("no refsExtractedMsg dispatched for live session %q with HasRefs=false", sessions[0].ID)
	}
	if !a.refsInFlight[sessions[0].ID] {
		t.Errorf("refsInFlight not set for %q after dispatching extract", sessions[0].ID)
	}
	// A live session with no extracted refs yet must show "Resolving…", not the
	// misleading "No PR or Jira links".
	if !strings.Contains(a.sessRefsCache, "Resolving") {
		t.Errorf("live session with pending extract should render Resolving…, got:\n%s", a.sessRefsCache)
	}
}

// collectExtractedIDs runs a (possibly batched) tea.Cmd and records the session
// IDs of any refsExtractedMsg it produces.
func collectExtractedIDs(cmd tea.Cmd, out map[string]bool) {
	if cmd == nil {
		return
	}
	msg := cmd()
	switch m := msg.(type) {
	case refsExtractedMsg:
		out[m.id] = true
	case tea.BatchMsg:
		for _, c := range m {
			collectExtractedIDs(c, out)
		}
	}
}

func TestRenderSessionRefs(t *testing.T) {
	a := &App{}
	// No refs at all.
	if got := a.renderSessionRefs(nil, 80, false, false); !strings.Contains(got, "No PR or Jira") {
		t.Errorf("empty render missing hint: %q", got)
	}
	// Has links but not yet resolved.
	if got := a.renderSessionRefs(nil, 80, true, false); !strings.Contains(got, "Resolving") {
		t.Errorf("pending render missing hint: %q", got)
	}
	// Has links, resolve completed, but none were resolvable.
	if got := a.renderSessionRefs(nil, 80, true, true); !strings.Contains(got, "No resolvable") {
		t.Errorf("resolved-empty render missing hint: %q", got)
	}

	refs := []session.SessionRef{
		{Kind: session.RefPR, Label: "sendbird/ccx#52", State: session.RefStateOpen, ReviewDecision: "APPROVED", ChecksState: "SUCCESS", Resolved: true},
		{Kind: session.RefPR, Label: "sendbird/ccx#40", State: session.RefStateMerged, Resolved: true},
		{Kind: session.RefJira, Label: "CPLAT-1234", JiraStatus: "In Progress", Resolved: true},
	}
	out := a.renderSessionRefs(orderRefs(refs), 80, true, true)
	for _, want := range []string{"Pull Requests", "sendbird/ccx#52", "OPEN", "Jira Issues", "CPLAT-1234", "In Progress"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q in:\n%s", want, out)
		}
	}

	// Cursor highlight only appears when the pane is focused.
	a.sessSplit.Focus = true
	a.sessRefsCursor = 0
	if got := a.renderSessionRefs(orderRefs(refs), 80, true, true); !strings.Contains(got, "> ") {
		t.Errorf("focused render missing cursor marker:\n%s", got)
	}
	if !strings.Contains(a.renderSessionRefs(orderRefs(refs), 80, true, true), "↵:open") {
		t.Error("focused render missing open hint")
	}
}

// TestRefsPreviewEnterOpensURL guards the reported bug: with the References
// preview focused, Enter on a PR/Jira entry must open its URL in the browser —
// NOT fall through to km.Session.Open and open the conversation. `o` already
// worked; Enter was intercepted by the Session.Open case, which had no refs
// branch. Both Enter and `o` must open the URL and leave the view unchanged.
func TestRefsPreviewEnterOpensURL(t *testing.T) {
	for _, key := range []tea.KeyMsg{
		{Type: tea.KeyEnter},
		{Type: tea.KeyRunes, Runes: []rune{'o'}},
	} {
		sessions := fakeSessions()
		a := newTestApp(sessions)
		a.sessionList.Select(0)

		// Simulate a focused, populated References preview.
		a.sessPreviewMode = sessPreviewRefs
		a.sessSplit.Show = true
		a.sessSplit.Focus = true
		a.sessPreviewRefs = []session.SessionRef{
			{Kind: session.RefPR, URL: "https://github.com/sendbird/ccx/pull/62",
				Label: "sendbird/ccx#62", State: session.RefStateOpen, Resolved: true},
		}
		a.sessRefsCursor = 0

		var opened string
		a.openURL = func(u string) error { opened = u; return nil }

		m, _ := a.Update(key)
		got := m.(*App)

		if opened != "https://github.com/sendbird/ccx/pull/62" {
			t.Errorf("key %v: expected the ref URL to be opened, got %q", key, opened)
		}
		if got.state != viewSessions {
			t.Errorf("key %v: view changed to %v — Enter leaked to openConversation", key, got.state)
		}
	}
}

// refsForMultiSelectTest returns three distinct refs (2 PRs, 1 Jira) for
// exercising multi-selection over the References preview.
func refsForMultiSelectTest() []session.SessionRef {
	return []session.SessionRef{
		{Kind: session.RefPR, URL: "https://github.com/sendbird/ccx/pull/62",
			Label: "sendbird/ccx#62", State: session.RefStateOpen, Resolved: true},
		{Kind: session.RefPR, URL: "https://github.com/sendbird/ccx/pull/40",
			Label: "sendbird/ccx#40", State: session.RefStateMerged, Resolved: true},
		{Kind: session.RefJira, URL: "https://sendbird.atlassian.net/browse/CPLAT-1234",
			Label: "CPLAT-1234", JiraStatus: "In Progress", Resolved: true},
	}
}

// setupFocusedRefsPreview builds an App with a focused, populated References
// preview ready for key-handling tests. The refs are set on both
// sessPreviewRefs (what the key handlers read) and the backing session's
// Refs field (what updateSessionRefsPreview reloads from on every toggle —
// see sessionByIDFromStore), so a space/enter/y keypress that re-derives
// sessPreviewRefs from the session store doesn't wipe them out.
func setupFocusedRefsPreview(t *testing.T) *App {
	t.Helper()
	sessions := fakeSessions()
	refs := refsForMultiSelectTest()
	sessions[0].Refs = refs
	sessions[0].HasRefs = true
	sessions[0].RefsResolved = true
	a := newTestApp(sessions)
	a.sessionList.Select(0)
	a.sessPreviewMode = sessPreviewRefs
	a.sessSplit.Show = true
	a.sessSplit.Focus = true
	a.sessPreviewRefs = refs
	a.sessRefsCursor = 0
	// updateSessionRefsPreview clears sessRefsSelected whenever
	// sessRefsCacheID != the session it's rendering — mirror the real
	// startup flow (setSessPreviewMode calls this once already) so the
	// cache ID is primed before a test's first space/enter/y keypress.
	a.sessRefsCacheID = sessions[0].ID
	return a
}

// TestRefsPreviewSpaceTogglesSelection guards multi-selection in the
// References preview: space must toggle the current item into
// sessRefsSelected and advance the cursor, mirroring the URL menu's
// space-to-select behavior.
func TestRefsPreviewSpaceTogglesSelection(t *testing.T) {
	a := setupFocusedRefsPreview(t)

	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	got := m.(*App)

	if !got.sessRefsSelected[a.sessPreviewRefs[0].URL] {
		t.Fatalf("expected ref 0 selected after space, selected=%v", got.sessRefsSelected)
	}
	if got.sessRefsCursor != 1 {
		t.Errorf("expected cursor to advance to 1 after toggle, got %d", got.sessRefsCursor)
	}

	// Toggle it back off.
	got.sessRefsCursor = 0
	m2, _ := got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	got2 := m2.(*App)
	if got2.sessRefsSelected[a.sessPreviewRefs[0].URL] {
		t.Errorf("expected ref 0 deselected after second space toggle, selected=%v", got2.sessRefsSelected)
	}
}

// TestRefsPreviewEnterOpensAllSelected guards that Enter opens every
// multi-selected reference (not just the one under the cursor) when a
// selection exists.
func TestRefsPreviewEnterOpensAllSelected(t *testing.T) {
	a := setupFocusedRefsPreview(t)
	refs := a.sessPreviewRefs

	var opened []string
	a.openURL = func(u string) error { opened = append(opened, u); return nil }

	// Select refs 0 and 2, leave cursor on 1 (unselected) to prove selection
	// wins over cursor position.
	a.sessRefsSelected[refs[0].URL] = true
	a.sessRefsSelected[refs[2].URL] = true
	a.sessRefsCursor = 1

	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := m.(*App)

	if len(opened) != 2 {
		t.Fatalf("expected 2 URLs opened, got %d: %v", len(opened), opened)
	}
	wantSet := map[string]bool{refs[0].URL: true, refs[2].URL: true}
	for _, u := range opened {
		if !wantSet[u] {
			t.Errorf("unexpected URL opened: %q", u)
		}
	}
	if got.state != viewSessions {
		t.Errorf("view changed to %v — Enter should stay in sessions view", got.state)
	}
}

// TestRefsPreviewYCopiesSelected guards that `y` copies every multi-selected
// reference URL (newline-joined) to the clipboard, falling back to the
// cursor item when nothing is selected.
func TestRefsPreviewYCopiesSelected(t *testing.T) {
	a := setupFocusedRefsPreview(t)
	refs := a.sessPreviewRefs

	a.sessRefsSelected[refs[0].URL] = true
	a.sessRefsSelected[refs[1].URL] = true

	m, _, _ := a.copySelectedRefs()
	got := m.(*App)
	if !strings.Contains(got.copiedMsg, "2") {
		t.Errorf("expected copiedMsg to mention 2 refs, got %q", got.copiedMsg)
	}

	// No selection: falls back to the cursor item.
	clear(a.sessRefsSelected)
	a.sessRefsCursor = 1
	m2, _, _ := a.copySelectedRefs()
	got2 := m2.(*App)
	if !strings.Contains(got2.copiedMsg, refs[1].Label) {
		t.Errorf("expected copiedMsg to mention cursor ref %q, got %q", refs[1].Label, got2.copiedMsg)
	}
}

// TestRefsPreviewSelectionClearedOnModeSwitch guards that leaving the
// References preview (esc back to the conversation preview) clears any
// leftover multi-selection so it doesn't leak into a later refs-preview
// session.
func TestRefsPreviewSelectionClearedOnModeSwitch(t *testing.T) {
	a := setupFocusedRefsPreview(t)
	a.sessRefsSelected[a.sessPreviewRefs[0].URL] = true

	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyEscape})
	got := m.(*App)

	if len(got.sessRefsSelected) != 0 {
		t.Errorf("expected selection cleared after leaving refs preview, got %v", got.sessRefsSelected)
	}
}

