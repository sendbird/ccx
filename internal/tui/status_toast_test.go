package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/keyolk/tuikit/toast"
)

func TestFlashLevelFromText(t *testing.T) {
	for text, want := range map[string]toast.Level{
		"Copied path":                  toast.Info,
		"Send failed: no server":       toast.Error,
		"Error: boom":                  toast.Error,
		"Creating worktree…":           toast.Progress,
		"Focused: most recent session": toast.Info,
	} {
		if got := flashLevel(text); got != want {
			t.Errorf("flashLevel(%q) = %v, want %v", text, got, want)
		}
	}
}

// A status set deep in a handler gets its expiry tick from Update, and the
// tick clears it; a newer status replaces the shown one rather than stacking.
func TestFlashExpiresThroughUpdate(t *testing.T) {
	a := newTestApp(fakeSessions())
	clock := time.Unix(0, 0)
	a.toasts = toast.WithClock(a.toasts, func() time.Time { return clock })

	a.flash("first")
	a.flash("Copied 2 paths")
	if got := a.flashText(); got != "Copied 2 paths" {
		t.Fatalf("flashText = %q", got)
	}
	if n := a.toasts.Len(); n != 1 {
		t.Fatalf("status toasts stacked: %d", n)
	}
	id, _ := a.statusToast()
	exp, ok := a.toasts.ExpiredMsg(id.ID())
	if !ok {
		t.Fatal("info status has no expiry")
	}
	clock = clock.Add(toast.DefaultTTL + time.Second)
	a.Update(exp)
	if got := a.flashText(); got != "" {
		t.Fatalf("status survived its expiry: %q", got)
	}
}

// flashAppend on no status starts one; flash("") clears it.
func TestFlashAppendAndClear(t *testing.T) {
	a := newTestApp(fakeSessions())
	a.flash("")
	a.flashAppend("x")
	if a.flashText() != "x" {
		t.Fatalf("flashAppend on empty = %q", a.flashText())
	}
	a.flash("")
	if a.toasts.Len() != 0 {
		t.Fatal("flash(\"\") did not clear the status")
	}
}

// An error stays until a key acknowledges it, and the footer shows it with
// a level prefix that survives NO_COLOR.
func TestFlashErrorIsStickyAndRendered(t *testing.T) {
	a := newTestApp(fakeSessions())
	a.width = 160
	clock := time.Unix(0, 0)
	a.toasts = toast.WithClock(a.toasts, func() time.Time { return clock })
	a.flash("Kill failed: no server running")
	if got := stripANSI(a.View()); !strings.Contains(got, "✗ Kill failed") {
		t.Fatalf("footer lacks the error toast: %q", got[max(0, len(got)-200):])
	}
	clock = clock.Add(time.Minute)
	a.Update(tea.KeyMsg{Type: tea.KeyDown})
	if a.flashText() != "" {
		t.Fatalf("error not acknowledged by a key: %q", a.flashText())
	}
}
