package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/sendbird/ccx/internal/session"
	"github.com/sendbird/ccx/internal/tmux"
)

// shareRefItem is one shareable artifact from the source session: a memory
// note, a scratchpad file, or a plan file. Path is the absolute filesystem
// path the target session can reference via @<path>.
type shareRefItem struct {
	Label string // display label (file name / note name / plan slug)
	Path  string // absolute path to inject as @<path>
	Kind  string // "memory" | "scratchpad" | "plan"
}

// shareRef stage values for the two-step picker.
const (
	shareRefStageItem   = 0 // pick which artifact to share
	shareRefStageTarget = 1 // pick which live session to share it to
)

// openShareRef opens a two-step picker: (1) pick a shareable artifact from the
// currently selected session, (2) pick a target live session to receive an
// @<path> reference prompt. The prompt is injected into the target's tmux pane
// via tmux.SendKeys — the same channel openLiveInput uses.
func (a *App) openShareRef() (tea.Model, tea.Cmd) {
	sess, ok := a.selectedSession()
	if !ok {
		a.copiedMsg = "No session selected"
		return a, nil
	}
	items := gatherShareRefItems(sess)
	if len(items) == 0 {
		a.copiedMsg = "Nothing shareable (no memory/scratchpad/plan)"
		return a, nil
	}
	a.shareRefMenu = true
	a.shareRefStage = shareRefStageItem
	a.shareRefItems = items
	a.shareRefCursor = 0
	a.shareRefPicked = shareRefItem{}
	a.shareRefSrcShort = shortSessionLabel(sess)
	a.shareRefSrcID = sess.ID
	a.shareRefTargets = nil
	return a, nil
}

// gatherShareRefItems collects memory notes, scratchpad files, and plan files
// for a session into a flat list of shareable references.
func gatherShareRefItems(sess session.Session) []shareRefItem {
	var items []shareRefItem
	home, _ := os.UserHomeDir()

	// Memory notes: ~/.claude/projects/<enc>/memory/<file>.md (per-project).
	for _, note := range session.LoadMemoryNotes(sess.ProjectPath, home) {
		path := filepath.Join(home, ".claude", "projects", session.EncodeProjectPath(sess.ProjectPath), "memory", note.FileName)
		items = append(items, shareRefItem{Label: "memory: " + note.Name, Path: path, Kind: "memory"})
	}

	// Scratchpad files: per-session ephemeral working files.
	for _, f := range session.LoadScratchpadFiles(sess.ProjectPath, sess.ID) {
		items = append(items, shareRefItem{Label: "scratch: " + f.Name, Path: f.Path, Kind: "scratchpad"})
	}

	// Plan files: ~/.claude/plans/<slug>.md (global, but authored from a session).
	for _, slug := range sess.PlanSlugs {
		if slug == "" {
			continue
		}
		path := filepath.Join(home, ".claude", "plans", slug+".md")
		if _, err := os.Stat(path); err == nil {
			items = append(items, shareRefItem{Label: "plan: " + slug, Path: path, Kind: "plan"})
		}
	}
	return items
}

// gatherShareRefTargets returns live sessions the source can share TO, excluding
// the source session itself.
func (a *App) gatherShareRefTargets(srcID string) []session.Session {
	var targets []session.Session
	for _, s := range a.sessions {
		if !s.IsLive || s.ID == srcID {
			continue
		}
		targets = append(targets, s)
	}
	return targets
}

// handleShareRefKey drives the two-step picker.
func (a *App) handleShareRefKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "esc", "q":
		a.shareRefMenu = false
		return a, nil
	case "up", "k":
		if a.shareRefCursor > 0 {
			a.shareRefCursor--
		}
		return a, nil
	case "down", "j":
		max := len(a.currentShareRefList()) - 1
		if a.shareRefCursor < max {
			a.shareRefCursor++
		}
		return a, nil
	case "enter":
		return a.confirmShareRef()
	}
	return a, nil
}

// currentShareRefList returns the list for the active stage.
func (a *App) currentShareRefList() []shareRefItem {
	if a.shareRefStage == shareRefStageTarget {
		items := make([]shareRefItem, 0, len(a.shareRefTargets))
		for _, t := range a.shareRefTargets {
			items = append(items, shareRefItem{
				Label: liveSessionLabel(t),
				Path:  t.ID,
				Kind:  "target",
			})
		}
		return items
	}
	return a.shareRefItems
}

// confirmShareRef advances the picker or performs the injection.
func (a *App) confirmShareRef() (tea.Model, tea.Cmd) {
	list := a.currentShareRefList()
	if a.shareRefCursor < 0 || a.shareRefCursor >= len(list) {
		return a, nil
	}
	picked := list[a.shareRefCursor]

	if a.shareRefStage == shareRefStageItem {
		// Move to target-selection stage.
		targets := a.gatherShareRefTargets(a.shareRefSrcID)
		if len(targets) == 0 {
			a.copiedMsg = "No other live sessions to share to"
			a.shareRefMenu = false
			return a, nil
		}
		a.shareRefPicked = picked
		a.shareRefTargets = targets
		a.shareRefStage = shareRefStageTarget
		a.shareRefCursor = 0
		return a, nil
	}

	// Final stage: picked is the target session (Path holds its ID).
	a.shareRefMenu = false
	targetID := picked.Path
	pickedItem := a.shareRefPicked

	// Resolve the target session to find its tmux pane.
	var target session.Session
	for _, s := range a.shareRefTargets {
		if s.ID == targetID {
			target = s
			break
		}
	}
	if target.ID == "" {
		a.copiedMsg = "Target session not found"
		return a, nil
	}

	prompt := buildShareRefPrompt(a.shareRefSrcShort, pickedItem)
	a.copiedMsg = fmt.Sprintf("Shared @%s → %s", filepath.Base(pickedItem.Path), shortSessionLabel(target))
	return a, func() tea.Msg {
		pane, found := tmux.FindPane(target.ProjectPath, target.ID)
		if !found || !tmux.HasClaude(pane.PID) {
			return liveInputSentMsg{err: fmt.Errorf("no live Claude pane for %s", shortSessionLabel(target))}
		}
		return liveInputSentMsg{err: tmux.SendKeys(pane, prompt)}
	}
}

// buildShareRefPrompt composes the prompt injected into the target session.
// Format keeps the @<path> reference prominent so the target Claude reads it.
func buildShareRefPrompt(srcShort string, item shareRefItem) string {
	return fmt.Sprintf("다른 세션(%s)의 %s 자료를 참고해: @%s", srcShort, item.Kind, item.Path)
}

// shortSessionLabel returns a compact "id firstprompt" label for menus.
func shortSessionLabel(s session.Session) string {
	id := s.ShortID
	if id == "" && len(s.ID) >= 8 {
		id = s.ID[:8]
	}
	if id == "" {
		id = s.ID
	}
	return id
}

// liveSessionLabel renders a live target session as "id  first-prompt".
func liveSessionLabel(s session.Session) string {
	id := shortSessionLabel(s)
	prompt := s.FirstPrompt
	if len(prompt) > 40 {
		prompt = prompt[:37] + "..."
	}
	if prompt == "" {
		return id
	}
	return id + "  " + prompt
}

// renderShareRefMenu renders the two-step picker as a centered modal.
func (a *App) renderShareRefMenu() string {
	if !a.shareRefMenu {
		return ""
	}
	hl := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	d := dimStyle
	cur := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	sel := lipgloss.NewStyle().Foreground(colorUser)

	var sb strings.Builder
	if a.shareRefStage == shareRefStageItem {
		sb.WriteString(hl.Render("Share reference — pick artifact") + "\n\n")
		sb.WriteString(d.Render("from session "+a.shareRefSrcShort) + "\n\n")
		for i, it := range a.shareRefItems {
			marker := "  "
			line := it.Label
			if i == a.shareRefCursor {
				marker = cur.Render("▶ ")
				line = sel.Render(line)
			}
			sb.WriteString(marker + line + "\n")
		}
	} else {
		sb.WriteString(hl.Render("Share reference — pick target live session") + "\n\n")
		sb.WriteString(d.Render("sharing "+a.shareRefPicked.Label) + "\n\n")
		for i, t := range a.shareRefTargets {
			marker := "  "
			line := liveSessionLabel(t)
			if i == a.shareRefCursor {
				marker = cur.Render("▶ ")
				line = sel.Render(line)
			}
			sb.WriteString(marker + line + "\n")
		}
	}
	sb.WriteString("\n" + d.Render("↑↓:nav enter:confirm esc:cancel"))
	body := sb.String()

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorDim).
		Padding(0, 1)
	return boxStyle.Render(body)
}
