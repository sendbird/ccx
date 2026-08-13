package tui

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/list"
	"github.com/sendbird/ccx/internal/session"
)

// renderRow renders one list item through the session delegate and returns its
// two lines with styling stripped, so assertions read the glyphs the user sees.
func renderRow(t *testing.T, item list.Item, width int) (line1, line2 string) {
	t.Helper()
	l := list.New([]list.Item{item},
		sessionDelegate{timeW: 6, msgW: 3, rowCache: newSessionRowCache(8)}, width, 10)
	var sb strings.Builder
	d := sessionDelegate{timeW: 6, msgW: 3, rowCache: newSessionRowCache(8)}
	d.Render(&sb, l, 0, item)
	lines := strings.SplitN(stripANSI(sb.String()), "\n", 2)
	if len(lines) < 2 {
		return lines[0], ""
	}
	return lines[0], lines[1]
}

// A project row is the LAST child of its day: it must close the run with └─,
// not repeat ├─. renderProject had no way to express "last" at all.
func TestProjectRowRendersLastChildConnector(t *testing.T) {
	base := projectItem{
		basePath: "/tmp/repo-a", displayName: "repo-a", dayKey: "2026-08-13", treeDepth: 1,
		sessions: []session.Session{{ID: "a1", ModTime: time.Now(), MsgCount: 3}},
		bestTime: time.Now(),
	}

	mid := base
	mid.treeLast = false
	line1, _ := renderRow(t, mid, 120)
	if !strings.Contains(line1, "├─") {
		t.Fatalf("a non-last project row must render ├─, got %q", line1)
	}

	last := base
	last.treeLast = true
	line1, _ = renderRow(t, last, 120)
	if !strings.Contains(line1, "└─") {
		t.Fatalf("the last project row under a day must render └─, got %q", line1)
	}
	if strings.Contains(line1, "├─") {
		t.Fatalf("the last project row must not also render ├─, got %q", line1)
	}
}

// The row cache is keyed by everything that changes a row's pixels. treeLast
// flips a glyph, so leaving it out of the key serves a stale ├─ for a row that
// has become the last child.
func TestProjectCacheKeyVariesWithTreeLast(t *testing.T) {
	l := list.New(nil, sessionDelegate{}, 100, 10)
	d := sessionDelegate{rowCache: newSessionRowCache(8)}
	pi := projectItem{basePath: "/tmp/repo-a", displayName: "repo-a", dayKey: "d", treeDepth: 1}

	notLast := d.projectCacheKey(l, 0, pi, false)
	pi.treeLast = true
	isLast := d.projectCacheKey(l, 0, pi, false)

	if notLast == isLast {
		t.Fatalf("project cache key must change with treeLast, both were %q", notLast)
	}
}

// A depth-0 row (every non-daily top-level row) draws no connector at all.
func TestTopLevelRowsDrawNoConnector(t *testing.T) {
	line1, _ := renderRow(t, sessionItem{
		sess: session.Session{ID: "s1", ShortID: "s1", ProjectName: "repo", ModTime: time.Now(), MsgCount: 2},
	}, 120)
	if strings.Contains(line1, "├─") || strings.Contains(line1, "└─") {
		t.Fatalf("a top-level session row must not draw a tree connector, got %q", line1)
	}
}

// Rendering at a narrow width used to panic: prompt[:maxW-3] with maxW in 1..3
// evaluates to a negative bound ("slice bounds out of range [:-1]"), taking the
// whole render down with it.
func TestRenderSessionSurvivesNarrowWidth(t *testing.T) {
	si := sessionItem{sess: session.Session{
		ID: "s1", ShortID: "s1", ProjectName: "repo-a", ModTime: time.Now(), MsgCount: 4,
		FirstPrompt: "a first prompt long enough to need truncating",
	}}
	// Widths on both sides of the old panic threshold (maxW = width-11).
	for w := 1; w <= 20; w++ {
		renderRow(t, si, w) // panics before the fix
	}
}

// Project names and prompts were sliced by BYTE against a CELL budget, so a
// multibyte name was cut mid-rune and rendered as replacement characters.
func TestRenderSessionTruncatesMultibyteOnRuneBoundary(t *testing.T) {
	si := sessionItem{sess: session.Session{
		ID: "s1", ShortID: "s1", ModTime: time.Now(), MsgCount: 4,
		ProjectName: strings.Repeat("한글프로젝트", 6),
		FirstPrompt: strings.Repeat("한글 프롬프트 ", 12),
	}}
	for w := 12; w <= 90; w++ {
		line1, line2 := renderRow(t, si, w)
		for _, ln := range []string{line1, line2} {
			if strings.ContainsRune(ln, '�') {
				t.Fatalf("width %d: row cut mid-rune (U+FFFD present): %q", w, ln)
			}
			if !utf8.ValidString(ln) {
				t.Fatalf("width %d: row is not valid UTF-8: %q", w, ln)
			}
		}
	}
}
