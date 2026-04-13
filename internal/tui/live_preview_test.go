package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
	"github.com/sendbird/ccx/internal/tmux"
)

func newTestApp(sessions []session.Session) *App {
	app := NewApp(sessions, Config{TmuxEnabled: true})
	m, _ := app.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	a := m.(*App)
	// Reset state to viewSessions — NewApp may restore a different view from
	// persisted preferences (~/.config/ccx/config.yaml), which would break
	// tests that assume the sessions view is active.
	a.state = viewSessions
	a.sessPreviewMode = sessPreviewConversation
	return a
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
	app.paneProxy = &paneProxyState{sessID: "aaa"}

	app.cycleSessionPreviewMode()

	if app.paneProxy != nil {
		t.Error("cycleSessionPreviewMode should clear paneProxy")
	}
	if app.sessPreviewMode == sessPreviewLive {
		t.Error("should not land on sessPreviewLive")
	}
}

// TestTogglePreviewModeLive verifies L key toggles live preview mode.
func TestTogglePreviewModeLive(t *testing.T) {
	app := newTestApp(fakeSessions())

	// Open preview via right arrow
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRight})
	app = m.(*App)

	if !app.sessSplit.Show {
		t.Fatal("preview should be open after Right")
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

	// Set up: preview showing live mode with pane proxy active
	app.sessSplit.Show = true
	app.sessPreviewMode = sessPreviewLive
	app.paneProxy = &paneProxyState{sessID: "aaa"}

	// Simulate navigating to session at index 1 (bbb = not live)
	// Use the bubbles list directly — Update with a cursor-down message
	var cmd tea.Cmd
	app.sessionList, cmd = app.sessionList.Update(tea.KeyMsg{Type: tea.KeyDown})
	_ = cmd

	// Verify we selected the right session
	sess, ok := app.selectedSession()
	if !ok {
		t.Skip("could not select session at index 1")
	}
	if sess.IsLive {
		t.Skip("session at index 1 is live, need non-live for this test")
	}

	// Force preview update
	app.sessSplit.CacheKey = ""
	app.updateSessionPreview()

	if app.sessPreviewMode != sessPreviewLive {
		t.Errorf("should stay in sessPreviewLive, got %d", app.sessPreviewMode)
	}
	if app.paneProxy != nil {
		t.Error("paneProxy should be cleared for non-live session")
	}
}

// TestLiveTickDoesNotRefreshWhenNotInLiveMode verifies liveTickMsg is ignored
// when not in live preview mode.
func TestLiveTickDoesNotRefreshWhenNotInLiveMode(t *testing.T) {
	app := newTestApp(fakeSessions())

	app.sessPreviewMode = sessPreviewConversation
	app.paneProxy = nil

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
	app.paneProxy = &paneProxyState{sessID: "aaa"}
	// paneProxy.pane is zero-value, so refreshLivePreview will fail silently,
	// but we can still verify the tick is rescheduled.

	m, cmd := app.Update(liveTickMsg{})
	app = m.(*App)

	if cmd == nil {
		t.Error("liveTickMsg should schedule next tick when in live preview mode")
	}
}

// TestTeaKeyToTmux verifies key mapping from Bubble Tea to tmux.
func TestTeaKeyToTmux(t *testing.T) {
	tests := []struct {
		input   string
		tmuxKey string
		literal bool
	}{
		{"enter", "Enter", false},
		{"backspace", "BSpace", false},
		{"tab", "Tab", false},
		{"up", "Up", false},
		{"ctrl+c", "C-c", false},
		{"a", "a", true},
		{"1", "1", true},
		{"space", "Space", false},
		{" ", "Space", false},
		{"unknown-key", "", false},
	}
	for _, tt := range tests {
		key, literal := tmux.TeaKeyToTmux(tt.input)
		if key != tt.tmuxKey || literal != tt.literal {
			t.Errorf("tmux.TeaKeyToTmux(%q) = (%q, %v), want (%q, %v)", tt.input, key, literal, tt.tmuxKey, tt.literal)
		}
	}
}

// TestPaneProxyIndicator verifies LIVE/SHELL badge rendering.
func TestPaneProxyIndicator(t *testing.T) {
	app := newTestApp(fakeSessions())

	// No proxy → empty
	if got := app.paneProxyIndicator(); got != "" {
		t.Errorf("expected empty indicator with no proxy, got %q", got)
	}

	// Live proxy, unfocused → contains LIVE and ○
	app.paneProxy = &paneProxyState{sessID: "aaa"}
	app.sessSplit.Focus = false
	got := app.paneProxyIndicator()
	if got == "" || !contains(got, "LIVE") || !contains(got, "○") {
		t.Errorf("unfocused live indicator should contain LIVE and ○, got %q", got)
	}

	// Live proxy, focused → contains LIVE and ●
	app.sessSplit.Focus = true
	got = app.paneProxyIndicator()
	if got == "" || !contains(got, "LIVE") || !contains(got, "●") {
		t.Errorf("focused live indicator should contain LIVE and ●, got %q", got)
	}

	// Shell proxy → contains SHELL
	app.paneProxy = &paneProxyState{isShell: true}
	got = app.paneProxyIndicator()
	if got == "" || !contains(got, "SHELL") {
		t.Errorf("shell indicator should contain SHELL, got %q", got)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// TestLiveTickCapturesBothFocusStates verifies liveTickMsg works for both focused and unfocused.
func TestLiveTickCapturesBothFocusStates(t *testing.T) {
	app := newTestApp(fakeSessions())
	app.sessPreviewMode = sessPreviewLive
	app.paneProxy = &paneProxyState{sessID: "aaa"}

	// Unfocused: should reschedule
	app.sessSplit.Focus = false
	_, cmd := app.Update(liveTickMsg{})
	if cmd == nil {
		t.Error("liveTickMsg should reschedule when proxy active and unfocused")
	}

	// Focused: should also reschedule (passive capture for process output)
	app.sessSplit.Focus = true
	_, cmd = app.Update(liveTickMsg{})
	if cmd == nil {
		t.Error("liveTickMsg should reschedule when proxy active and focused")
	}
}

// TestCtrlQFromFocusedPaneProxyUnfocuses verifies ctrl+q unfocuses pane proxy.
func TestCtrlQFromFocusedPaneProxyUnfocuses(t *testing.T) {
	app := newTestApp(fakeSessions())

	// Open preview via right arrow and set up pane proxy
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRight})
	app = m.(*App)
	app.sessPreviewMode = sessPreviewLive
	app.paneProxy = &paneProxyState{sessID: "aaa"}
	app.sessSplit.Focus = true

	// ctrl+q should unfocus, not close
	m, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlQ})
	app = m.(*App)

	if app.sessSplit.Focus {
		t.Error("ctrl+q should unfocus the pane proxy")
	}
	if app.paneProxy == nil {
		t.Error("pane proxy should still exist after unfocus")
	}
	if cmd == nil {
		t.Error("should return liveTickCmd after unfocus")
	}
}

// TestClosePaneProxyKillsShell verifies closePaneProxy clears state.
func TestClosePaneProxyKillsShell(t *testing.T) {
	app := newTestApp(fakeSessions())
	// Non-shell proxy: just nil out
	app.paneProxy = &paneProxyState{sessID: "aaa"}
	app.closePaneProxy()
	if app.paneProxy != nil {
		t.Error("closePaneProxy should nil out paneProxy")
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
		sessPreviewRemote,
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
