package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
)

// TestRefsStartupRestoreDispatchesExtract reproduces the real stuck bug: the
// "refs" preview mode is PERSISTED across runs. On startup NewApp restores
// sessPreviewMode=refs + sessSplit.Show=true directly (NOT via setSessPreviewMode,
// the only path that dispatches the extract). Then the first WindowSizeMsg hits
// resizeAll's startup branch (sessionList.Width()==0), which only dispatches for
// LIVE mode — refs mode falls through, the extract is never dispatched, and the
// preview sticks on "Resolving…" forever.
func TestRefsStartupRestoreDispatchesExtract(t *testing.T) {
	sessions := []session.Session{
		{ID: "aaa", ShortID: "aaa", ProjectPath: "/tmp/proj-a", ProjectName: "proj-a",
			ModTime: time.Now(), HasRefs: true, FilePath: "/tmp/proj-a/aaa.jsonl",
			IsLive: true, IsCurrentWindow: true},
	}
	// Simulate persisted "refs" preview mode restored at startup.
	app := NewApp(sessions, Config{TmuxEnabled: true, PreviewMode: "refs"})
	if app.sessPreviewMode != sessPreviewRefs || !app.sessSplit.Show {
		t.Fatalf("precondition: expected restored refs mode + Show; got mode=%v show=%v",
			app.sessPreviewMode, app.sessSplit.Show)
	}

	// First window size — the startup resize path.
	m, cmd := app.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	a := m.(*App)

	// Drain the batched cmd looking for a refsExtractedMsg (the extract firing).
	if !dispatchesExtract(cmd) {
		t.Fatal("startup with restored refs mode never dispatched the extract — stuck on Resolving")
	}
	_ = a
}

// dispatchesExtract runs a (possibly batched) cmd and reports whether any
// resulting message is a refsExtractedMsg.
func dispatchesExtract(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	msg := cmd()
	return walkMsgForExtract(msg)
}

func walkMsgForExtract(msg tea.Msg) bool {
	switch v := msg.(type) {
	case refsExtractedMsg:
		return true
	case tea.BatchMsg:
		for _, c := range v {
			if c == nil {
				continue
			}
			if walkMsgForExtract(c()) {
				return true
			}
		}
	}
	return false
}

// TestRefsStartupRestoreProjectCentric is the same startup-restore repro but in
// the default projectCentric group mode, where the selected row is a projectItem.
func TestRefsStartupRestoreProjectCentric(t *testing.T) {
	sessions := []session.Session{
		{ID: "aaa", ShortID: "aaa", ProjectPath: "/tmp/proj-a", ProjectName: "proj-a",
			ModTime: time.Now(), HasRefs: true, FilePath: "/tmp/proj-a/aaa.jsonl",
			IsLive: true, IsCurrentWindow: true},
	}
	app := NewApp(sessions, Config{TmuxEnabled: true, PreviewMode: "refs"})
	// NewApp forces groupProjectCentric already, but be explicit.
	app.sessGroupMode = groupProjectCentric

	m, cmd := app.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	_ = m
	if !dispatchesExtract(cmd) {
		t.Fatal("projectCentric startup with restored refs mode never dispatched extract — stuck on Resolving")
	}
}
