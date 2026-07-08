package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sendbird/ccx/internal/session"
)

// TestRefsPendingFlushDoesNotDropSessions guards the "stuck on Resolving…" bug:
// the on-demand extract is queued in a map (sessRefsPending), not a single slot,
// so switching sessions before the 3s tick fires can no longer strand an earlier
// session with refsInFlight=true but no extract ever dispatched. handleTick must
// flush ALL pending entries and clear the queue.
func TestRefsPendingFlushDoesNotDropSessions(t *testing.T) {
	a := newTestApp(fakeSessions())
	a.liveUpdate = false // so handleTick returns only the pending-flush batch

	// Two sessions queued their extract via the render path (cmd discarded there).
	a.sessRefsPending["aaa"] = "/tmp/proj-a/aaa.jsonl"
	a.sessRefsPending["bbb"] = "/tmp/proj-b/bbb.jsonl"
	a.refsInFlight["aaa"] = true
	a.refsInFlight["bbb"] = true

	cmd := a.handleTick()

	if len(a.sessRefsPending) != 0 {
		t.Errorf("pending not fully flushed: %v", a.sessRefsPending)
	}
	if cmd == nil {
		t.Fatal("handleTick returned nil cmd despite pending extracts")
	}
	// Draining the batch must yield a refsExtractedMsg for BOTH queued sessions,
	// proving neither was dropped.
	got := map[string]bool{}
	collectExtractedIDs(cmd, got)
	for _, id := range []string{"aaa", "bbb"} {
		if !got[id] {
			t.Errorf("no refsExtractedMsg dispatched for session %q (would stay stuck on Resolving…)", id)
		}
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
