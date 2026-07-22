package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/clauderegistry"
	"github.com/sendbird/ccx/internal/cli"
	"github.com/sendbird/ccx/internal/kitty"
	"github.com/sendbird/ccx/internal/session"
	"github.com/sendbird/ccx/internal/tui"
	"gopkg.in/yaml.v3"
)

var version = "dev"

func defaultConfigHeader() string {
	return "# ccx configuration\n# Keybindings: session, actions, views, navigation\n# Preferences: preferences section (auto-saved on quit)\n# Claude: command_template controls local Claude launches; {{args}} expands to ccx-provided args.\n# Open: command_template controls how URLs open; {{url}} expands to the URL (empty = OS default open/xdg-open).\n\n"
}

func runConfigCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: ccx config <view|edit|path|get|set> [path] [value]")
	}
	path := filepath.Join(os.Getenv("HOME"), ".config", "ccx", "config.yaml")
	switch args[0] {
	case "path":
		fmt.Println(path)
		return nil
	case "view", "list", "ls":
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		fmt.Print(string(data))
		return nil
	case "edit":
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := os.WriteFile(path, []byte(defaultConfigHeader()), 0644); err != nil {
				return err
			}
		}
		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi"
		}
		cmd := exec.Command(editor, path)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	case "get":
		if len(args) != 2 {
			return fmt.Errorf("usage: ccx config get <dot.path>")
		}
		cfg, err := readConfigMap(path)
		if err != nil {
			return err
		}
		val, ok := getConfigPath(cfg, args[1])
		if !ok {
			return fmt.Errorf("config path not found: %s", args[1])
		}
		data, err := yaml.Marshal(val)
		if err != nil {
			return err
		}
		fmt.Print(string(data))
		return nil
	case "set":
		if len(args) != 3 {
			return fmt.Errorf("usage: ccx config set <dot.path> <value>")
		}
		cfg, err := readConfigMap(path)
		if err != nil {
			return err
		}
		setConfigPath(cfg, args[1], parseConfigValue(args[2]))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		data, err := yaml.Marshal(cfg)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(defaultConfigHeader()+string(data)), 0644); err != nil {
			return err
		}
		fmt.Printf("%s = %v\n", args[1], parseConfigValue(args[2]))
		return nil
	default:
		return fmt.Errorf("unknown config command %q", args[0])
	}
}

func readConfigMap(path string) (map[string]interface{}, error) {
	cfg := map[string]interface{}{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func getConfigPath(cfg map[string]interface{}, dotPath string) (interface{}, bool) {
	cur := interface{}(cfg)
	for _, part := range strings.Split(dotPath, ".") {
		m, ok := cur.(map[string]interface{})
		if !ok {
			return nil, false
		}
		cur, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func setConfigPath(cfg map[string]interface{}, dotPath string, value interface{}) {
	parts := strings.Split(dotPath, ".")
	cur := cfg
	for _, part := range parts[:len(parts)-1] {
		next, _ := cur[part].(map[string]interface{})
		if next == nil {
			next = map[string]interface{}{}
			cur[part] = next
		}
		cur = next
	}
	cur[parts[len(parts)-1]] = value
}

func parseConfigValue(value string) interface{} {
	switch value {
	case "true":
		return true
	case "false":
		return false
	case "null", "nil", "~":
		return nil
	default:
		return value
	}
}

func main() {
	var (
		showVersion  bool
		claudeDir    string
		tmuxEnabled  bool
		tmuxAutoLive bool
		initialFocus string
		worktreeDir  string
		searchQuery  string
		groupMode    string
		previewMode  string
		viewMode     string
		sessionID    string
		jumpSession  string
		jumpUUID     string
	)

	// Handle subcommands before global flag parsing
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "sessions":
			fs := flag.NewFlagSet("sessions", flag.ExitOnError)
			all := fs.Bool("all", false, "list all sessions (default: current tmux window only)")
			pick := fs.Bool("pick", false, "launch interactive picker and emit JSON on stdout")
			search := fs.String("search", "", "initial filter query (same syntax as TUI /)")
			multi := fs.Bool("multi", false, "allow multi-select (with --pick)")
			dirFlag := fs.String("dir", "", "path to Claude data directory (default: ~/.claude)")
			fs.Parse(os.Args[2:])
			dir := resolveClaudeDir(*dirFlag)
			if *pick {
				os.Exit(int(cli.RunPickSessionTUI(dir, *search, *multi)))
			}
			if err := cli.RunSessions(dir, *all); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		case "config":
			if err := runConfigCommand(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		case "move":
			fs := flag.NewFlagSet("move", flag.ExitOnError)
			sess := fs.String("session", "", "session ID to move (prefix match); default: current tmux window's session")
			from := fs.String("from", "", "project directory to move by path instead of session ID (moves every session under it)")
			dirFlag := fs.String("dir", "", "path to Claude data directory (default: ~/.claude)")
			fs.Usage = func() {
				fmt.Fprintf(os.Stderr, "Move a Claude session's project path to a new location.\n\n")
				fmt.Fprintf(os.Stderr, "Usage:\n")
				fmt.Fprintf(os.Stderr, "  ccx move <new-path>                  Move current tmux session's project path\n")
				fmt.Fprintf(os.Stderr, "  ccx move --session <id> <new-path>   Move a specific session by ID\n")
				fmt.Fprintf(os.Stderr, "  ccx move --from <dir> <new-path>     Move a project dir by path (no session lookup)\n\n")
				fmt.Fprintf(os.Stderr, "Flags:\n")
				fs.PrintDefaults()
			}
			fs.Parse(os.Args[2:])
			if fs.NArg() != 1 {
				fs.Usage()
				os.Exit(1)
			}
			dir := resolveClaudeDir(*dirFlag)
			if err := cli.RunMove(dir, *sess, *from, fs.Arg(0)); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			os.Exit(0)
		case "urls", "refs", "files", "changes", "images", "conversation", "info", "help":
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
		flag.StringVar(&initialFocus, "initial-focus", "", "startup focus strategy: tmux (default: tmux window match, else most recent) | cwd (adds a CWD-based directory-walk fallback before most recent)")
		flag.StringVar(&worktreeDir, "worktree-dir", ".worktree", "subdirectory name for git worktrees")
		flag.StringVar(&searchQuery, "search", "", "start with session list filtered by search query")
		flag.StringVar(&groupMode, "group", "", "initial group mode (flat|proj|tree|chain|fork)")
		flag.StringVar(&previewMode, "preview", "", "initial preview mode (conv|stats|mem|tasks)")
		flag.StringVar(&viewMode, "view", "", "initial view (sessions|config|plugins|stats)")
		flag.StringVar(&sessionID, "session", "", "open a specific session by ID (prefix match)")
		flag.Usage = func() {
			fmt.Fprintf(os.Stderr, "ccx — Claude Code Explorer\n\n")
			fmt.Fprintf(os.Stderr, "Usage: ccx [flags]\n")
			fmt.Fprintf(os.Stderr, "       ccx <command> [--plain]\n")
			fmt.Fprintf(os.Stderr, "       ccx config <view|edit|path|get|set> ...\n\n")
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
	km, _, _, _, cc, oc := tui.LoadCCXConfig(configPath)

	initialSessions := session.LoadCachedSessions(claudeDir)
	if len(initialSessions) == 0 {
		livePaths := clauderegistry.Cwds()
		initialSessions, _ = session.ScanSessionsForPaths(claudeDir, livePaths)
	}

	if sessionID != "" {
		found := false
		for _, s := range initialSessions {
			if strings.HasPrefix(s.ID, sessionID) {
				jumpSession = s.ID
				found = true
				break
			}
		}
		if !found {
			if s, ok := session.FindSessionByID(claudeDir, sessionID); ok {
				initialSessions = append([]session.Session{s}, initialSessions...)
				jumpSession = s.ID
			} else {
				fmt.Fprintf(os.Stderr, "Error: session %q not found\n", sessionID)
				os.Exit(1)
			}
		}
	}

	app := tui.NewApp(initialSessions, tui.Config{
		ClaudeDir:    claudeDir,
		TmuxEnabled:  tmuxEnabled,
		TmuxAutoLive: tmuxAutoLive,
		InitialFocus: initialFocus,
		WorktreeDir:  worktreeDir,
		SearchQuery:  searchQuery,
		Keymap:       km,
		GroupMode:    groupMode,
		PreviewMode:  previewMode,
		ViewMode:     viewMode,
		JumpSession:  jumpSession,
		JumpUUID:     jumpUUID,
		Claude:       cc,
		Open:         oc,
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
