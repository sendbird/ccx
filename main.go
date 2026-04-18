package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/cli"
	"github.com/sendbird/ccx/internal/kitty"
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
		jumpSession  string
		jumpUUID     string
	)

	// Handle subcommands before global flag parsing
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "sessions":
			fs := flag.NewFlagSet("sessions", flag.ExitOnError)
			all := fs.Bool("all", false, "list all sessions (default: current tmux window only)")
			fs.Parse(os.Args[2:])
			dir := resolveClaudeDir("")
			if err := cli.RunSessions(dir, *all); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		case "pick":
			if len(os.Args) < 3 {
				fmt.Fprintln(os.Stderr, "ccx pick: missing entity (expected: session)")
				os.Exit(1)
			}
			entity := os.Args[2]
			if entity != "session" {
				fmt.Fprintf(os.Stderr, "ccx pick: unknown entity %q (expected: session)\n", entity)
				os.Exit(1)
			}
			fs := flag.NewFlagSet("pick session", flag.ExitOnError)
			query := fs.String("query", "", "initial filter query (same syntax as TUI /)")
			multi := fs.Bool("multi", false, "allow multi-select")
			dirFlag := fs.String("dir", "", "path to Claude data directory (default: ~/.claude)")
			fs.Parse(os.Args[3:])

			dir := resolveClaudeDir(*dirFlag)
			os.Exit(int(cli.RunPickSession(dir, *query, *multi)))
		case "urls", "files", "changes", "images", "conversation", "help":
			subcmd := os.Args[1]
			fs := flag.NewFlagSet(subcmd, flag.ExitOnError)
			plain := fs.Bool("plain", false, "force plain text output (no interactive picker)")
			fs.Parse(os.Args[2:])

			dir := resolveClaudeDir("")
			result, err := cli.Run(subcmd, dir, *plain)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			if result != nil && result.JumpSession != "" {
				// Picker selected "jump to conversation" — launch full TUI
				jumpSession = result.JumpSession
				jumpUUID = result.JumpUUID
				claudeDir = dir
			} else {
				os.Exit(0)
			}
		}
	}

	// Only parse global flags if we didn't handle a subcommand
	if jumpSession == "" {
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
			fmt.Fprintf(os.Stderr, "Usage: ccx [flags]\n")
			fmt.Fprintf(os.Stderr, "       ccx <command> [--plain]\n\n")
			fmt.Fprintf(os.Stderr, "Commands:\n")
			for _, c := range cli.Commands {
				fmt.Fprintf(os.Stderr, "  %-10s %s\n", c.Name, c.Desc)
			}
			fmt.Fprintf(os.Stderr, "\nFlags:\n")
			flag.PrintDefaults()
		}
		flag.Parse()

		if showVersion {
			fmt.Println("ccx", version)
			os.Exit(0)
		}

		claudeDir = resolveClaudeDir(claudeDir)
	}

	if !tmuxEnabled && os.Getenv("TMUX") != "" {
		tmuxEnabled = true
	}

	configPath := filepath.Join(os.Getenv("HOME"), ".config", "ccx", "config.yaml")
	km, _, _, _ := tui.LoadCCXConfig(configPath)

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
		JumpSession:  jumpSession,
		JumpUUID:     jumpUUID,
	})
	p := tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	// Clear any Kitty inline images before exiting
	if kitty.Supported() {
		fmt.Print(kitty.ClearImages())
	}
}

func resolveClaudeDir(dir string) string {
	if dir == "" {
		dir = os.Getenv("CLAUDE_CONFIG_DIR")
	}
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
		dir = home + "/.claude"
	}
	return dir
}
