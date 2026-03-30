package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
	"github.com/sendbird/ccx/internal/tmux"
	"github.com/sendbird/ccx/internal/tui"
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
		groupMode    string
		previewMode  string
		viewMode     string
	)

	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.BoolVar(&showVersion, "v", false, "print version and exit (shorthand)")
	flag.StringVar(&claudeDir, "dir", "", "path to Claude data directory (default: ~/.claude)")
	flag.BoolVar(&tmuxEnabled, "tmux", false, "enable tmux integration (auto-detected if inside tmux)")
	flag.BoolVar(&tmuxAutoLive, "tmux-auto-live", false, "auto-enter live session in same tmux window on startup")
	flag.StringVar(&worktreeDir, "worktree-dir", ".worktree", "subdirectory name for git worktrees")
	flag.StringVar(&searchQuery, "search", "", "start with session list filtered by search query")
	flag.StringVar(&groupMode, "group", "", "initial group mode (flat|proj|tree|chain|fork)")
	flag.StringVar(&previewMode, "preview", "", "initial preview mode (conv|stats|mem|tasks)")
	flag.StringVar(&viewMode, "view", "", "initial view (sessions|config|plugins|stats)")
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

	if claudeDir == "" {
		claudeDir = os.Getenv("CLAUDE_CONFIG_DIR")
	}
	if claudeDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		claudeDir = home + "/.claude"
	}

	// Auto-detect tmux unless explicitly set
	if !tmuxEnabled && os.Getenv("TMUX") != "" {
		tmuxEnabled = true
	}

	configPath := filepath.Join(os.Getenv("HOME"), ".config", "ccx", "config.yaml")
	km, _, _, _ := tui.LoadCCXConfig(configPath)

	// Load cached sessions for instant first paint (~5ms).
	// Falls back to live-only scan (~40ms) if no cache exists.
	// Full scan happens asynchronously inside the TUI.
	initialSessions := session.LoadCachedSessions(claudeDir)
	if len(initialSessions) == 0 {
		livePaths := tmux.DetectLiveProjectPaths()
		initialSessions, _ = session.ScanSessionsForPaths(claudeDir, livePaths)
	}

	app := tui.NewApp(initialSessions, tui.Config{
		ClaudeDir:    claudeDir,
		TmuxEnabled:  tmuxEnabled,
		TmuxAutoLive: tmuxAutoLive,
		WorktreeDir:  worktreeDir,
		SearchQuery:  searchQuery,
		Keymap:       km,
		GroupMode:    groupMode,
		PreviewMode:  previewMode,
		ViewMode:     viewMode,
	})
	p := tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
