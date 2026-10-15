package tui

import (
	"errors"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sendbird/ccx/internal/session"
	"github.com/sendbird/ccx/internal/tmux"
)

// tmux lookups (FindPane lists every pane and walks the process table) and
// send-keys used to run inside Update, stalling input for each call. They now
// run in tea.Cmds; results come back as messages. The indirections below
// exist so tests can observe that no tmux call happens on the Update path.
var (
	findPane      = tmux.FindPane
	paneHasClaude = tmux.HasClaude
	// tuikit/tmux puts tmux's stderr in the error, so "no server running"
	// reaches the status line.
	sendInterruptK = tmux.SendInterrupt
)

// killTarget identifies a live session to stop.
type killTarget struct {
	id, projectPath string
	// anyPane allows falling back to any Claude pane in projectPath when no
	// pane runs this exact session (single-session kill only).
	anyPane bool
}

// sessionsKilledMsg reports the outcome of killSessionsCmd.
type sessionsKilledMsg struct {
	bulk     bool
	killed   []string // session IDs that received the interrupts
	notFound int
	errs     []error
}

// killSessionsCmd sends Ctrl-C twice to each target's pane to stop Claude
// gracefully. send-keys errors are collected and reported instead of being
// dropped, so a failed kill no longer reads as "Killed".
func killSessionsCmd(targets []killTarget, bulk bool) tea.Cmd {
	return func() tea.Msg {
		res := sessionsKilledMsg{bulk: bulk}
		for _, t := range targets {
			pane, found := findPane(t.projectPath, t.id)
			if !found && t.anyPane {
				pane, found = findPane(t.projectPath)
			}
			if !found {
				res.notFound++
				continue
			}
			target := pane.Session + ":" + pane.Window + "." + pane.Pane
			if err := errors.Join(sendInterruptK(pane), sendInterruptK(pane)); err != nil {
				res.errs = append(res.errs, fmt.Errorf("%s: %w", target, err))
				continue
			}
			res.killed = append(res.killed, t.id)
		}
		return res
	}
}

func (a *App) bulkKill(selected []session.Session) (tea.Model, tea.Cmd) {
	var targets []killTarget
	for _, s := range selected {
		if s.IsLive {
			targets = append(targets, killTarget{id: s.ID, projectPath: s.ProjectPath})
		}
	}
	a.clearMultiSelection()
	if len(targets) == 0 {
		a.flash("Killed 0")
		return a, nil
	}
	return a, killSessionsCmd(targets, true)
}

// killLiveSession stops the Claude process in the session's tmux pane.
func (a *App) killLiveSession(sess session.Session) (tea.Model, tea.Cmd) {
	return a, killSessionsCmd([]killTarget{{id: sess.ID, projectPath: sess.ProjectPath, anyPane: true}}, false)
}

func (a *App) handleSessionsKilled(msg sessionsKilledMsg) {
	for _, id := range msg.killed {
		// Close any live preview for a killed session.
		if a.paneProxy != nil && a.paneProxy.sessID == id {
			a.closePaneProxy()
			a.sessPreviewMode = sessPreviewConversation
			a.sessSplit.CacheKey = ""
			a.sessSplit.Focus = false
		}
	}
	switch {
	case len(msg.errs) > 0 && msg.bulk:
		a.flash(fmt.Sprintf("Killed %d, %d failed: %v", len(msg.killed), len(msg.errs), msg.errs[0]))
	case len(msg.errs) > 0:
		a.flash(fmt.Sprintf("Kill failed: %v", msg.errs[0]))
	case msg.bulk:
		a.flash(fmt.Sprintf("Killed %d", len(msg.killed)))
	case msg.notFound > 0:
		a.flash("No tmux pane found")
	default:
		a.flash("Killed")
	}
}

// liveInputPaneMsg carries the pane lookup for openLiveInput.
type liveInputPaneMsg struct {
	seq         int
	projectPath string
	pane        tmux.Pane
	ok          bool
}

func (a *App) openLiveInput(projectPath string, sessionID ...string) (tea.Model, tea.Cmd) {
	if !tmux.InTmux() {
		a.flash("Requires tmux")
		return a, nil
	}
	a.liveInputSeq++
	seq := a.liveInputSeq
	return a, func() tea.Msg {
		pane, found := findPane(projectPath, sessionID...)
		return liveInputPaneMsg{seq: seq, projectPath: projectPath, pane: pane, ok: found && paneHasClaude(pane.PID)}
	}
}

func (a *App) handleLiveInputPane(msg liveInputPaneMsg) tea.Cmd {
	// A newer request superseded this one; opening an input for the older
	// target would send text to the wrong pane.
	if msg.seq != a.liveInputSeq {
		return nil
	}
	if !msg.ok {
		a.flash("No live Claude pane")
		return nil
	}
	pane, projectPath := msg.pane, msg.projectPath
	a.liveInputPane = pane
	a.liveInputProjDir = projectPath

	// If user prefers $EDITOR, skip inline modal and open editor directly
	if a.editorInput {
		return func() tea.Msg {
			tmpFile, err := os.CreateTemp("", "ccx-input-*.md")
			if err != nil {
				return liveInputSentMsg{err: err}
			}
			tmpFile.Close()
			defer os.Remove(tmpFile.Name())

			editor := os.Getenv("EDITOR")
			if editor == "" {
				editor = "vim"
			}
			cmd := fmt.Sprintf("cd %s && %s %s", shellescape(projectPath), editor, tmpFile.Name())
			if err := tmux.DisplayPopup("80%", "70%", cmd); err != nil {
				return liveInputSentMsg{err: err}
			}

			content, readErr := os.ReadFile(tmpFile.Name())
			if readErr != nil || len(strings.TrimSpace(string(content))) == 0 {
				return liveInputSentMsg{err: fmt.Errorf("empty")}
			}
			sendText := strings.TrimRight(string(content), "\n")
			return liveInputSentMsg{err: sendKeys(pane, sendText)}
		}
	}

	a.liveInputModal = newInputModal()
	a.liveInputActive = true
	return nil
}
