package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/keyolk/ccx/internal/session"
	"github.com/keyolk/ccx/internal/tui"
)

var version = "dev"

func main() {
	var (
		showVersion  bool
		claudeDir    string
		tmuxEnabled  bool
		tmuxAutoLive bool
		worktreeDir  string
		searchQuery  string
	)

	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.BoolVar(&showVersion, "v", false, "print version and exit (shorthand)")
	flag.StringVar(&claudeDir, "dir", "", "path to Claude data directory (default: ~/.claude)")
	flag.BoolVar(&tmuxEnabled, "tmux", false, "enable tmux integration (auto-detected if inside tmux)")
	flag.BoolVar(&tmuxAutoLive, "tmux-auto-live", false, "auto-enter live session in same tmux window on startup")
	flag.StringVar(&worktreeDir, "worktree-dir", ".worktree", "subdirectory name for git worktrees")
	flag.StringVar(&searchQuery, "search", "", "start with session list filtered by search query")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "ccx — Claude Code Explorer\n\n")
		fmt.Fprintf(os.Stderr, "Usage: ccx [flags]\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if showVersion {
		fmt.Println("ccx", version)
		os.Exit(0)
	}

	sessions, err := session.ScanSessions(claudeDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error scanning sessions: %v\n", err)
		os.Exit(1)
	}

	// Auto-detect tmux unless explicitly set
	if !tmuxEnabled && os.Getenv("TMUX") != "" {
		tmuxEnabled = true
	}

	if len(sessions) == 0 {
		dir := claudeDir
		if dir == "" {
			dir = "~/.claude/projects/"
		}
		fmt.Fprintf(os.Stderr, "No sessions found in %s\n", dir)
		os.Exit(0)
	}

	app := tui.NewApp(sessions, tui.Config{
		TmuxEnabled:  tmuxEnabled,
		TmuxAutoLive: tmuxAutoLive,
		WorktreeDir:  worktreeDir,
		SearchQuery:  searchQuery,
	})
	p := tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
