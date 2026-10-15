package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sendbird/ccx/internal/claudecmd"
	"github.com/sendbird/ccx/internal/session"
	"github.com/sendbird/ccx/internal/tmux"
)

// `git worktree add` checks out a full tree and can take seconds on a large
// repo. It used to run inside Update, freezing input and redraw for that
// long. Each worktree action now does all git/tmux/filesystem work in a
// tea.Cmd and returns a worktreeDoneMsg that Update applies.

// worktreeDoneMsg is the outcome of an async worktree action.
type worktreeDoneMsg struct {
	status  string // shown via status toast
	refresh bool   // a new Claude window was spawned; poll for its session file
	execDir string // non-tmux: take over the terminal with Claude in this dir
	move    *worktreeMove
}

// worktreeMove describes sessions relocated from oldPath to newPath on disk;
// Update mirrors it into the in-memory session list.
type worktreeMove struct {
	oldPath, newPath string
}

func gitRepoRoot(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// addWorktree checks out ref at wtPath; if that fails (typically because ref
// is already checked out elsewhere, or does not exist yet) it creates
// newBranch at wtPath, starting from startPoint when given.
func addWorktree(repoRoot, wtPath, ref, newBranch, startPoint string) error {
	if err := exec.Command("git", "-C", repoRoot, "worktree", "add", wtPath, ref).Run(); err == nil {
		return nil
	}
	args := []string{"-C", repoRoot, "worktree", "add", "-b", newBranch, wtPath}
	if startPoint != "" {
		args = append(args, startPoint)
	}
	return exec.Command("git", args...).Run()
}

// newWorktreeSessionCmd creates a worktree for branch and starts a new Claude
// session in it. okStatus is shown after a tmux spawn; execStatus before a
// non-tmux terminal takeover. Config values are captured by the caller so
// the goroutine never reads the model.
func newWorktreeSessionCmd(projectPath, worktreeDir, branch, windowName string,
	claudeCfg claudecmd.Config, okStatus, spawnFailStatus, execStatus string) tea.Cmd {
	return func() tea.Msg {
		repoRoot, err := gitRepoRoot(projectPath)
		if err != nil {
			return worktreeDoneMsg{status: "Not a git repo"}
		}
		wtPath := filepath.Join(repoRoot, worktreeDir, branch)
		if err := addWorktree(repoRoot, wtPath, branch, branch, ""); err != nil {
			return worktreeDoneMsg{status: "Worktree failed: " + err.Error()}
		}
		if windowName == "" {
			windowName = filepath.Base(repoRoot) + "/" + branch
		}
		if tmux.InTmux() {
			if err := tmux.NewWindowClaudeNewWithConfig(windowName, wtPath, claudeCfg); err != nil {
				return worktreeDoneMsg{status: spawnFailStatus}
			}
			return worktreeDoneMsg{status: okStatus, refresh: true}
		}
		return worktreeDoneMsg{status: execStatus, execDir: wtPath}
	}
}

// executeNewWorktreeSession creates a git worktree and spawns a new Claude session in it.
func (a *App) executeNewWorktreeSession(sess session.Session, branch string) (tea.Model, tea.Cmd) {
	a.flash("Creating worktree…")
	return a, newWorktreeSessionCmd(sess.ProjectPath, a.config.WorktreeDir, branch, "", a.config.Claude,
		fmt.Sprintf("New session → %s/%s", a.config.WorktreeDir, branch), "Spawn failed", "")
}

// executeCmdWorktreeNew handles "worktree:new <branch>" / "wt:new <branch>".
// It creates a git worktree for the given branch and opens a new Claude session in it.
func (a *App) executeCmdWorktreeNew(input string) (tea.Model, tea.Cmd) {
	parts := strings.Fields(input)
	if len(parts) < 2 {
		a.flash("Usage: worktree:new <branch>")
		return a, nil
	}
	branch := parts[len(parts)-1]

	sess, ok := a.selectedSession()
	if !ok || sess.ProjectPath == "" {
		a.flash("No session with project selected")
		return a, nil
	}
	a.flash("Creating worktree…")
	return a, newWorktreeSessionCmd(sess.ProjectPath, a.config.WorktreeDir, branch, branch, a.config.Claude,
		fmt.Sprintf("Worktree + session → %s/%s", a.config.WorktreeDir, branch),
		"Worktree created but spawn failed",
		fmt.Sprintf("Worktree created → %s/%s", a.config.WorktreeDir, branch))
}

// executeWorktree moves the session's project into a new worktree named name.
func (a *App) executeWorktree(sess session.Session, name string) (tea.Model, tea.Cmd) {
	projectPath, branch, worktreeDir := sess.ProjectPath, sess.GitBranch, a.config.WorktreeDir
	a.flash("Creating worktree…")
	return a, func() tea.Msg {
		repoRoot, err := gitRepoRoot(projectPath)
		if err != nil {
			return worktreeDoneMsg{status: "Not a git repo"}
		}
		if branch == "" {
			bOut, err := exec.Command("git", "-C", projectPath, "branch", "--show-current").Output()
			if err != nil {
				return worktreeDoneMsg{status: "Cannot determine branch"}
			}
			branch = strings.TrimSpace(string(bOut))
		}
		wtPath := filepath.Join(repoRoot, worktreeDir, name)
		// Try the existing branch first; if it is already checked out,
		// create a new branch from it.
		if err := addWorktree(repoRoot, wtPath, branch, name, branch); err != nil {
			return worktreeDoneMsg{status: "Worktree failed: " + err.Error()}
		}
		if err := session.MoveProject(projectPath, wtPath); err != nil {
			return worktreeDoneMsg{status: "Move failed: " + err.Error()}
		}
		return worktreeDoneMsg{
			status: fmt.Sprintf("Worktree created → %s/%s", worktreeDir, name),
			move:   &worktreeMove{oldPath: projectPath, newPath: wtPath},
		}
	}
}

func (a *App) handleWorktreeDone(msg worktreeDoneMsg) tea.Cmd {
	a.flash(msg.status)
	if mv := msg.move; mv != nil {
		home, _ := os.UserHomeDir()
		newName := session.ShortenPath(mv.newPath, home)
		oldEncoded := session.EncodeProjectPath(mv.oldPath)
		newEncoded := session.EncodeProjectPath(mv.newPath)
		// Match by path, not index: the list may have been rescanned while
		// the worktree was being created.
		for i := range a.sessions {
			if a.sessions[i].ProjectPath == mv.oldPath {
				a.sessions[i].ProjectPath = mv.newPath
				a.sessions[i].ProjectName = newName
				a.sessions[i].IsWorktree = true
				a.sessions[i].FilePath = strings.Replace(a.sessions[i].FilePath, oldEncoded, newEncoded, 1)
			}
		}
		a.rebuildSessionList()
	}
	if msg.refresh {
		return a.delayedRefreshCmd()
	}
	if msg.execDir != "" {
		cmd, err := a.claudeCmd(msg.execDir)
		if err != nil {
			a.flash("Claude command failed: " + err.Error())
			return nil
		}
		return tea.ExecProcess(cmd, func(err error) tea.Msg {
			return tea.QuitMsg{}
		})
	}
	return nil
}
