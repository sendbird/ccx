package tui

import (
	"errors"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sendbird/ccx/internal/session"
	"github.com/sendbird/ccx/internal/tmux"
)

// Pane jump, live preview, bulk input and share-ref used to call
// tmux.FindPane (list every pane + walk the process table) inside Update.
// Each now resolves panes in a tea.Cmd; the result message carries the
// generation it was issued under and Update drops it when a newer request
// of the same kind has started, so a slow lookup never acts on a stale
// target. Same pattern as openLiveInput in pane_actions.go.
var (
	switchToPane = tmux.SwitchToPane
	sendKeys     = tmux.SendKeys
)

// paneJumpFoundMsg is the lookup result for jumpToTmuxPane/resumeSession.
type paneJumpFoundMsg struct {
	seq   int
	pane  tmux.Pane
	found bool
	// resume, when set, is the session to resume in this terminal if no
	// pane runs it (resumeSession's fallback).
	resume *session.Session
}

// paneJumpDoneMsg reports the switch-client outcome.
type paneJumpDoneMsg struct{ err error }

func (a *App) jumpToTmuxPane(projectPath string, sessionID ...string) (tea.Model, tea.Cmd) {
	return a, a.paneJumpCmd(nil, projectPath, sessionID...)
}

func (a *App) paneJumpCmd(resume *session.Session, projectPath string, sessionID ...string) tea.Cmd {
	a.paneJumpSeq++
	seq := a.paneJumpSeq
	return func() tea.Msg {
		pane, found := findPane(projectPath, sessionID...)
		return paneJumpFoundMsg{seq: seq, pane: pane, found: found, resume: resume}
	}
}

func (a *App) handlePaneJumpFound(msg paneJumpFoundMsg) tea.Cmd {
	if msg.seq != a.paneJumpSeq {
		return nil // superseded: switching now would land on an older target
	}
	if msg.found {
		pane := msg.pane
		return func() tea.Msg { return paneJumpDoneMsg{err: switchToPane(pane)} }
	}
	if msg.resume == nil {
		a.copiedMsg = "No tmux pane found"
		return nil
	}
	// Live session without a pane: take over this terminal.
	dir := msg.resume.ProjectPath
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}
	cmd, err := a.claudeCmd(dir, "--resume", msg.resume.ID)
	if err != nil {
		a.copiedMsg = "Claude command failed: " + err.Error()
		return nil
	}
	return tea.ExecProcess(cmd, func(error) tea.Msg { return tea.QuitMsg{} })
}

// livePreviewPaneMsg is the lookup result for openLivePreview.
type livePreviewPaneMsg struct {
	seq    int
	sessID string
	pane   tmux.Pane
	found  bool
}

func (a *App) openLivePreview(sess session.Session) (tea.Model, tea.Cmd) {
	if !sess.IsLive {
		a.copiedMsg = "not a live session"
		return a, nil
	}
	a.livePreviewSeq++
	seq := a.livePreviewSeq
	projectPath, id := sess.ProjectPath, sess.ID
	return a, func() tea.Msg {
		pane, found := findPane(projectPath, id)
		return livePreviewPaneMsg{seq: seq, sessID: id, pane: pane, found: found}
	}
}

func (a *App) handleLivePreviewPane(msg livePreviewPaneMsg) tea.Cmd {
	if msg.seq != a.livePreviewSeq {
		return nil
	}
	if sess, ok := a.selectedSession(); !ok || sess.ID != msg.sessID {
		return nil // cursor moved while looking up
	}
	if !msg.found {
		a.copiedMsg = "tmux pane not found"
		return nil
	}
	a.paneProxy = &paneProxyState{pane: msg.pane, sessID: msg.sessID}
	a.toggleSessionPreviewMode(sessPreviewLive)
	return tea.Batch(capturePaneCmd(msg.pane), liveTickCmd())
}

// bulkInputPanesMsg carries the live Claude panes for bulkInput.
type bulkInputPanesMsg struct {
	seq     int
	panes   []tmux.Pane
	projDir string
}

func (a *App) bulkInput(selected []session.Session) (tea.Model, tea.Cmd) {
	if !tmux.InTmux() {
		a.copiedMsg = "Requires tmux"
		return a, nil
	}
	type target struct{ path, id string }
	var targets []target
	for _, s := range selected {
		if s.IsLive {
			targets = append(targets, target{s.ProjectPath, s.ID})
		}
	}
	projDir := ""
	if len(selected) > 0 {
		projDir = selected[0].ProjectPath
	}
	a.liveInputSeq++
	seq := a.liveInputSeq
	return a, func() tea.Msg {
		var panes []tmux.Pane
		for _, t := range targets {
			pane, found := findPane(t.path, t.id)
			if found && paneHasClaude(pane.PID) {
				panes = append(panes, pane)
			}
		}
		return bulkInputPanesMsg{seq: seq, panes: panes, projDir: projDir}
	}
}

func (a *App) handleBulkInputPanes(msg bulkInputPanesMsg) {
	// Shares liveInputSeq with openLiveInput: whichever input request is
	// newest owns the modal.
	if msg.seq != a.liveInputSeq {
		return
	}
	if len(msg.panes) == 0 {
		a.copiedMsg = "No live Claude panes"
		return
	}
	a.liveInputPanes = msg.panes
	a.liveInputModal = newInputModal()
	a.liveInputModal.title = fmt.Sprintf("Send to %d panes", len(msg.panes))
	a.liveInputActive = true
	a.liveInputProjDir = msg.projDir
}

// shareRefSentMsg reports the outcome of sending a share-ref prompt.
type shareRefSentMsg struct {
	okStatus string
	err      error
}

func shareRefSendCmd(target session.Session, prompt, okStatus string) tea.Cmd {
	return func() tea.Msg {
		pane, found := findPane(target.ProjectPath, target.ID)
		if !found || !paneHasClaude(pane.PID) {
			return shareRefSentMsg{err: fmt.Errorf("no live Claude pane for %s", shortSessionLabel(target))}
		}
		return shareRefSentMsg{okStatus: okStatus, err: sendKeys(pane, prompt)}
	}
}

func (a *App) handleShareRefSent(msg shareRefSentMsg) {
	if msg.err != nil {
		a.copiedMsg = "Share failed: " + msg.err.Error()
		return
	}
	a.copiedMsg = msg.okStatus
}

// sendToPanes sends text to every pane and joins the errors so a partial
// failure is reported instead of the last error only.
func sendToPanes(panes []tmux.Pane, text string) error {
	var errs []error
	for _, p := range panes {
		if err := sendKeys(p, text); err != nil {
			errs = append(errs, fmt.Errorf("%s:%s.%s: %w", p.Session, p.Window, p.Pane, err))
		}
	}
	return errors.Join(errs...)
}
