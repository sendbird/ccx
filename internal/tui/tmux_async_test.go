package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/sendbird/ccx/internal/session"
	"github.com/sendbird/ccx/internal/tmux"
)

// stubPaneIO replaces switch-client and send-keys and counts calls.
func stubPaneIO(t *testing.T, sendErr error) (switches, sends *int) {
	t.Helper()
	switches, sends = new(int), new(int)
	origSwitch, origSend := switchToPane, sendKeys
	t.Cleanup(func() { switchToPane, sendKeys = origSwitch, origSend })
	switchToPane = func(tmux.Pane) error { *switches++; return nil }
	sendKeys = func(tmux.Pane, string) error { *sends++; return sendErr }
	return switches, sends
}

func TestJumpToTmuxPaneLooksUpOffUpdateAndDropsStale(t *testing.T) {
	finds, _ := stubTmux(t, true, nil)
	switches, _ := stubPaneIO(t, nil)
	a := newTestApp(fakeSessions())

	_, first := a.jumpToTmuxPane("/p", "id1")
	_, second := a.jumpToTmuxPane("/p", "id2")
	if *finds != 0 {
		t.Fatalf("FindPane ran on Update path %d time(s)", *finds)
	}
	if _, cmd := a.Update(first()); cmd != nil {
		t.Fatal("stale lookup must not switch")
	}
	_, cmd := a.Update(second())
	if cmd == nil {
		t.Fatal("latest lookup should return a switch command")
	}
	if *switches != 0 {
		t.Fatal("switch-client ran on Update path")
	}
	a.Update(cmd())
	if *switches != 1 {
		t.Fatalf("switches = %d, want 1", *switches)
	}
}

func TestJumpToTmuxPaneNotFound(t *testing.T) {
	stubTmux(t, false, nil)
	a := newTestApp(fakeSessions())
	_, cmd := a.jumpToTmuxPane("/p", "id1")
	a.Update(cmd())
	if a.flashText() != "No tmux pane found" {
		t.Fatalf("status toast = %q", a.flashText())
	}
}

func TestOpenLivePreviewLooksUpOffUpdateAndChecksCursor(t *testing.T) {
	finds, _ := stubTmux(t, true, nil)
	a := newTestApp(fakeSessions())
	sess, ok := a.selectedSession()
	if !ok || !sess.IsLive {
		t.Fatalf("expected a live selected session, got %+v", sess)
	}

	_, cmd := a.openLivePreview(sess)
	if *finds != 0 {
		t.Fatal("FindPane ran on Update path")
	}
	msg := cmd()
	if a.paneProxy != nil || a.sessPreviewMode == sessPreviewLive {
		t.Fatal("preview opened before the lookup returned")
	}

	// Cursor moved while the lookup ran: result is for another session.
	stale := msg.(livePreviewPaneMsg)
	stale.sessID = "someone-else"
	a.Update(stale)
	if a.paneProxy != nil {
		t.Fatal("result for a session no longer selected opened the preview")
	}

	a.Update(msg)
	if a.paneProxy == nil || a.sessPreviewMode != sessPreviewLive {
		t.Fatalf("live preview not opened: proxy=%v mode=%v", a.paneProxy, a.sessPreviewMode)
	}
}

func TestBulkInputLooksUpOffUpdate(t *testing.T) {
	t.Setenv("TMUX", "/tmp/fake,1,0")
	finds, _ := stubTmux(t, true, nil)
	a := newTestApp(fakeSessions())
	live := []session.Session{a.sessions[0], a.sessions[2]}

	_, cmd := a.bulkInput(live)
	if *finds != 0 {
		t.Fatal("FindPane ran on Update path")
	}
	a.Update(cmd())
	if !a.liveInputActive || len(a.liveInputPanes) != 2 {
		t.Fatalf("modal not opened for 2 panes: active=%v panes=%d", a.liveInputActive, len(a.liveInputPanes))
	}
}

func TestShareRefSendReportsSendError(t *testing.T) {
	stubTmux(t, true, nil)
	stubPaneIO(t, errors.New("can't find pane"))
	a := newTestApp(fakeSessions())
	a.Update(shareRefSendCmd(a.sessions[0], "prompt", "Shared x")())
	if !strings.Contains(a.flashText(), "Share failed") || !strings.Contains(a.flashText(), "can't find pane") {
		t.Fatalf("status toast = %q", a.flashText())
	}

	stubPaneIO(t, nil)
	a.Update(shareRefSendCmd(a.sessions[0], "prompt", "Shared x")())
	if a.flashText() != "Shared x" {
		t.Fatalf("status toast = %q, want %q", a.flashText(), "Shared x")
	}
}

func TestSendToPanesReportsEveryFailure(t *testing.T) {
	_, sends := stubPaneIO(t, errors.New("boom"))
	err := sendToPanes([]tmux.Pane{{Session: "a"}, {Session: "b"}}, "hi")
	if *sends != 2 {
		t.Fatalf("sends = %d, want 2", *sends)
	}
	if err == nil || strings.Count(err.Error(), "boom") != 2 {
		t.Fatalf("err = %v, want both failures", err)
	}
}

// The `\` continuation used to drop send-keys errors.
func TestLiveCaptureSurfacesSendError(t *testing.T) {
	a := newTestApp(fakeSessions())
	a.Update(liveCaptureMsg{content: "x", sendErr: errors.New("no server")})
	if !strings.Contains(a.flashText(), "no server") {
		t.Fatalf("status toast = %q", a.flashText())
	}
}
