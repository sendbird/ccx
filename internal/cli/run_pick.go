package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
	"github.com/sendbird/ccx/internal/tmux"
	"github.com/sendbird/ccx/internal/tui"
	"golang.org/x/term"
)

type PickSessionExitCode int

const (
	PickSessionConfirmed PickSessionExitCode = 0
	PickSessionError     PickSessionExitCode = 1
	PickSessionNoMatches PickSessionExitCode = 2
	PickSessionCancelled PickSessionExitCode = 130
)

type sessionResultEntry struct {
	ID              string `json:"id"`
	ProjectRootPath string `json:"project_root_path"`
	TranscriptPath  string `json:"transcript_path"`
}

// RunPickSessionTUI launches the full ccx TUI in pick mode on stderr,
// leaving stdout clean for the JSON envelope. The user confirms the pick
// via the actions menu (x → P). Returns the exit code the caller should use.
func RunPickSessionTUI(claudeDir, search string, multi bool) PickSessionExitCode {
	sessions := session.LoadCachedSessions(claudeDir)
	if len(sessions) == 0 {
		livePaths := tmux.DetectLiveProjectPaths()
		sessions, _ = session.ScanSessionsForPaths(claudeDir, livePaths)
	}
	if len(sessions) == 0 {
		fmt.Fprintln(os.Stderr, "ccx: no sessions found")
		return PickSessionNoMatches
	}

	if !term.IsTerminal(int(os.Stderr.Fd())) {
		fmt.Fprintln(os.Stderr, "ccx: stderr is not a tty; interactive picker cannot render")
		return PickSessionError
	}

	configPath := filepath.Join(os.Getenv("HOME"), ".config", "ccx", "config.yaml")
	km, _, _, _ := tui.LoadCCXConfig(configPath)

	app := tui.NewApp(sessions, tui.Config{
		ClaudeDir:   claudeDir,
		TmuxEnabled: tmux.InTmux(),
		WorktreeDir: ".worktree",
		SearchQuery: search,
		Keymap:      km,
		PickMode:    true,
	})

	p := tea.NewProgram(app,
		tea.WithAltScreen(),
		tea.WithOutput(os.Stderr),
		tea.WithMouseCellMotion(),
	)
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "ccx: picker failed: %v\n", err)
		return PickSessionError
	}

	result := app.PickResult()
	if result == nil {
		return PickSessionCancelled
	}

	switch r := result.(type) {
	case tui.SessionsResult:
		if len(r.Items) == 0 {
			return PickSessionCancelled
		}
		items := r.Items
		if !multi && len(items) > 1 {
			items = items[:1]
		}
		entries := make([]sessionResultEntry, 0, len(items))
		for _, s := range items {
			entries = append(entries, sessionResultEntry{
				ID:              s.ID,
				ProjectRootPath: s.ProjectPath,
				TranscriptPath:  s.FilePath,
			})
		}
		envelope := struct {
			Sessions []sessionResultEntry `json:"sessions"`
		}{Sessions: entries}
		enc := json.NewEncoder(os.Stdout)
		if err := enc.Encode(envelope); err != nil {
			fmt.Fprintf(os.Stderr, "ccx: failed to encode result: %v\n", err)
			return PickSessionError
		}
		return PickSessionConfirmed
	default:
		fmt.Fprintf(os.Stderr, "ccx: unexpected pick result type %T\n", result)
		return PickSessionError
	}
}
