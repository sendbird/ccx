package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
)

// Several preview modes used to do their disk work inline: the builder read and
// JSON-parsed a whole transcript, and whichever of Update or View reached it
// first paid the cost on the UI loop. Measured on a 226-session store in the
// daily view, one cursor move cost (p50 / p90 / max):
//
//	contexts     49.7ms / 219ms  / 722ms
//	scratchpad    372µs /  27ms  / 3.10s
//	shells        323µs / 158ms  / 676ms
//	agents        275µs / 320µs  / 195ms
//	workflows     276µs / 345µs  / 196ms
//
// while the modes that already loaded in a tea.Cmd (outputs, refs,
// conversation, stats) all sat at ~260µs. Keys queue behind that work, so
// holding j/k in the contexts pane backed the loop up by seconds.
//
// The seam is the same in every one of these modes: a pure session.* load
// followed by a cheap App-side render. This file runs the load in a tea.Cmd,
// caches the payload per session, and leaves the render where it was.
//
// View cannot dispatch a command — one returned from View() is dropped — so a
// mode is only ever driven asynchronously from Update. View reads the cache and
// renders a placeholder on a miss, which is why previewLoadsOffLoop has to be
// consulted on the View path too.

// previewLoadMsg carries a finished off-loop load back to the UI loop. The
// payload's concrete type is whatever that mode's loader produced; the switch
// in applyPreviewLoad is the only place that asserts it.
type previewLoadMsg struct {
	mode    sessPreview
	sessID  string
	dataKey string
	payload any
}

// workflowsLoad is the workflows pane's payload: the runs plus the subagents
// joined onto them, which is the pair the renderer needs.
type workflowsLoad struct {
	runs   []session.WorkflowRun
	agents []session.Subagent
}

// previewLoadsOffLoop reports whether this mode's data is loaded in a tea.Cmd
// rather than inline. Keep it in sync with the switch in previewLoader.
func previewLoadsOffLoop(mode sessPreview) bool {
	switch mode {
	case sessPreviewContexts, sessPreviewScratchpad, sessPreviewShells,
		sessPreviewAgents, sessPreviewWorkflows:
		return true
	}
	return false
}

// previewLoadKey identifies a cached load. The mtime is part of it so a
// transcript that grew is loaded again rather than served stale.
func previewLoadKey(mode sessPreview, sess session.Session) string {
	return fmt.Sprintf("%d:%s:%d", mode, sess.ID, sess.ModTime.UnixNano())
}

// previewLoader returns the off-loop half of a preview mode: the disk work,
// closed over plain values so it touches no App state and is safe in a
// goroutine. It returns nil for modes that do not load off-loop.
func (a *App) previewLoader(mode sessPreview, sess session.Session) func() any {
	claudeDir := a.config.ClaudeDir
	switch mode {
	case sessPreviewContexts:
		return func() any {
			tree, err := session.BuildSessionContextTree(claudeDir, sess)
			if err != nil {
				return err
			}
			return tree
		}
	case sessPreviewScratchpad:
		projectPath, id := sess.ProjectPath, sess.ID
		return func() any { return session.LoadScratchpadFiles(projectPath, id) }
	case sessPreviewShells:
		path := sess.FilePath
		return func() any {
			entries, err := session.LoadMessages(path)
			if err != nil {
				return []session.ShellJob{}
			}
			return session.LoadShellJobsFromEntries(entries)
		}
	case sessPreviewAgents:
		path := sess.FilePath
		return func() any {
			agents, err := session.FindSubagents(path)
			if err != nil {
				return []session.Subagent{}
			}
			return agents
		}
	case sessPreviewWorkflows:
		path := sess.FilePath
		return func() any {
			runs, _ := session.FindWorkflows(path)
			all, _ := session.FindSubagents(path)
			return workflowsLoad{runs: runs, agents: session.JoinWorkflowAgents(runs, all)}
		}
	}
	return nil
}

// previewLoadCmd dispatches a mode's load, deduplicating against one already in
// flight for the same key. It returns nil when the payload is already cached or
// a load is pending, so callers can treat nil as "nothing more to do".
func (a *App) previewLoadCmd(mode sessPreview, sess session.Session) tea.Cmd {
	key := previewLoadKey(mode, sess)
	if _, ok := a.previewLoads.Get(key); ok {
		return nil
	}
	if a.previewLoadInFlight[key] {
		return nil
	}
	load := a.previewLoader(mode, sess)
	if load == nil {
		return nil
	}
	if a.previewLoadInFlight == nil {
		a.previewLoadInFlight = make(map[string]bool)
	}
	a.previewLoadInFlight[key] = true
	id := sess.ID
	return func() tea.Msg {
		return previewLoadMsg{mode: mode, sessID: id, dataKey: key, payload: load()}
	}
}

// applyPreviewLoad copies a cached payload into the per-mode App fields the
// renderers read, and reports whether it recognised the payload. The cursor is
// only reset when the payload belongs to a different session than the one the
// pane is currently showing, so a reload of the same session (a grown
// transcript) does not jump the highlight.
func (a *App) applyPreviewLoad(mode sessPreview, sess session.Session, payload any) bool {
	switch mode {
	case sessPreviewContexts:
		switch v := payload.(type) {
		case *session.SessionContextTree:
			a.sessCtxTree = v
			a.sessCtxNodes = flattenContextNodes(v)
			if a.sessCtxCursor >= len(a.sessCtxNodes) {
				a.sessCtxCursor = max(len(a.sessCtxNodes)-1, 0)
			}
			a.sessCtxErr = nil
			return true
		case error:
			a.sessCtxTree, a.sessCtxNodes, a.sessCtxErr = nil, nil, v
			return true
		}
	case sessPreviewScratchpad:
		if v, ok := payload.([]session.ScratchpadFile); ok {
			a.sessScratchpadFiles = v
			return true
		}
	case sessPreviewShells:
		if v, ok := payload.([]session.ShellJob); ok {
			a.sessShellJobs = v
			return true
		}
	case sessPreviewAgents:
		if v, ok := payload.([]session.Subagent); ok {
			a.sessPreviewAgents = v
			if a.sessAgentCursor >= len(v) {
				a.sessAgentCursor = 0
			}
			return true
		}
	case sessPreviewWorkflows:
		if v, ok := payload.(workflowsLoad); ok {
			a.sessWfRuns, a.sessWfAgents = v.runs, v.agents
			if a.sessWfCursor >= len(v.agents) {
				a.sessWfCursor = 0
			}
			return true
		}
	}
	return false
}

// adoptPreviewLoad makes a mode's payload current for sess, returning a command
// to load it when it is not cached yet.
//
// Adopting before dispatching is what makes revisiting a row free: the
// per-mode App fields track one session, so a row change clears them, and
// without this the next visit would re-read a transcript already on hand.
func (a *App) adoptPreviewLoad(mode sessPreview, sess session.Session) tea.Cmd {
	if payload, ok := a.previewLoads.Get(previewLoadKey(mode, sess)); ok {
		a.applyPreviewLoad(mode, sess, payload)
		return nil
	}
	return a.previewLoadCmd(mode, sess)
}

// handlePreviewLoad records a finished load and applies it when the pane is
// still showing that session and mode.
//
// The payload is stored even when the cursor has moved on: the disk work
// already happened, and keeping it is what makes moving back free.
func (a *App) handlePreviewLoad(msg previewLoadMsg) tea.Cmd {
	delete(a.previewLoadInFlight, msg.dataKey)
	a.previewLoads.Set(msg.dataKey, msg.payload)
	if a.sessPreviewMode != msg.mode {
		return nil
	}
	sess, ok := a.selectedSession()
	if !ok || sess.ID != msg.sessID || previewLoadKey(msg.mode, sess) != msg.dataKey {
		return nil
	}
	// Re-run the mode's update so the payload lands in its App fields and the
	// placeholder is replaced. The load is cached now, so this does no I/O.
	a.sessSplit.CacheKey = ""
	return a.updateSessionPreview()
}
