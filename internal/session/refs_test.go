package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestExtractSessionRefsFromFile verifies the raw-line fast path: it parses
// URLs and per-line timestamps out of real JSONL without a full JSON decode,
// dedups by label, and orders most-recent-first.
func TestExtractSessionRefsFromFile(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "s.jsonl")
	// Two assistant lines with RFC3339 timestamps; #52 appears first (older),
	// #99 later (newer). Claude Code JSONL does not escape slashes, so URLs
	// appear in normal form.
	lines := []string{
		`{"type":"assistant","timestamp":"2026-07-01T10:00:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"PR https://github.com/sendbird/ccx/pull/52 and CPLAT-1 https://sendbird.atlassian.net/browse/CPLAT-1234"}]}}`,
		`{"type":"assistant","timestamp":"2026-07-02T10:00:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"newer https://github.com/sendbird/ccx/pull/99"}]}}`,
		`{"type":"assistant","timestamp":"2026-07-03T10:00:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"dup https://github.com/sendbird/ccx/pull/52#discussion_r1"}]}}`,
	}
	if err := os.WriteFile(fp, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	refs := ExtractSessionRefsFromFile(fp)
	// #52, #99 (deduped), CPLAT-1234 → 3 refs; PRs before Jira, newest PR first.
	if len(refs) != 3 {
		t.Fatalf("want 3 refs, got %d: %+v", len(refs), refs)
	}
	if refs[0].Label != "sendbird/ccx#99" || refs[1].Label != "sendbird/ccx#52" {
		t.Errorf("PR order = [%s, %s], want [#99, #52]", refs[0].Label, refs[1].Label)
	}
	if refs[2].Kind != RefJira || refs[2].Label != "CPLAT-1234" {
		t.Errorf("ref[2] = %+v, want Jira CPLAT-1234", refs[2])
	}
	// Timestamp parsed from the line, earliest kept on dedup for #52.
	for _, r := range refs {
		if r.FirstSeen.IsZero() {
			t.Errorf("%s missing FirstSeen", r.Label)
		}
		if r.Label == "sendbird/ccx#52" && r.FirstSeen.Day() != 1 {
			t.Errorf("#52 FirstSeen = %v, want Jul 1 (earliest)", r.FirstSeen)
		}
	}
}

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
		{"https://claude.ai/code/artifact/d248181d-78cb-471d-9d94-56bea9242b23", RefArtifact, "artifact:d248181d", true},
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

// TestClassifyURLRef guards the exported wrapper URL menus use to detect PR/Jira
// links; it must agree with the internal classifyRef.
func TestClassifyURLRef(t *testing.T) {
	if ref, ok := ClassifyURLRef("https://github.com/sendbird/ccx/pull/52"); !ok || ref.Kind != RefPR || ref.Label != "sendbird/ccx#52" {
		t.Errorf("PR: got %+v ok=%v", ref, ok)
	}
	if ref, ok := ClassifyURLRef("https://sendbird.atlassian.net/browse/CPLAT-9497"); !ok || ref.Kind != RefJira || ref.Label != "CPLAT-9497" {
		t.Errorf("Jira: got %+v ok=%v", ref, ok)
	}
	if _, ok := ClassifyURLRef("https://example.com/foo"); ok {
		t.Error("non-PR/Jira URL should not classify")
	}
}

// TestRefStatusText covers the plain-text status summary shown in URL-menu rows.
func TestRefStatusText(t *testing.T) {
	cases := []struct {
		name string
		ref  SessionRef
		want string
	}{
		{"pr unresolved", SessionRef{Kind: RefPR}, "…"},
		{"pr resolved no-status", SessionRef{Kind: RefPR, Resolved: true}, ""},
		{"pr open approved success", SessionRef{Kind: RefPR, State: RefStateOpen, ReviewDecision: "APPROVED", ChecksState: "SUCCESS", Resolved: true}, "OPEN · approved · checks ✓"},
		{"pr merged", SessionRef{Kind: RefPR, State: RefStateMerged, Resolved: true}, "MERGED"},
		{"pr changes failing", SessionRef{Kind: RefPR, State: RefStateOpen, ReviewDecision: "CHANGES_REQUESTED", ChecksState: "FAILURE", Resolved: true}, "OPEN · changes requested · checks ✗"},
		{"jira unresolved", SessionRef{Kind: RefJira}, "…"},
		{"jira in progress", SessionRef{Kind: RefJira, JiraStatus: "In Progress", Resolved: true}, "In Progress"},
		{"jira resolved no-status", SessionRef{Kind: RefJira, Resolved: true}, ""},
		{"artifact", SessionRef{Kind: RefArtifact}, "published"},
	}
	for _, c := range cases {
		if got := RefStatusText(c.ref); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// TestRefStatusVariants covers the progressively-shorter status forms a
// width-constrained list pane picks from. Variants must be longest-first,
// deduplicated, and always start with the full RefStatusText.
func TestRefStatusVariants(t *testing.T) {
	cases := []struct {
		name string
		ref  SessionRef
		want []string
	}{
		{
			"pr full", SessionRef{Kind: RefPR, State: RefStateOpen, ReviewDecision: "APPROVED", ChecksState: "SUCCESS", Resolved: true},
			[]string{"OPEN · approved · checks ✓", "OPEN · checks ✓", "OPEN"},
		},
		{
			"pr no checks", SessionRef{Kind: RefPR, State: RefStateOpen, ReviewDecision: "APPROVED", Resolved: true},
			[]string{"OPEN · approved", "OPEN"},
		},
		{
			"pr state only", SessionRef{Kind: RefPR, State: RefStateMerged, Resolved: true},
			[]string{"MERGED"},
		},
		{"pr unresolved", SessionRef{Kind: RefPR}, []string{"…"}},
		{"pr resolved empty", SessionRef{Kind: RefPR, Resolved: true}, nil},
		{"jira", SessionRef{Kind: RefJira, JiraStatus: "In Progress", Resolved: true}, []string{"In Progress"}},
		{"artifact", SessionRef{Kind: RefArtifact}, []string{"published"}},
	}
	for _, c := range cases {
		got := RefStatusVariants(c.ref)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: variant %d got %q want %q", c.name, i, got[i], c.want[i])
			}
		}
		// Widths must be non-increasing so callers can take the first that fits.
		for i := 1; i < len(got); i++ {
			if len([]rune(got[i])) > len([]rune(got[i-1])) {
				t.Errorf("%s: variant %d (%q) is longer than %q", c.name, i, got[i], got[i-1])
			}
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
		{SessionRef{Kind: RefPR, State: RefStateUnknown, Resolved: false}, true},
		{SessionRef{Kind: RefPR, State: RefStateUnknown, Resolved: true}, true}, // failed resolve → surfaced
		{SessionRef{Kind: RefJira, Resolved: true, JiraStatusDone: true}, false},
		{SessionRef{Kind: RefJira, Resolved: true, JiraStatusDone: false}, true},
		{SessionRef{Kind: RefJira, Resolved: false}, true}, // unresolved → surfaced
		{SessionRef{Kind: RefArtifact}, true},              // artifacts always surface
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

// TestClearRefCache verifies the manual-refresh cache invalidator empties the
// process-wide TTL cache (and clears the Jira auth breaker) so a subsequent
// lookup misses and would re-fetch.
func TestClearRefCache(t *testing.T) {
	// Seed a fresh cache entry and confirm it is a hit.
	setCachedRef(SessionRef{URL: "https://example.test/pr/1", Resolved: true, State: RefStateOpen})
	if _, ok := getCachedRef("https://example.test/pr/1"); !ok {
		t.Fatal("precondition: seeded ref should be a cache hit")
	}
	// Trip the Jira breaker so we can confirm ClearRefCache resets it.
	markJiraAuthFailed()
	if !jiraAuthIsFailed() {
		t.Fatal("precondition: jira auth breaker should be tripped")
	}

	ClearRefCache()

	if _, ok := getCachedRef("https://example.test/pr/1"); ok {
		t.Error("ClearRefCache did not drop the cached ref")
	}
	if jiraAuthIsFailed() {
		t.Error("ClearRefCache did not reset the jira auth breaker")
	}
}
