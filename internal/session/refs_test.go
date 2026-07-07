package session

import "testing"

func TestExtractSessionRefs(t *testing.T) {
	entries := []Entry{
		{Content: []ContentBlock{
			{Type: "text", Text: "PR up at https://github.com/sendbird/ccx/pull/52 and ticket https://sendbird.atlassian.net/browse/CPLAT-1234"},
		}},
		{Content: []ContentBlock{
			// duplicate PR + a non-PR github link (issues) that must be ignored
			{Type: "text", Text: "see https://github.com/sendbird/ccx/pull/52 again, also https://github.com/sendbird/ccx/issues/9"},
		}},
	}
	refs := ExtractSessionRefs(entries)
	if len(refs) != 2 {
		t.Fatalf("want 2 refs, got %d: %+v", len(refs), refs)
	}
	// PRs sort before Jira.
	if refs[0].Kind != RefPR || refs[0].Label != "sendbird/ccx#52" {
		t.Errorf("ref[0] = %+v, want PR sendbird/ccx#52", refs[0])
	}
	if refs[1].Kind != RefJira || refs[1].Label != "CPLAT-1234" {
		t.Errorf("ref[1] = %+v, want Jira CPLAT-1234", refs[1])
	}
}

func TestClassifyRef(t *testing.T) {
	cases := []struct {
		url      string
		wantKind RefKind
		wantLbl  string
		wantOK   bool
	}{
		{"https://github.com/sendbird/ccx/pull/52", RefPR, "sendbird/ccx#52", true},
		{"https://github.com/sendbird/ccx/pull/52#discussion_r1", RefPR, "sendbird/ccx#52", true},
		{"https://sendbird.atlassian.net/browse/CPLAT-9497", RefJira, "CPLAT-9497", true},
		{"https://github.com/sendbird/ccx/issues/9", "", "", false},
		{"https://example.com/foo", "", "", false},
	}
	for _, c := range cases {
		ref, ok := classifyRef(c.url)
		if ok != c.wantOK {
			t.Errorf("%s: ok=%v want %v", c.url, ok, c.wantOK)
			continue
		}
		if !ok {
			continue
		}
		if ref.Kind != c.wantKind || ref.Label != c.wantLbl {
			t.Errorf("%s: got %s/%s want %s/%s", c.url, ref.Kind, ref.Label, c.wantKind, c.wantLbl)
		}
	}
}

func TestSessionRefIsOpen(t *testing.T) {
	cases := []struct {
		ref  SessionRef
		want bool
	}{
		{SessionRef{Kind: RefPR, State: RefStateOpen}, true},
		{SessionRef{Kind: RefPR, State: RefStateDraft}, true},
		{SessionRef{Kind: RefPR, State: RefStateMerged}, false},
		{SessionRef{Kind: RefPR, State: RefStateClosed}, false},
		{SessionRef{Kind: RefJira, Resolved: true, JiraStatusDone: true}, false},
		{SessionRef{Kind: RefJira, Resolved: true, JiraStatusDone: false}, true},
		{SessionRef{Kind: RefJira, Resolved: false}, true}, // unresolved → surfaced
	}
	for i, c := range cases {
		if got := c.ref.IsOpen(); got != c.want {
			t.Errorf("case %d: IsOpen()=%v want %v (%+v)", i, got, c.want, c.ref)
		}
	}
}

func TestRollupChecks(t *testing.T) {
	mk := func(states ...string) ghPRView {
		var v ghPRView
		for _, s := range states {
			v.StatusCheckRollup = append(v.StatusCheckRollup, struct {
				State      string `json:"state"`
				Conclusion string `json:"conclusion"`
				Status     string `json:"status"`
			}{Conclusion: s, Status: "COMPLETED"})
		}
		return v
	}
	if got := rollupChecks(ghPRView{}); got != "" {
		t.Errorf("empty rollup = %q, want empty", got)
	}
	if got := rollupChecks(mk("SUCCESS", "SUCCESS")); got != "SUCCESS" {
		t.Errorf("all success = %q, want SUCCESS", got)
	}
	if got := rollupChecks(mk("SUCCESS", "FAILURE")); got != "FAILURE" {
		t.Errorf("one failure = %q, want FAILURE", got)
	}
}
