package session

import (
	"bytes"
	"context"
	"encoding/gob"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The day pane's Enter jumps to the conversation entry where an output first
// appeared. That is only possible if a ref remembers WHICH entry it first
// appeared in — FirstSeen (a timestamp) cannot address a message. These tests
// guard that both extraction paths record the uuid, that dedup keeps the
// FIRST occurrence's uuid, and that the URL-keyed resolve cache does not
// substitute another session's uuid.

// TestExtractSessionRefsFromFileRecordsFirstSeenUUID guards the raw-line
// scanner: the fast path must pull the entry uuid off the line the same way it
// pulls the timestamp, or every ref the day pane shows has nothing to jump to.
func TestExtractSessionRefsFromFileRecordsFirstSeenUUID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fp := filepath.Join(t.TempDir(), "s.jsonl")
	lines := []string{
		`{"type":"assistant","uuid":"entry-first","timestamp":"2026-07-01T10:00:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"opened https://github.com/sendbird/ccx/pull/52"}]}}`,
		// The same PR again, later, with a different uuid: dedup must keep the
		// FIRST entry's uuid — where the work happened, not where it was quoted.
		`{"type":"assistant","uuid":"entry-later","timestamp":"2026-07-03T10:00:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"see https://github.com/sendbird/ccx/pull/52#discussion_r1"}]}}`,
		`{"type":"assistant","uuid":"entry-jira","timestamp":"2026-07-02T10:00:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"ticket https://sendbird.atlassian.net/browse/CPLAT-1234"}]}}`,
	}
	if err := os.WriteFile(fp, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	byLabel := map[string]SessionRef{}
	for _, r := range ExtractSessionRefsFromFile(fp) {
		byLabel[r.Label] = r
	}
	if got := byLabel["sendbird/ccx#52"].FirstSeenUUID; got != "entry-first" {
		t.Errorf("PR FirstSeenUUID = %q, want %q (the first occurrence's entry)", got, "entry-first")
	}
	if got := byLabel["CPLAT-1234"].FirstSeenUUID; got != "entry-jira" {
		t.Errorf("Jira FirstSeenUUID = %q, want %q", got, "entry-jira")
	}
}

// TestExtractSessionRefsRecordsFirstSeenUUID guards the []Entry path, which the
// conversation-side callers use.
func TestExtractSessionRefsRecordsFirstSeenUUID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t0 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	entries := []Entry{
		{UUID: "u-first", Timestamp: t0, Content: []ContentBlock{
			{Type: "text", Text: "opened https://github.com/sendbird/ccx/pull/52"},
		}},
		{UUID: "u-later", Timestamp: t0.Add(time.Hour), Content: []ContentBlock{
			{Type: "text", Text: "again https://github.com/sendbird/ccx/pull/52#discussion_r1"},
		}},
	}
	refs := ExtractSessionRefs(entries)
	if len(refs) != 1 {
		t.Fatalf("want 1 deduped ref, got %d: %+v", len(refs), refs)
	}
	if refs[0].FirstSeenUUID != "u-first" {
		t.Errorf("FirstSeenUUID = %q, want %q (dedup must keep the first occurrence)", refs[0].FirstSeenUUID, "u-first")
	}
}

// TestRefOutputCarriesFirstSeenUUID guards the hand-off: RefOutput is what the
// digests render, so a uuid recorded on the ref but dropped here leaves the
// jump target empty just the same.
func TestRefOutputCarriesFirstSeenUUID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	out := RefOutput(SessionRef{
		Kind: RefPR, Label: "sendbird/ccx#52",
		URL:           "https://github.com/sendbird/ccx/pull/52",
		FirstSeenUUID: "u-first",
	})
	if out.MessageUUID != "u-first" {
		t.Errorf("SessionOutput.MessageUUID = %q, want %q", out.MessageUUID, "u-first")
	}
}

// TestResolveRefKeepsThisOccurrencesUUID guards the staleness trap: the resolve
// cache is keyed by URL and shared process-wide, so the SAME PR referenced from
// two sessions hits one cache entry. FirstSeenUUID names an entry in a specific
// transcript — serving session B's uuid to session A sends the jump to an entry
// that does not exist there ("Entry not found in transcript").
func TestResolveRefKeepsThisOccurrencesUUID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ClearRefCache()
	t.Cleanup(ClearRefCache)

	url := "https://sendbird.atlassian.net/browse/CPLAT-9999"
	// Prime the cache as if session A resolved this ref first. Seeding directly
	// keeps the test hermetic — no gh/Jira network call.
	setCachedRef(SessionRef{
		Kind: RefJira, URL: url, Label: "CPLAT-9999",
		FirstSeen: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), FirstSeenUUID: "sessionA-entry",
		JiraStatus: "In Progress", State: RefStateOpen, Resolved: true,
	})

	// Session B sees the same PR at its own entry and asks for a resolve.
	got := ResolveRef(context.Background(), SessionRef{
		Kind: RefJira, URL: url, Label: "CPLAT-9999",
		FirstSeen: time.Date(2026, 7, 5, 0, 0, 0, 0, time.UTC), FirstSeenUUID: "sessionB-entry",
	})

	if got.FirstSeenUUID != "sessionB-entry" {
		t.Errorf("FirstSeenUUID = %q, want %q — the cache leaked another session's jump target",
			got.FirstSeenUUID, "sessionB-entry")
	}
	if got.JiraStatus != "In Progress" {
		t.Errorf("resolved status lost: JiraStatus = %q", got.JiraStatus)
	}
}

// TestLineUUIDReadsTopLevelEntryUUID pins the raw-line extractor's contract:
// the value it returns is the entry's own uuid, and a line without one yields
// "" rather than a neighbouring field's value.
func TestLineUUIDReadsTopLevelEntryUUID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := lineUUID([]byte(`{"parentUuid":"p1","type":"assistant","uuid":"e1","timestamp":"2026-07-01T10:00:00.000Z"}`)); got != "e1" {
		t.Errorf("lineUUID = %q, want %q", got, "e1")
	}
	if got := lineUUID([]byte(`{"type":"mode","mode":"normal","sessionId":"abc"}`)); got != "" {
		t.Errorf("lineUUID on a uuid-less line = %q, want empty", got)
	}
}

// TestSessionRefGobRoundTripsWithoutUUID guards the on-disk session cache: the
// scan cache is gob-encoded, so a user upgrading ccx decodes yesterday's cache
// into today's struct. A stream written before FirstSeenUUID existed must still
// decode — gob ignores absent fields, and this pins that it stays true (a
// cache that failed to decode would silently force a full rescan).
func TestSessionRefGobRoundTripsWithoutUUID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()

	// Write a cache the way the scanner does, with refs on the session.
	sc := &sessionCache{path: filepath.Join(dir, ".ccx-cache.gob"), entries: map[string]cachedSession{}}
	mod := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	sc.store("/tmp/p/a.jsonl", mod, Session{
		ID: "a", MsgCount: 3, ModTime: mod, HasRefs: true,
		Refs: []SessionRef{{
			Kind: RefPR, URL: "https://github.com/sendbird/ccx/pull/52", Label: "sendbird/ccx#52",
			FirstSeen: mod, FirstSeenUUID: "u-first",
		}},
	})
	sc.save()

	reloaded := loadCache(dir)
	cached, ok := reloaded.lookup("/tmp/p/a.jsonl", mod)
	if !ok {
		t.Fatal("cache entry did not survive a gob round-trip")
	}
	if len(cached.Refs) != 1 || cached.Refs[0].FirstSeenUUID != "u-first" {
		t.Errorf("FirstSeenUUID did not survive the gob round-trip: %+v", cached.Refs)
	}
}

// TestLegacySessionRefGobDecodes is the other half of the cache-compat story:
// a cache written by a ccx build that predates FirstSeenUUID. gob matches
// fields by name, so an absent field decodes as the zero value rather than an
// error — this pins that, because a decode error would blow away the whole
// cache map (loadCache returns an empty cache on any error) and cost every
// user a full rescan on upgrade.
func TestLegacySessionRefGobDecodes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	// legacySessionRef mirrors SessionRef as it was before FirstSeenUUID. gob
	// keys on field names, not on the struct's own name.
	type legacySessionRef struct {
		Kind      RefKind
		URL       string
		Label     string
		Title     string
		FirstSeen time.Time
		State     RefState
		Resolved  bool
	}
	mod := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode([]legacySessionRef{{
		Kind: RefPR, URL: "https://github.com/sendbird/ccx/pull/52",
		Label: "sendbird/ccx#52", FirstSeen: mod, State: RefStateOpen, Resolved: true,
	}}); err != nil {
		t.Fatal(err)
	}

	var got []SessionRef
	if err := gob.NewDecoder(&buf).Decode(&got); err != nil {
		t.Fatalf("a pre-FirstSeenUUID cache must still decode, got: %v", err)
	}
	if len(got) != 1 || got[0].Label != "sendbird/ccx#52" {
		t.Fatalf("decoded refs = %+v", got)
	}
	if got[0].FirstSeenUUID != "" {
		t.Errorf("FirstSeenUUID = %q, want empty for a legacy ref", got[0].FirstSeenUUID)
	}
}
