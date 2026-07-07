package session

import (
	"strings"
	"testing"
	"time"
)

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

// TestExtractSessionRefsDedupAndTimestamp verifies label-keyed dedup (the same
// PR via different URL forms collapses to one) keeps the EARLIEST FirstSeen, and
// that within a kind refs sort most-recent-first.
func TestExtractSessionRefsDedupAndTimestamp(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Hour)
	t2 := t0.Add(2 * time.Hour)
	entries := []Entry{
		{Timestamp: t0, Content: []ContentBlock{
			{Type: "text", Text: "first https://github.com/sendbird/ccx/pull/52"},
		}},
		{Timestamp: t1, Content: []ContentBlock{
			// same PR via an anchor URL — must dedup to #52, keep t0 as FirstSeen
			{Type: "text", Text: "again https://github.com/sendbird/ccx/pull/52#discussion_r1"},
		}},
		{Timestamp: t2, Content: []ContentBlock{
			{Type: "text", Text: "newer https://github.com/sendbird/ccx/pull/99"},
		}},
	}
	refs := ExtractSessionRefs(entries)
	if len(refs) != 2 {
		t.Fatalf("want 2 deduped PRs, got %d: %+v", len(refs), refs)
	}
	// Most-recent-first: #99 (t2) before #52 (t0).
	if refs[0].Label != "sendbird/ccx#99" || refs[1].Label != "sendbird/ccx#52" {
		t.Errorf("order = [%s, %s], want [#99, #52]", refs[0].Label, refs[1].Label)
	}
	// #52 keeps its earliest appearance (t0), not the anchor URL's t1.
	for _, r := range refs {
		if r.Label == "sendbird/ccx#52" && !r.FirstSeen.Equal(t0) {
			t.Errorf("#52 FirstSeen = %v, want %v", r.FirstSeen, t0)
		}
	}
}

// TestExtractSessionRefsSkipsCompareURL guards that GitHub "/pull/new/<branch>"
// compare-page URLs (which `gh pr view` can never resolve) are not tracked as PRs.
func TestExtractSessionRefsSkipsCompareURL(t *testing.T) {
	entries := []Entry{
		{Content: []ContentBlock{
			{Type: "text", Text: "create it at https://github.com/sendbird/ccx/pull/new/CPLAT-10756-refs then real one https://github.com/sendbird/ccx/pull/56"},
		}},
	}
	refs := ExtractSessionRefs(entries)
	if len(refs) != 1 {
		t.Fatalf("want 1 ref (compare URL skipped), got %d: %+v", len(refs), refs)
	}
	if refs[0].Label != "sendbird/ccx#56" {
		t.Errorf("ref = %+v, want sendbird/ccx#56", refs[0])
	}
}

// TestExtractSessionRefsGlued guards the regression where two URLs separated by
// a literal "\n" (raw JSON escape in the transcript) were concatenated into one
// ref, and where prose glued onto a PR number / Jira key leaked into the label.
func TestExtractSessionRefsGlued(t *testing.T) {
	entries := []Entry{
		{Content: []ContentBlock{
			// Two URLs glued by a literal backslash-n (as stored in raw JSONL).
			{Type: "text", Text: `https://github.com/sendbird/delight-ops-k8s/pull/431\nhttps://sendbird.atlassian.net/browse/CPLAT-10747`},
			// PR number with trailing prose, and a Jira key with a suffix word.
			{Type: "text", Text: "https://github.com/sendbird/delight-ops-k8s/pull/435CPLAT-10747 https://sendbird.atlassian.net/browse/CPLAT-10753Follow-up"},
		}},
	}
	refs := ExtractSessionRefs(entries)
	got := map[string]bool{}
	for _, r := range refs {
		got[r.Label] = true
	}
	for _, want := range []string{
		"sendbird/delight-ops-k8s#431",
		"sendbird/delight-ops-k8s#435",
		"CPLAT-10747",
		"CPLAT-10753",
	} {
		if !got[want] {
			t.Errorf("missing ref label %q; got %+v", want, refs)
		}
	}
	// The glued forms must NOT appear.
	for label := range got {
		if strings.Contains(label, "https:") || strings.Contains(label, "Follow-up") {
			t.Errorf("label not trimmed: %q", label)
		}
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
		// "/pull/new/<branch>" is the compare/create page, not a real PR.
		{"https://github.com/sendbird/ccx/pull/new/CPLAT-10756-refs", "", "", false},
		{"https://github.com/sendbird/ccx/pull/new/feat/rendering", "", "", false},
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
