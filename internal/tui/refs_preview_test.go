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
