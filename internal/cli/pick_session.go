package cli

import (
	"encoding/json"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
	"github.com/sendbird/ccx/internal/tmux"
	"golang.org/x/term"
)

type PickSessionExitCode int

const (
	PickSessionConfirmed PickSessionExitCode = 0
	PickSessionError     PickSessionExitCode = 1
	PickSessionNoMatches PickSessionExitCode = 2
	PickSessionCancelled PickSessionExitCode = 130
)

// RunPickSession loads sessions, filters by query, opens an interactive picker
// on stderr, and prints the selection as a JSON envelope on stdout. Returns
// the exit code the caller should use.
func RunPickSession(claudeDir, query string, multi bool) PickSessionExitCode {
	sessions := session.LoadCachedSessions(claudeDir)
	if len(sessions) == 0 {
		livePaths := tmux.DetectLiveProjectPaths()
		sessions, _ = session.ScanSessionsForPaths(claudeDir, livePaths)
	}
	if len(sessions) == 0 {
		fmt.Fprintln(os.Stderr, "ccx: no sessions found")
		return PickSessionNoMatches
	}

	tmux.MarkLiveSessions(sessions)

	cwdPaths := collectCurrentProjectPaths()

	filterVals := make([]string, 0, len(sessions))
	filtered := make([]session.Session, 0, len(sessions))
	for _, s := range sessions {
		fv := session.FilterValueFor(s, cwdPaths)
		if !session.Matches(fv, query) {
			continue
		}
		filtered = append(filtered, s)
		filterVals = append(filterVals, fv)
	}
	if len(filtered) == 0 {
		fmt.Fprintf(os.Stderr, "ccx: no sessions match query: %q\n", query)
		return PickSessionNoMatches
	}

	if !term.IsTerminal(int(os.Stderr.Fd())) {
		fmt.Fprintln(os.Stderr, "ccx: stderr is not a tty; interactive picker cannot render")
		return PickSessionError
	}

	model := newSessionPickerModel(filtered, filterVals, query, multi)
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithOutput(os.Stderr))
	finalModel, err := p.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ccx: picker failed: %v\n", err)
		return PickSessionError
	}
	m := finalModel.(sessionPickerModel)
	if !m.confirmed {
		return PickSessionCancelled
	}

	entries := m.result()
	if len(entries) == 0 {
		return PickSessionCancelled
	}

	envelope := struct {
		Sessions []sessionResultEntry `json:"sessions"`
	}{Sessions: entries}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(envelope); err != nil {
		fmt.Fprintf(os.Stderr, "ccx: failed to encode result: %v\n", err)
		return PickSessionError
	}
	return PickSessionConfirmed
}

// collectCurrentProjectPaths returns absolute paths of the caller's current
// Claude project(s): tmux-window Claude CWDs plus the shell's cwd. Used to
// tag sessions with the `is:current` filter token.
func collectCurrentProjectPaths() []string {
	seen := make(map[string]bool)
	var out []string
	add := func(p string) {
		if p == "" {
			return
		}
		abs := session.AbsPath(p)
		if seen[abs] {
			return
		}
		seen[abs] = true
		out = append(out, abs)
	}
	for _, p := range tmux.CurrentWindowClaudes() {
		add(p)
	}
	if wd, err := os.Getwd(); err == nil {
		add(wd)
	}
	return out
}
