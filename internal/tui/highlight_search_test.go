package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sendbird/ccx/internal/session"
)

// hlCount counts highlighted spans by looking for the ANSI start codes
// highlightLine emits.
func hlCount(s string) int {
	return strings.Count(s, "\x1b[43;30m") + strings.Count(s, "\x1b[46;30m")
}

func TestHighlightSearchTermsPaintsEveryTerm(t *testing.T) {
	content := "the worktree was pruned\nunrelated line\nprune again"
	got := highlightSearchTerms(content, []string{"worktree", "prune"}, -1)

	// worktree(1) + pruned(1) on line 1, prune(1) on line 3.
	if n := hlCount(got); n != 3 {
		t.Errorf("highlighted spans = %d, want 3\n%q", n, got)
	}
	if strings.Contains(strings.Split(got, "\n")[1], "\x1b[43;30m") {
		t.Error("non-matching line was highlighted")
	}
}

// Applying a second term must not corrupt the first term's escapes or match
// them as if they were text.
func TestHighlightSearchTermsOverlappingApplication(t *testing.T) {
	got := highlightSearchTerms("alpha beta gamma", []string{"alpha", "beta", "gamma"}, -1)
	if n := hlCount(got); n != 3 {
		t.Errorf("spans = %d, want 3\n%q", n, got)
	}
	if plain := stripANSI(got); plain != "alpha beta gamma" {
		t.Errorf("visible text changed: %q", plain)
	}
}

func TestHighlightSearchTermsEmptyInputs(t *testing.T) {
	if got := highlightSearchTerms("text", nil, -1); got != "text" {
		t.Errorf("nil terms changed content: %q", got)
	}
	if got := highlightSearchTerms("text", []string{""}, -1); got != "text" {
		t.Errorf("empty term changed content: %q", got)
	}
}

// Structured filter tokens select whole blocks; highlighting them would paint
// the literal string "is:tool" wherever it appears in the text.
func TestHighlightableTermsSkipsStructuralTokens(t *testing.T) {
	cases := []struct {
		filter string
		want   []string
	}{
		{"worktree", []string{"worktree"}},
		{"is:tool worktree", []string{"worktree"}},
		{"tool:Bash prune", []string{"prune"}},
		{"is:error", nil},
		{"tool:mcp*", nil},
		{"!skipme worktree", []string{"worktree"}},
		{"alpha beta", []string{"alpha", "beta"}},
		{"", nil},
		{"   ", nil},
	}
	for _, c := range cases {
		got := highlightableTerms(c.filter)
		if len(got) != len(c.want) {
			t.Errorf("highlightableTerms(%q) = %v, want %v", c.filter, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("highlightableTerms(%q) = %v, want %v", c.filter, got, c.want)
				break
			}
		}
	}
}

// A negated term marks what must be absent — there is nothing to paint, and
// painting it would be actively misleading.
func TestNegatedTermIsNotHighlighted(t *testing.T) {
	terms := highlightableTerms("!worktree prune")
	for _, term := range terms {
		if strings.Contains(term, "worktree") {
			t.Errorf("negated term %q would be highlighted", term)
		}
	}
}

// Opening a conversation from anywhere other than a search result must not
// inherit a previous jump's highlight. Seven of the eight openConversation
// callers are not search jumps, so the clear has to live in the callee.
func TestOpenConversationClearsSearchHighlight(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	line := `{"type":"user","uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"hello there"}]}}`
	if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &App{}
	a.conv.split.Folds = &FoldState{}
	a.convHighlightTerms = []string{"stale"}
	a.conv.split.Folds.ExtraHighlight = []string{"stale"}

	a.openConversation(session.Session{FilePath: path})

	if len(a.convHighlightTerms) != 0 {
		t.Errorf("convHighlightTerms = %v, want cleared", a.convHighlightTerms)
	}
	if len(a.conv.split.Folds.ExtraHighlight) != 0 {
		t.Errorf("Folds.ExtraHighlight = %v, want cleared", a.conv.split.Folds.ExtraHighlight)
	}
}

func TestSearchModeStringsAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range []session.SearchMode{
		session.SearchModeScan,
		session.SearchModeIndex,
		session.SearchModeIndexPartial,
	} {
		s := m.String()
		if s == "" {
			t.Error("empty mode string")
		}
		if seen[s] {
			t.Errorf("duplicate mode string %q", s)
		}
		seen[s] = true
	}
}

// End-to-end through the real App: picking a cross-session search result must
// leave the term highlighted in the preview the jump lands on. Unit tests cover
// the paint function; this covers the wiring between search, openConversation,
// and the preview renderer.
func TestSearchJumpHighlightsPreviewEndToEnd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	lines := []string{
		`{"type":"user","uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"please prune the worktree now"}]}}`,
		`{"type":"assistant","uuid":"a1","message":{"role":"assistant","content":[{"type":"text","text":"done, worktree removed"}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	sess := session.Session{
		ID: "sid1", ShortID: "sid1",
		ProjectPath: dir, ProjectName: "proj",
		FilePath: path, ModTime: fi.ModTime(), MsgCount: 2,
	}

	a := newTestApp([]session.Session{sess})
	a.searchQuery = "worktree"

	entries, err := session.LoadMessages(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no entries loaded")
	}
	result := session.SearchResult{
		Session: &sess,
		Entry:   &entries[0],
		Block:   &entries[0].Content[0],
	}

	a.openSearchResult(result)

	if len(a.convHighlightTerms) == 0 {
		t.Fatal("jump did not carry the query terms into the conversation")
	}
	if a.convHighlightTerms[0] != "worktree" {
		t.Errorf("carried terms = %v, want [worktree]", a.convHighlightTerms)
	}

	// The preview content itself must carry the highlight escapes.
	a.conv.split.Show = true
	a.setConvPreviewText("the worktree line in preview")
	got := a.conv.split.Preview.View()
	if hlCount(got) == 0 {
		t.Errorf("preview has no highlight for the searched term:\n%q", stripANSI(got))
	}

	// Opening an unrelated session afterwards must not keep painting.
	a.openConversation(sess)
	if len(a.convHighlightTerms) != 0 {
		t.Errorf("highlight leaked into a later conversation: %v", a.convHighlightTerms)
	}
}
