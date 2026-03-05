package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
)

func newTestApp(sessions []session.Session) *App {
	app := NewApp(sessions, Config{TmuxEnabled: true})
	m, _ := app.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	return m.(*App)
}

func fakeSessions() []session.Session {
	return []session.Session{
		{ID: "aaa", ShortID: "aaa", ProjectPath: "/tmp/proj-a", ProjectName: "proj-a", ModTime: time.Now(), MsgCount: 10, IsLive: true},
		{ID: "bbb", ShortID: "bbb", ProjectPath: "/tmp/proj-b", ProjectName: "proj-b", ModTime: time.Now().Add(-time.Hour), MsgCount: 5},
		{ID: "ccc", ShortID: "ccc", ProjectPath: "/tmp/proj-c", ProjectName: "proj-c", ModTime: time.Now().Add(-2 * time.Hour), MsgCount: 3, IsLive: true},
	}
}

// TestCyclePreviewModeSkipsLive verifies tab cycling skips sessPreviewLive.
func TestCyclePreviewModeSkipsLive(t *testing.T) {
	app := newTestApp(fakeSessions())

	// Start at conversation mode
	app.sessPreviewMode = sessPreviewConversation

	// Cycle forward through all modes, collecting visited modes
	visited := map[sessPreview]bool{}
	for range numSessPreviewModes + 2 { // extra iterations to confirm full cycle
		app.cycleSessionPreviewMode()
		visited[app.sessPreviewMode] = true
	}

	if visited[sessPreviewLive] {
		t.Error("cycleSessionPreviewMode should skip sessPreviewLive")
	}

	// Verify we visit all other modes
	for _, mode := range []sessPreview{sessPreviewConversation, sessPreviewStats, sessPreviewMemory, sessPreviewTasksPlan} {
		if !visited[mode] {
			t.Errorf("cycleSessionPreviewMode should visit mode %d", mode)
		}
	}
}

// TestCyclePreviewModeReverseSkipsLive verifies reverse tab cycling skips sessPreviewLive.
func TestCyclePreviewModeReverseSkipsLive(t *testing.T) {
	app := newTestApp(fakeSessions())

	app.sessPreviewMode = sessPreviewConversation

	visited := map[sessPreview]bool{}
	for range numSessPreviewModes + 2 {
		app.cycleSessionPreviewModeReverse()
		visited[app.sessPreviewMode] = true
	}

	if visited[sessPreviewLive] {
		t.Error("cycleSessionPreviewModeReverse should skip sessPreviewLive")
	}
}

// TestCycleClearsLivePreviewState verifies that cycling away from live mode clears state.
func TestCycleClearsLivePreviewState(t *testing.T) {
	app := newTestApp(fakeSessions())

	// Simulate being in live preview
	app.sessPreviewMode = sessPreviewTasksPlan // one before live
	app.livePreviewSessID = "aaa"

	app.cycleSessionPreviewMode()

	if app.livePreviewSessID != "" {
		t.Errorf("cycleSessionPreviewMode should clear livePreviewSessID, got %q", app.livePreviewSessID)
	}
	if app.sessPreviewMode == sessPreviewLive {
		t.Error("should not land on sessPreviewLive")
	}
}

// TestTogglePreviewModeLive verifies L key toggles live preview mode.
func TestTogglePreviewModeLive(t *testing.T) {
	app := newTestApp(fakeSessions())

	// Open preview first
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyTab})
	app = m.(*App)

	if !app.sessSplit.Show {
		t.Fatal("preview should be open after Tab")
	}

	// Toggle to live mode
	app.toggleSessionPreviewMode(sessPreviewLive)
	if app.sessPreviewMode != sessPreviewLive {
		t.Errorf("expected sessPreviewLive, got %d", app.sessPreviewMode)
	}

	// Toggle again should return to conversation
	app.toggleSessionPreviewMode(sessPreviewLive)
	if app.sessPreviewMode != sessPreviewConversation {
		t.Errorf("expected sessPreviewConversation, got %d", app.sessPreviewMode)
	}
}

// TestTogglePreviewModeOpensIfClosed verifies toggle opens preview if closed.
func TestTogglePreviewModeOpensIfClosed(t *testing.T) {
	app := newTestApp(fakeSessions())

	app.sessSplit.Show = false
	app.toggleSessionPreviewMode(sessPreviewLive)

	if !app.sessSplit.Show {
		t.Error("toggleSessionPreviewMode should open preview if closed")
	}
	if app.sessPreviewMode != sessPreviewLive {
		t.Errorf("expected sessPreviewLive, got %d", app.sessPreviewMode)
	}
}

// TestOpenLivePreviewNonLiveSession verifies openLivePreview rejects non-live sessions.
func TestOpenLivePreviewNonLiveSession(t *testing.T) {
	app := newTestApp(fakeSessions())

	nonLive := session.Session{ID: "dead", ShortID: "dead", ProjectPath: "/tmp/dead", IsLive: false}
	_, cmd := app.openLivePreview(nonLive)

	if cmd != nil {
		t.Error("openLivePreview on non-live session should return nil cmd")
	}
	if app.copiedMsg != "not a live session" {
		t.Errorf("expected 'not a live session' message, got %q", app.copiedMsg)
	}
	if app.sessPreviewMode == sessPreviewLive {
		t.Error("should not switch to live mode for non-live session")
	}
}

// TestLivePreviewNonLiveSessionShowsMessage verifies that when in live mode,
// navigating to a non-live session shows a message instead of switching modes.
func TestLivePreviewNonLiveSessionShowsMessage(t *testing.T) {
	app := newTestApp(fakeSessions())

	// Open preview and set live mode
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyTab})
	app = m.(*App)
	app.sessPreviewMode = sessPreviewLive
	app.livePreviewSessID = "aaa"

	// Select a non-live session (index 1 = "bbb")
	app.sessionList.Select(1)
	app.sessSplit.CacheKey = "" // force refresh
	app.updateSessionPreview()

	if app.sessPreviewMode != sessPreviewLive {
		t.Errorf("should stay in sessPreviewLive, got %d", app.sessPreviewMode)
	}
	if app.livePreviewSessID != "" {
		t.Errorf("livePreviewSessID should be cleared, got %q", app.livePreviewSessID)
	}
}

// TestLiveTickDoesNotRefreshWhenNotInLiveMode verifies liveTickMsg is ignored
// when not in live preview mode.
func TestLiveTickDoesNotRefreshWhenNotInLiveMode(t *testing.T) {
	app := newTestApp(fakeSessions())

	app.sessPreviewMode = sessPreviewConversation
	app.livePreviewSessID = ""

	m, cmd := app.Update(liveTickMsg{})
	app = m.(*App)

	if cmd != nil {
		t.Error("liveTickMsg should not schedule another tick when not in live mode")
	}
}

// TestLiveTickSchedulesTick verifies liveTickMsg reschedules when in live mode.
func TestLiveTickSchedulesTick(t *testing.T) {
	app := newTestApp(fakeSessions())

	app.sessPreviewMode = sessPreviewLive
	app.livePreviewSessID = "aaa"
	// livePreviewPane is zero-value, so refreshLivePreview will fail silently,
	// but we can still verify the tick is rescheduled.

	m, cmd := app.Update(liveTickMsg{})
	app = m.(*App)

	if cmd == nil {
		t.Error("liveTickMsg should schedule next tick when in live preview mode")
	}
}

// TestPreviewModeConstants verifies the mode enum is consistent.
func TestPreviewModeConstants(t *testing.T) {
	modes := []sessPreview{
		sessPreviewConversation,
		sessPreviewStats,
		sessPreviewMemory,
		sessPreviewTasksPlan,
		sessPreviewLive,
	}

	if len(modes) != int(numSessPreviewModes) {
		t.Errorf("numSessPreviewModes=%d but there are %d modes", numSessPreviewModes, len(modes))
	}

	// Verify sequential iota values
	for i, m := range modes {
		if int(m) != i {
			t.Errorf("mode %d has value %d, expected %d", i, int(m), i)
		}
	}
}
