package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// writeIndexTranscript writes a .jsonl transcript and returns a Session for it.
func writeIndexTranscript(t *testing.T, dir, name string, lines []string) *Session {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return &Session{FilePath: path, ModTime: fi.ModTime(), ProjectName: name}
}

func userLine(text string) string {
	return fmt.Sprintf(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":%q}]}}`, text)
}

func assistantLine(text string) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":%q}]}}`, text)
}

func toolLine(name, input string) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","name":%q,"input":{"command":%q}}]}}`, name, input)
}

func toolResultLine(text string) string {
	return fmt.Sprintf(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":%q}]}}`, text)
}

func openTestIndex(t *testing.T) (*Index, string) {
	t.Helper()
	dir := t.TempDir()
	ix, err := OpenIndex(dir)
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	t.Cleanup(func() { ix.Close() })
	return ix, dir
}

func syncAll(t *testing.T, ix *Index, sessions []*Session) SyncStats {
	t.Helper()
	stats, err := ix.Sync(context.Background(), sessions, nil)
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	return stats
}

func searchIdx(t *testing.T, ix *Index, sessions []*Session, query string) ([]SearchResult, SearchMode) {
	t.Helper()
	res, mode, err := SearchWithIndex(context.Background(), ix, sessions, ParseSearchQuery(query), 0)
	if err != nil {
		t.Fatalf("SearchWithIndex(%q): %v", query, err)
	}
	return res, mode
}

func TestIndexFindsBasicMatches(t *testing.T) {
	ix, dir := openTestIndex(t)
	s := writeIndexTranscript(t, dir, "a.jsonl", []string{
		userLine("please prune the worktree"),
		assistantLine("removed the stale worktree entry"),
		userLine("unrelated content"),
	})
	syncAll(t, ix, []*Session{s})

	res, mode := searchIdx(t, ix, []*Session{s}, "worktree")
	if len(res) != 2 {
		t.Fatalf("hits = %d, want 2", len(res))
	}
	if mode != SearchModeIndexPartial {
		t.Errorf("mode = %v, want index", mode)
	}
}

// The index must not change what a search means. Any divergence from the
// full scan is a bug, so compare the two on the same corpus.
func TestIndexResultsMatchFullScan(t *testing.T) {
	ix, dir := openTestIndex(t)
	sessions := []*Session{
		writeIndexTranscript(t, dir, "a.jsonl", []string{
			userLine("deploy the worktree now"),
			assistantLine("WORKTREE uppercase mention"),
			toolLine("Bash", "git worktree prune"),
			userLine("worktree and prune together"),
		}),
		writeIndexTranscript(t, dir, "b.jsonl", []string{
			userLine("워크트리를 정리했다"),
			assistantLine("only prune here"),
			toolLine("Read", "worktree config"),
		}),
	}
	syncAll(t, ix, sessions)

	queries := []string{
		"worktree",
		"WORKTREE",
		"worktree prune",
		"worktree -prune",
		`"worktree and prune"`,
		"워크트리",
		"orktre",
		"user:worktree",
		"assistant:worktree",
		"tool:Bash",
		"tool:Bash worktree",
	}

	key := func(r SearchResult) string {
		return fmt.Sprintf("%s|%s|%s", r.Session.FilePath, r.Block.Type, blockSearchText(r.Block))
	}
	keys := func(rs []SearchResult) []string {
		out := make([]string, 0, len(rs))
		for _, r := range rs {
			out = append(out, key(r))
		}
		sort.Strings(out)
		return out
	}

	for _, q := range queries {
		t.Run(q, func(t *testing.T) {
			parsed := ParseSearchQuery(q)
			scan := collectScan(context.Background(), sessions, parsed, 0)

			// tool_result is not indexed, so only compare on corpora where the
			// scan's extra reach cannot matter — this corpus has none.
			idx, _, err := SearchWithIndex(context.Background(), ix, sessions, parsed, 0)
			if err != nil {
				t.Fatal(err)
			}

			got, want := keys(idx), keys(scan)
			if len(got) != len(want) {
				t.Fatalf("index=%d scan=%d\n index=%v\n  scan=%v", len(got), len(want), got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("result %d:\n index=%s\n  scan=%s", i, got[i], want[i])
				}
			}
		})
	}
}

func TestIndexSkipsToolResultAndFallsBack(t *testing.T) {
	ix, dir := openTestIndex(t)
	s := writeIndexTranscript(t, dir, "a.jsonl", []string{
		toolResultLine("secretmarker lives only in tool output"),
	})
	syncAll(t, ix, []*Session{s})

	// Indexed search cannot see it...
	res, mode := searchIdx(t, ix, []*Session{s}, "secretmarker")
	if len(res) != 0 {
		t.Errorf("index returned %d hits for tool_result-only content, want 0", len(res))
	}
	if mode != SearchModeIndexPartial {
		t.Errorf("mode = %v, want partial (so the UI can say so)", mode)
	}

	// ...but the scan does, which is why partial coverage must be surfaced.
	scan := collectScan(context.Background(), []*Session{s}, ParseSearchQuery("secretmarker"), 0)
	if len(scan) != 1 {
		t.Errorf("scan hits = %d, want 1", len(scan))
	}
}

// Terms shorter than a trigram cannot be answered by the index at all; the
// search must silently fall back rather than return nothing.
func TestShortTermFallsBackToScan(t *testing.T) {
	ix, dir := openTestIndex(t)
	s := writeIndexTranscript(t, dir, "a.jsonl", []string{
		userLine("go is short"),
	})
	syncAll(t, ix, []*Session{s})

	if cov := ix.Coverage(ParseSearchQuery("go")); cov != CoverageNone {
		t.Errorf("Coverage(2-char) = %v, want CoverageNone", cov)
	}
	res, mode := searchIdx(t, ix, []*Session{s}, "go")
	if mode != SearchModeScan {
		t.Errorf("mode = %v, want scan", mode)
	}
	if len(res) != 1 {
		t.Errorf("hits = %d, want 1 (fallback must still find it)", len(res))
	}
}

func TestIncrementalSyncReindexesOnlyChangedFiles(t *testing.T) {
	ix, dir := openTestIndex(t)
	a := writeIndexTranscript(t, dir, "a.jsonl", []string{userLine("alpha content")})
	b := writeIndexTranscript(t, dir, "b.jsonl", []string{userLine("bravo content")})
	sessions := []*Session{a, b}

	if st := syncAll(t, ix, sessions); st.Indexed != 2 {
		t.Fatalf("initial Indexed = %d, want 2", st.Indexed)
	}
	// Nothing changed — a re-sync must not rewrite anything.
	if st := syncAll(t, ix, sessions); st.Indexed != 0 {
		t.Errorf("no-op Indexed = %d, want 0", st.Indexed)
	}

	// Append to one file; only it should be reindexed.
	f, err := os.OpenFile(a.FilePath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(userLine("charlie appended") + "\n")
	f.Close()
	// Ensure mtime differs even on coarse-grained filesystems.
	future := time.Now().Add(2 * time.Second)
	os.Chtimes(a.FilePath, future, future)
	fi, _ := os.Stat(a.FilePath)
	a.ModTime = fi.ModTime()

	if st := syncAll(t, ix, sessions); st.Indexed != 1 {
		t.Errorf("after edit Indexed = %d, want 1", st.Indexed)
	}
	if res, _ := searchIdx(t, ix, sessions, "charlie"); len(res) != 1 {
		t.Errorf("appended content hits = %d, want 1", len(res))
	}
	// The old content must still be there exactly once — not duplicated by the
	// reindex, which is the classic contentless-FTS failure.
	if res, _ := searchIdx(t, ix, sessions, "alpha"); len(res) != 1 {
		t.Errorf("pre-existing content hits = %d, want 1 (duplicate rows?)", len(res))
	}
}

func TestSyncDropsDeletedTranscripts(t *testing.T) {
	ix, dir := openTestIndex(t)
	a := writeIndexTranscript(t, dir, "a.jsonl", []string{userLine("alpha content")})
	b := writeIndexTranscript(t, dir, "b.jsonl", []string{userLine("bravo content")})
	syncAll(t, ix, []*Session{a, b})

	os.Remove(b.FilePath)
	st := syncAll(t, ix, []*Session{a})
	if st.Removed != 1 {
		t.Errorf("Removed = %d, want 1", st.Removed)
	}
	if res, _ := searchIdx(t, ix, []*Session{a}, "bravo"); len(res) != 0 {
		t.Errorf("deleted session still returns %d hits", len(res))
	}
}

// A quote in a query must not produce an FTS5 syntax error.
func TestQuotesInQueryAreEscaped(t *testing.T) {
	ix, dir := openTestIndex(t)
	s := writeIndexTranscript(t, dir, "a.jsonl", []string{
		userLine(`he said "worktree" loudly`),
	})
	syncAll(t, ix, []*Session{s})

	for _, q := range []string{`said "worktree`, `"he said \"worktree\" loudly"`} {
		if _, _, err := SearchWithIndex(context.Background(), ix, []*Session{s}, ParseSearchQuery(q), 0); err != nil {
			t.Errorf("query %q: %v", q, err)
		}
	}
}

func TestResultsOrderedByRecency(t *testing.T) {
	ix, dir := openTestIndex(t)
	old := writeIndexTranscript(t, dir, "old.jsonl", []string{userLine("shared marker here")})
	recent := writeIndexTranscript(t, dir, "recent.jsonl", []string{userLine("shared marker here")})

	past := time.Now().Add(-48 * time.Hour)
	os.Chtimes(old.FilePath, past, past)
	fi, _ := os.Stat(old.FilePath)
	old.ModTime = fi.ModTime()

	sessions := []*Session{old, recent}
	syncAll(t, ix, sessions)

	res, _ := searchIdx(t, ix, sessions, "marker")
	if len(res) != 2 {
		t.Fatalf("hits = %d, want 2", len(res))
	}
	if res[0].Session.FilePath != recent.FilePath {
		t.Errorf("first result = %s, want the newer session", filepath.Base(res[0].Session.FilePath))
	}
}

func TestCorruptIndexIsRebuilt(t *testing.T) {
	dir := t.TempDir()
	path := indexFilePath(dir)
	if err := os.WriteFile(path, []byte("this is not a database"), 0o644); err != nil {
		t.Fatal(err)
	}

	ix, err := OpenIndex(dir)
	if err != nil {
		t.Fatalf("OpenIndex on corrupt file: %v", err)
	}
	defer ix.Close()

	s := writeIndexTranscript(t, dir, "a.jsonl", []string{userLine("worktree content")})
	syncAll(t, ix, []*Session{s})
	if res, _ := searchIdx(t, ix, []*Session{s}, "worktree"); len(res) != 1 {
		t.Errorf("hits after rebuild = %d, want 1", len(res))
	}
}

// tool_result is the only block type the index is allowed to omit. Any other
// omission is a silent divergence from the scan, so enumerate the types the
// parser can produce and assert the rule directly. Found the hard way: system_tag
// blocks were dropped, costing real hits on the live corpus.
func TestOnlyToolResultIsExcludedFromIndex(t *testing.T) {
	types := []string{
		"text", "tool_use", "thinking", "system_tag",
		"redacted_thinking", "server_tool_use", "advisor_tool_result",
	}
	for _, ty := range types {
		b := &ContentBlock{Type: ty, Text: "body"}
		if !indexableBlock(b) {
			t.Errorf("block type %q is excluded from the index but the scan matches it", ty)
		}
	}
	if indexableBlock(&ContentBlock{Type: "tool_result", Text: "body"}) {
		t.Error("tool_result must stay out of the index (it is half the corpus)")
	}
}

// A system_tag block must be findable through the index, end to end.
func TestSystemTagBlocksAreIndexed(t *testing.T) {
	ix, dir := openTestIndex(t)
	s := writeIndexTranscript(t, dir, "a.jsonl", []string{
		userLine("<system-reminder>uniquemarker inside a tag</system-reminder>"),
	})
	syncAll(t, ix, []*Session{s})

	res, _ := searchIdx(t, ix, []*Session{s}, "uniquemarker")
	scan := collectScan(context.Background(), []*Session{s}, ParseSearchQuery("uniquemarker"), 0)
	if len(res) != len(scan) {
		t.Errorf("index=%d scan=%d for system_tag content", len(res), len(scan))
	}
	if len(res) == 0 {
		t.Error("system_tag content not searchable via index")
	}
}

func TestSnippetHighlightsMatch(t *testing.T) {
	ix, dir := openTestIndex(t)
	s := writeIndexTranscript(t, dir, "a.jsonl", []string{
		userLine("a long preamble that goes on before the worktree token appears here"),
	})
	syncAll(t, ix, []*Session{s})

	res, _ := searchIdx(t, ix, []*Session{s}, "worktree")
	if len(res) != 1 {
		t.Fatalf("hits = %d, want 1", len(res))
	}
	if !strings.Contains(res[0].Snippet, "worktree") {
		t.Errorf("snippet lacks the match: %q", res[0].Snippet)
	}
}
