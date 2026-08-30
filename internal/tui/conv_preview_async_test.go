package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
)

// writeNavTranscript writes a transcript with n user/assistant messages.
func writeNavTranscript(t *testing.T, dir, name string, n int) session.Session {
	t.Helper()
	path := filepath.Join(dir, name)
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(`{"type":"user","uuid":"u`)
		b.WriteString(strings.Repeat("x", 3))
		b.WriteString(`","message":{"role":"user","content":[{"type":"text","text":"message body here"}]}}` + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return session.Session{
		ID: name, ShortID: name,
		ProjectPath: dir, ProjectName: "proj",
		FilePath: path, ModTime: fi.ModTime(), MsgCount: n,
	}
}

func navTestApp(t *testing.T, sessions []session.Session) *App {
	t.Helper()
	a := newTestApp(sessions)
	a.sessSplit.Show = true
	a.sessPreviewMode = sessPreviewConversation
	a.rebuildSessionList()
	if len(a.sessionList.VisibleItems()) == 0 {
		t.Fatal("session list is empty")
	}
	return a
}

// The transcript read must not run inside Update. It used to, and a large
// session blocked the event loop for ~465ms, freezing navigation. The read is
// only allowed to happen inside the returned command.
func TestConvPreviewReadHappensOffTheUpdateLoop(t *testing.T) {
	dir := t.TempDir()
	sessions := []session.Session{
		writeNavTranscript(t, dir, "a.jsonl", 20),
		writeNavTranscript(t, dir, "b.jsonl", 20),
	}
	a := navTestApp(t, sessions)

	// Force a fresh load for the selected session.
	a.sessSplit.CacheKey = ""
	a.sessConvCacheID = ""
	a.sessConvEntries = nil

	cmd := a.updateSessionPreview()
	if cmd == nil {
		t.Fatal("conversation preview returned no command — the read would be inline")
	}
	// Nothing may be parsed yet: Update returned without touching the file.
	if len(a.sessConvEntries) != 0 {
		t.Errorf("entries populated during Update (%d) — read did not move off the loop", len(a.sessConvEntries))
	}

	msg := cmd()
	loaded, ok := msg.(convPreviewLoadedMsg)
	if !ok {
		t.Fatalf("command returned %T, want convPreviewLoadedMsg", msg)
	}
	if loaded.total == 0 {
		t.Fatal("loader read no messages")
	}

	a.applyConvPreviewLoad(loaded)
	if len(a.sessConvEntries) == 0 {
		t.Error("applying the load produced no entries")
	}
}

// A result that arrives after the cursor moved on must not overwrite the pane
// the user is now looking at.
func TestStaleConvPreviewLoadIsDiscarded(t *testing.T) {
	dir := t.TempDir()
	sessions := []session.Session{
		writeNavTranscript(t, dir, "a.jsonl", 20),
		writeNavTranscript(t, dir, "b.jsonl", 20),
	}
	a := navTestApp(t, sessions)

	sel, ok := a.selectedSession()
	if !ok {
		t.Fatal("no session selected")
	}

	a.applyConvPreviewLoad(convPreviewLoadedMsg{
		sessID: "some-other-session-id",
		total:  5,
		head:   []session.Entry{{Role: "user", Content: []session.ContentBlock{{Type: "text", Text: "stale"}}}},
	})

	if a.sessConvCacheID == "some-other-session-id" {
		t.Error("a stale load overwrote the current preview")
	}
	if a.sessConvCacheID != "" && a.sessConvCacheID != sel.ID {
		t.Errorf("cache id = %q, want empty or %q", a.sessConvCacheID, sel.ID)
	}
}

// Two loads for the same session must not be dispatched concurrently.
func TestConvPreviewLoadIsNotDispatchedTwice(t *testing.T) {
	dir := t.TempDir()
	sessions := []session.Session{writeNavTranscript(t, dir, "a.jsonl", 20)}
	a := navTestApp(t, sessions)

	sel, _ := a.selectedSession()
	a.sessSplit.CacheKey = ""
	a.sessConvCacheID = ""
	if cmd := a.updateSessionPreview(); cmd == nil {
		t.Fatal("first dispatch returned no command")
	}
	if !a.convPreviewInFlight[sel.ID] {
		t.Fatal("in-flight latch not set")
	}

	a.sessSplit.CacheKey = "" // force the guard to be re-evaluated
	if cmd := a.updateSessionPreview(); cmd != nil {
		t.Error("second dispatch while one is in flight returned a command")
	}
}

// View() cannot dispatch commands, so any preview whose update returns one must
// be excluded from the View-driven path — otherwise the pane sticks on
// "(loading…)" forever.
func TestConversationIsExcludedFromViewDrivenPreview(t *testing.T) {
	if !previewDispatchesFromView(sessPreviewConversation) {
		t.Error("conversation preview loads async but View would still drive it")
	}
	// The narrower predicate must stay narrow: project rows rely on it to fall
	// through to the project summary rather than a representative session.
	if previewDispatchesCmd(sessPreviewConversation) {
		t.Error("previewDispatchesCmd must not include conversation — it reroutes project rows")
	}
	for _, m := range []sessPreview{sessPreviewLive, sessPreviewRefs, sessPreviewOutputs} {
		if !previewDispatchesFromView(m) {
			t.Errorf("mode %d dispatches a cmd but is not excluded from View", m)
		}
	}
}

// Navigating with the preview open must never block the loop, whatever the
// transcript size.
func TestNavigationDoesNotBlockOnLargeTranscript(t *testing.T) {
	dir := t.TempDir()
	sessions := []session.Session{
		writeNavTranscript(t, dir, "small.jsonl", 10),
		writeNavTranscript(t, dir, "big.jsonl", 20000),
	}
	a := navTestApp(t, sessions)

	down := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
	for i := 0; i < 4; i++ {
		m, _ := a.Update(down)
		a = m.(*App)
		m2, cmd := a.Update(previewDebounceMsg{id: a.previewDebounceID})
		a = m2.(*App)
		// Whatever the debounce produced, Update itself must not have parsed the
		// transcript; that is the loader's job, off the loop.
		if cmd != nil {
			if msg, ok := cmd().(convPreviewLoadedMsg); ok {
				m3, _ := a.Update(msg)
				a = m3.(*App)
			}
		}
	}
}

// The stats preview walks the whole transcript (790ms on a 180 MB session,
// measured), so it must dispatch rather than scan inside Update.
func TestStatsPreviewScanHappensOffTheUpdateLoop(t *testing.T) {
	dir := t.TempDir()
	sessions := []session.Session{writeNavTranscript(t, dir, "a.jsonl", 50)}
	a := navTestApp(t, sessions)
	a.sessPreviewMode = sessPreviewStats
	a.sessSplit.CacheKey = ""
	a.sessStatsCache = nil
	a.sessStatsCacheKey = ""

	cmd := a.updateSessionPreview()
	if cmd == nil {
		t.Fatal("stats preview returned no command — the scan would be inline")
	}
	if a.sessStatsCache != nil {
		t.Error("stats populated during Update — scan did not move off the loop")
	}

	msg, ok := cmd().(statsPreviewLoadedMsg)
	if !ok {
		t.Fatalf("command returned %T, want statsPreviewLoadedMsg", msg)
	}
	a.applyStatsPreviewLoad(msg)
	if a.sessStatsCache == nil {
		t.Error("applying the scan produced no stats")
	}

	// A cached session must render immediately, with no second dispatch.
	a.sessSplit.CacheKey = ""
	if cmd := a.updateSessionPreview(); cmd != nil {
		t.Error("cached stats dispatched a redundant scan")
	}
}

func TestStaleStatsLoadIsDiscarded(t *testing.T) {
	dir := t.TempDir()
	sessions := []session.Session{writeNavTranscript(t, dir, "a.jsonl", 10)}
	a := navTestApp(t, sessions)

	a.applyStatsPreviewLoad(statsPreviewLoadedMsg{sessID: "not-the-selected-one"})
	if a.sessStatsCacheKey == "not-the-selected-one" {
		t.Error("a stale stats result overwrote the current preview")
	}
}

// The live-session tick re-reads the transcript on every tick; that read must
// not run inside Update either.
func TestLiveConvReloadHappensOffTheUpdateLoop(t *testing.T) {
	dir := t.TempDir()
	sess := writeNavTranscript(t, dir, "live.jsonl", 30)
	sess.IsLive = true
	a := navTestApp(t, []session.Session{sess})
	a.sessPreviewMode = sessPreviewConversation

	cmd := a.refreshSessionPreviewLive()
	if cmd == nil {
		t.Fatal("live refresh returned no command — the re-read would be inline")
	}
	sel, _ := a.selectedSession()
	if !a.convLiveInFlight[sel.ID] {
		t.Error("live in-flight latch not set")
	}
	// A second refresh while one is in flight must not queue another read.
	if cmd2 := a.refreshSessionPreviewLive(); cmd2 != nil {
		t.Error("live refresh dispatched while one was already in flight")
	}
}
