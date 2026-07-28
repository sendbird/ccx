package cli

import (
	"testing"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

func sess(id, projPath string, mod time.Time) session.Session {
	return session.Session{ID: id, ProjectPath: projPath, ModTime: mod}
}

func ids(sessions []session.Session) []string {
	out := make([]string, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, s.ID)
	}
	return out
}

func TestMatchSessionsForPathsExactPrefersMostRecent(t *testing.T) {
	base := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	sessions := []session.Session{
		sess("old", "/w/proj", base),
		sess("new", "/w/proj", base.Add(time.Hour)),
		sess("other", "/w/elsewhere", base.Add(2*time.Hour)),
	}

	got := matchSessionsForPaths(sessions, []string{"/w/proj"}, samePath)

	if len(got) != 1 || got[0].ID != "new" {
		t.Fatalf("matchSessionsForPaths = %v, want [new]", ids(got))
	}
}

// samePath must not match a subdirectory session — that's what relatedPath is
// for, and conflating them would silently pick the wrong project.
func TestMatchSessionsForPathsExactIgnoresSubdirectory(t *testing.T) {
	base := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	sessions := []session.Session{sess("sub", "/w/proj/module", base)}

	if got := matchSessionsForPaths(sessions, []string{"/w/proj"}, samePath); len(got) != 0 {
		t.Fatalf("matchSessionsForPaths = %v, want no match", ids(got))
	}
}

// The cwd of a worktree session with a nested module flips between the project
// and its subdirectory, so a pane in the module may find only a session filed
// at the parent. paneUnderSession recovers it — and must not match the reverse,
// or a pane in a broad parent like ~/src would adopt an arbitrary deep session.
func TestPaneUnderSessionMatchesOnlyUpward(t *testing.T) {
	base := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)

	parent := []session.Session{sess("parent", "/w/proj", base)}
	got := matchSessionsForPaths(parent, []string{"/w/proj/module"}, paneUnderSession)
	if len(got) != 1 || got[0].ID != "parent" {
		t.Fatalf("pane under session: got %v, want [parent]", ids(got))
	}

	child := []session.Session{sess("child", "/w/proj/module", base)}
	if got := matchSessionsForPaths(child, []string{"/w/proj"}, paneUnderSession); len(got) != 0 {
		t.Fatalf("session under pane: got %v, want no match", ids(got))
	}
}

// Nearness beats recency: a distant ancestor must not win over the nearest one
// just because it was touched later.
func TestMatchSessionsForPathsPrefersNearestAncestor(t *testing.T) {
	base := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	sessions := []session.Session{
		sess("grandparent", "/w", base.Add(3*time.Hour)),
		sess("parent", "/w/proj", base),
	}

	got := matchSessionsForPaths(sessions, []string{"/w/proj/module"}, paneUnderSession)

	if len(got) != 1 || got[0].ID != "parent" {
		t.Fatalf("matchSessionsForPaths = %v, want [parent]", ids(got))
	}
}

func TestMatchSessionsForPathsDedupesAcrossPanePaths(t *testing.T) {
	base := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	sessions := []session.Session{sess("shared", "/w/proj", base)}

	got := matchSessionsForPaths(sessions, []string{"/w/proj/module", "/w/proj/other"}, paneUnderSession)

	if len(got) != 1 || got[0].ID != "shared" {
		t.Fatalf("matchSessionsForPaths = %v, want [shared] once", ids(got))
	}
}

func TestMatchSessionsForPathsOnePerPanePath(t *testing.T) {
	base := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	sessions := []session.Session{
		sess("a", "/w/alpha", base),
		sess("b", "/w/beta", base),
	}

	got := matchSessionsForPaths(sessions, []string{"/w/alpha", "/w/beta"}, samePath)

	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("matchSessionsForPaths = %v, want [a b] in pane order", ids(got))
	}
}

// A sibling whose name merely shares a prefix is not a parent.
func TestPaneUnderSessionRequiresSegmentBoundary(t *testing.T) {
	if paneUnderSession("/w/proj", "/w/projX") {
		t.Fatal("paneUnderSession treated /w/projX as living under /w/proj")
	}
	if !paneUnderSession("/w/proj", "/w/proj/x") {
		t.Fatal("paneUnderSession missed a real child")
	}
}

func TestPathDistance(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"/w/proj", "/w/proj", 0},
		{"/w/proj/module", "/w/proj", 1},
		{"/w/proj", "/w/proj/module", 1},
		{"/w/proj/a/b", "/w/proj", 2},
	}
	for _, tt := range tests {
		if got := pathDistance(tt.a, tt.b); got != tt.want {
			t.Errorf("pathDistance(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
	if pathDistance("/w/alpha", "/w/beta") <= 2 {
		t.Error("pathDistance ranked unrelated paths as near")
	}
}
