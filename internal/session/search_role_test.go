package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeRoleTranscript writes a transcript whose lines alternate by role, each
// containing the marker so every entry is a hit.
func writeRoleTranscript(t *testing.T, dir, name string, roles []string, marker string) *Session {
	t.Helper()
	var lines []string
	for i, role := range roles {
		text := fmt.Sprintf("%s occurrence %d", marker, i)
		lines = append(lines, fmt.Sprintf(
			`{"type":%q,"uuid":"u%d","message":{"role":%q,"content":[{"type":"text","text":%q}]}}`,
			role, i, role, text))
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return &Session{ID: name, ShortID: name, FilePath: path, ModTime: fi.ModTime(), ProjectName: name}
}

func resultRoles(rs []SearchResult) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		if r.Entry == nil {
			out = append(out, "")
			continue
		}
		out = append(out, r.Entry.Role)
	}
	return out
}

// Within one session the user's own words come first: a hit in your prompt is
// what you wrote, and therefore what you remember searching for.
func TestUserHitsRankBeforeAssistantWithinSession(t *testing.T) {
	ix, dir := openTestIndex(t)
	s := writeRoleTranscript(t, dir, "a.jsonl",
		[]string{"assistant", "user", "assistant", "user"}, "marker")
	syncAll(t, ix, []*Session{s})

	res, _ := searchIdx(t, ix, []*Session{s}, "marker")
	if len(res) != 4 {
		t.Fatalf("hits = %d, want 4", len(res))
	}
	got := resultRoles(res)
	want := []string{"user", "user", "assistant", "assistant"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("role order = %v, want %v", got, want)
		}
	}
}

// User-first must NOT reorder sessions: recency is what makes the result list
// browsable, and hoisting every user hit across sessions would destroy it.
func TestUserPriorityDoesNotReorderSessions(t *testing.T) {
	ix, dir := openTestIndex(t)
	// Older session whose hits are all user; newer session whose hits are all
	// assistant. Recency must still win at the session level.
	older := writeRoleTranscript(t, dir, "older.jsonl", []string{"user", "user"}, "marker")
	newer := writeRoleTranscript(t, dir, "newer.jsonl", []string{"assistant", "assistant"}, "marker")

	past := time.Now().Add(-48 * time.Hour)
	os.Chtimes(older.FilePath, past, past)
	fi, _ := os.Stat(older.FilePath)
	older.ModTime = fi.ModTime()

	sessions := []*Session{older, newer}
	syncAll(t, ix, sessions)

	res, _ := searchIdx(t, ix, sessions, "marker")
	if len(res) != 4 {
		t.Fatalf("hits = %d, want 4", len(res))
	}
	// The newer (assistant-only) session must still come first.
	if res[0].Session.FilePath != newer.FilePath {
		t.Errorf("first hit is from %s, want the newer session",
			filepath.Base(res[0].Session.FilePath))
	}
	if res[len(res)-1].Session.FilePath != older.FilePath {
		t.Errorf("last hit is from %s, want the older session",
			filepath.Base(res[len(res)-1].Session.FilePath))
	}
}

// The limit must not be filled by assistant hits while user hits are dropped —
// which is why the ordering happens before truncation, not after.
func TestLimitKeepsUserHitsOverAssistant(t *testing.T) {
	ix, dir := openTestIndex(t)
	roles := make([]string, 0, 12)
	for i := 0; i < 10; i++ {
		roles = append(roles, "assistant")
	}
	roles = append(roles, "user", "user")
	s := writeRoleTranscript(t, dir, "a.jsonl", roles, "marker")
	syncAll(t, ix, []*Session{s})

	res, _, err := SearchWithIndex(context.Background(), ix, []*Session{s}, ParseSearchQuery("marker"), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("hits = %d, want 2", len(res))
	}
	for i, r := range res {
		if r.Entry == nil || r.Entry.Role != "user" {
			t.Errorf("result %d role = %v, want user (assistant hits filled the limit)",
				i, resultRoles(res))
			break
		}
	}
}

// The scan fallback must order the same way, or results reshuffle depending on
// whether the index could answer the query.
func TestScanFallbackUsesSameRoleOrdering(t *testing.T) {
	dir := t.TempDir()
	s := writeRoleTranscript(t, dir, "a.jsonl",
		[]string{"assistant", "user", "assistant", "user"}, "marker")

	scan := collectScan(context.Background(), []*Session{s}, ParseSearchQuery("marker"), 0)
	if len(scan) != 4 {
		t.Fatalf("scan hits = %d, want 4", len(scan))
	}
	got := resultRoles(scan)
	want := []string{"user", "user", "assistant", "assistant"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("scan role order = %v, want %v", got, want)
		}
	}
}

func TestRoleRankOrdersUserFirst(t *testing.T) {
	if roleRank("user") >= roleRank("assistant") {
		t.Error("user must rank before assistant")
	}
	if roleRank("assistant") >= roleRank("") {
		t.Error("a roleless entry must sort after both, not among them")
	}
}

// Ranking must never starve a recent session. An earlier attempt ordered by
// role in SQL so the LIMIT would keep user hits; that ranking is global, so an
// old session with many user hits filled the cap and the newest session
// vanished from the results entirely. This pins the failure that caught it.
func TestLimitDoesNotStarveRecentSessions(t *testing.T) {
	ix, dir := openTestIndex(t)
	oldRoles := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		oldRoles = append(oldRoles, "user")
	}
	older := writeRoleTranscript(t, dir, "older.jsonl", oldRoles, "marker")
	// The recent session's user hit sits DEEP in its file, so a SQL order of
	// (role, line_off) would rank it behind the old session's shallow hits.
	newRoles := make([]string, 0, 40)
	for i := 0; i < 39; i++ {
		newRoles = append(newRoles, "assistant")
	}
	newRoles = append(newRoles, "user")
	newer := writeRoleTranscript(t, dir, "newer.jsonl", newRoles, "marker")

	past := time.Now().Add(-72 * time.Hour)
	os.Chtimes(older.FilePath, past, past)
	fi, _ := os.Stat(older.FilePath)
	older.ModTime = fi.ModTime()

	sessions := []*Session{older, newer}
	syncAll(t, ix, sessions)

	res, _, err := SearchWithIndex(context.Background(), ix, sessions, ParseSearchQuery("marker"), 5)
	if err != nil {
		t.Fatal(err)
	}
	var sawNewer bool
	for _, r := range res {
		if r.Session.FilePath == newer.FilePath {
			sawNewer = true
		}
	}
	t.Logf("results=%d sawNewer=%v", len(res), sawNewer)
	if !sawNewer {
		t.Error("the most recent session is absent from a limited result set")
	}
}
