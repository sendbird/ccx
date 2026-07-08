package tui

import (
	"strings"
	"testing"

	"github.com/sendbird/ccx/internal/session"
)

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
