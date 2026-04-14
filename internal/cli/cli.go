package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sendbird/ccx/internal/extract"
	"github.com/sendbird/ccx/internal/session"
	"github.com/sendbird/ccx/internal/tmux"
	"golang.org/x/term"
)

// Commands documents all available subcommands.
var Commands = []struct {
	Name string
	Desc string
}{
	{"urls", "List URLs from the Claude session (interactive on TTY)"},
	{"files", "List file paths touched by the session (interactive on TTY)"},
	{"images", "List image paths from the session (interactive on TTY)"},
	{"help", "Show available commands and usage"},
}

// RunResult holds the outcome of a subcommand.
type RunResult struct {
	// JumpSession/JumpUUID are set when the user chose "jump to conversation" in the picker.
	JumpSession string
	JumpUUID    string
}

// Run executes a CLI subcommand. Returns a RunResult (non-nil JumpSession means
// the caller should launch the full TUI and navigate to that message).
func Run(command, claudeDir string, plain bool) (*RunResult, error) {
	if command == "help" {
		printHelp()
		return nil, nil
	}

	filePath, sessID, err := findSessionFile(claudeDir)
	if err != nil {
		return nil, err
	}

	interactive := !plain && isTerminal()

	if interactive {
		return runInteractive(command, filePath, sessID, claudeDir)
	}
	return nil, runPlain(command, filePath, sessID, claudeDir)
}

func runPlain(command, filePath, sessID, claudeDir string) error {
	switch command {
	case "urls":
		return printItems(extract.SessionURLs(filePath), "urls")
	case "files":
		return printItems(extract.SessionFilePaths(filePath), "files")
	case "images":
		return printImages(filePath, sessID, claudeDir)
	default:
		return fmt.Errorf("unknown command: %s\nRun 'ccx help' for usage", command)
	}
}

func runInteractive(command, filePath, sessID, claudeDir string) (*RunResult, error) {
	entries, err := session.LoadMessages(filePath)
	if err != nil {
		return nil, err
	}

	home, _ := os.UserHomeDir()
	var items []PickerItem
	switch command {
	case "urls":
		items = extractURLsWithContext(entries, sessID)
	case "files":
		items = extractFilesWithContext(entries, sessID)
	case "images":
		items = extractImagesWithContext(entries, sessID, home)
	default:
		return nil, fmt.Errorf("unknown command: %s", command)
	}

	result, err := RunPicker(command, items)
	if err != nil {
		return nil, err
	}
	if result != nil {
		return &RunResult{
			JumpSession: result.SessionID,
			JumpUUID:    result.EntryUUID,
		}, nil
	}
	return nil, nil
}

func isTerminal() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

func printHelp() {
	fmt.Fprintf(os.Stderr, "ccx — Claude Code Explorer\n\n")
	fmt.Fprintf(os.Stderr, "Usage:\n")
	fmt.Fprintf(os.Stderr, "  ccx              Launch the TUI\n")
	fmt.Fprintf(os.Stderr, "  ccx <command>    Run a subcommand\n\n")
	fmt.Fprintf(os.Stderr, "Commands:\n")
	for _, c := range Commands {
		fmt.Fprintf(os.Stderr, "  %-10s %s\n", c.Name, c.Desc)
	}
	fmt.Fprintf(os.Stderr, "\nOn a TTY, subcommands launch an interactive picker.\n")
	fmt.Fprintf(os.Stderr, "When piped, output is tab-separated for fzf/awk/cut.\n")
	fmt.Fprintf(os.Stderr, "Use --plain to force non-interactive output.\n\n")
	fmt.Fprintf(os.Stderr, "Examples:\n")
	fmt.Fprintf(os.Stderr, "  ccx urls              Interactive URL picker\n")
	fmt.Fprintf(os.Stderr, "  ccx urls --plain      Plain tab-separated output\n")
	fmt.Fprintf(os.Stderr, "  ccx urls | fzf        Pipe to fzf (auto plain)\n")
	fmt.Fprintf(os.Stderr, "  ccx files             Interactive file picker\n")
	fmt.Fprintf(os.Stderr, "  ccx images            Interactive image picker\n\n")
	fmt.Fprintf(os.Stderr, "Picker keys:\n")
	fmt.Fprintf(os.Stderr, "  ↵ enter    Jump to message in full ccx TUI\n")
	fmt.Fprintf(os.Stderr, "  o          Open URL in browser\n")
	fmt.Fprintf(os.Stderr, "  e          Open file/image in $EDITOR\n")
	fmt.Fprintf(os.Stderr, "  y          Copy to clipboard\n")
	fmt.Fprintf(os.Stderr, "  space      Toggle multi-select\n")
	fmt.Fprintf(os.Stderr, "  a          Select all visible items\n")
	fmt.Fprintf(os.Stderr, "  A          Deselect all\n")
	fmt.Fprintf(os.Stderr, "  /          Search filter\n")
	fmt.Fprintf(os.Stderr, "  esc        Quit\n\n")
	fmt.Fprintf(os.Stderr, "TUI flags:\n")
	fmt.Fprintf(os.Stderr, "  -v, --version         Print version\n")
	fmt.Fprintf(os.Stderr, "  --search <query>      Start with session filter\n")
	fmt.Fprintf(os.Stderr, "  --group <mode>        Group mode: flat|proj|tree|chain|fork\n")
	fmt.Fprintf(os.Stderr, "  --view <mode>         Initial view: sessions|config|plugins|stats\n")
}

func printItems(items []extract.Item, kind string) error {
	if len(items) == 0 {
		return fmt.Errorf("no %s found in session", kind)
	}
	for _, item := range items {
		cat := strings.ToUpper(item.Category)
		if len(cat) < 5 {
			cat += strings.Repeat(" ", 5-len(cat))
		}
		fmt.Fprintf(os.Stdout, "%s\t%s\t%s\n", cat, item.Label, item.URL)
	}
	return nil
}

func printImages(filePath, sessID, claudeDir string) error {
	entries, err := session.LoadMessages(filePath)
	if err != nil {
		return err
	}

	home, _ := os.UserHomeDir()
	found := 0
	for _, e := range entries {
		for _, b := range e.Content {
			if b.Type != "image" || b.ImagePasteID <= 0 {
				continue
			}
			p := session.ImageCachePath(home, sessID, b.ImagePasteID)
			if p == "" {
				p, _ = session.ExtractImageToTemp(home, filePath, sessID, b.ImagePasteID)
			}
			if p != "" {
				ts := ""
				if !e.Timestamp.IsZero() {
					ts = e.Timestamp.Format("15:04:05")
				}
				fmt.Fprintf(os.Stdout, "%d\t%s\t%s\n", b.ImagePasteID, ts, p)
				found++
			}
		}
	}
	if found == 0 {
		return fmt.Errorf("no images found in session")
	}
	return nil
}

// findSessionFile detects Claude sessions in the same tmux window.
// If multiple sessions are found, prompts the user to choose one.
func findSessionFile(claudeDir string) (string, string, error) {
	projPaths := tmux.CurrentWindowClaudes()
	if len(projPaths) == 0 {
		live := tmux.FindLiveProjectPaths()
		for p := range live {
			projPaths = append(projPaths, p)
		}
	}
	if len(projPaths) == 0 {
		return "", "", fmt.Errorf("no Claude session found in current window")
	}

	allSessions := session.LoadCachedSessions(claudeDir)
	if len(allSessions) == 0 {
		return "", "", fmt.Errorf("no cached sessions found (run ccx once first)")
	}

	// Collect the best (most recent) session per project path
	var matches []session.Session
	seen := make(map[string]bool)
	for _, projPath := range projPaths {
		absProj, _ := filepath.Abs(projPath)
		if absProj == "" {
			absProj = projPath
		}
		var best *session.Session
		for i := range allSessions {
			absSP, _ := filepath.Abs(allSessions[i].ProjectPath)
			if absSP == "" {
				absSP = allSessions[i].ProjectPath
			}
			if absSP == absProj {
				if best == nil || allSessions[i].ModTime.After(best.ModTime) {
					best = &allSessions[i]
				}
			}
		}
		if best != nil && !seen[best.ID] {
			seen[best.ID] = true
			matches = append(matches, *best)
		}
	}

	if len(matches) == 0 {
		return "", "", fmt.Errorf("no session found matching project paths: %s", strings.Join(projPaths, ", "))
	}
	if len(matches) == 1 {
		return matches[0].FilePath, matches[0].ID, nil
	}

	// Multiple sessions — prompt user to select
	idx, err := promptSessionChoice(matches)
	if err != nil {
		return "", "", err
	}
	return matches[idx].FilePath, matches[idx].ID, nil
}

// promptSessionChoice shows a numbered list and asks the user to pick one.
func promptSessionChoice(sessions []session.Session) (int, error) {
	fmt.Fprintf(os.Stderr, "Multiple Claude sessions found in this tmux window:\n\n")
	for i, s := range sessions {
		name := s.ProjectName
		if name == "" {
			name = s.ShortID
		}
		prompt := s.FirstPrompt
		if len(prompt) > 60 {
			prompt = prompt[:57] + "..."
		}
		live := ""
		if s.IsLive {
			live = " [LIVE]"
		}
		fmt.Fprintf(os.Stderr, "  %d) %s%s — %s\n", i+1, name, live, prompt)
	}
	fmt.Fprintf(os.Stderr, "\nSelect session [1-%d]: ", len(sessions))

	var choice int
	if _, err := fmt.Scan(&choice); err != nil {
		return 0, fmt.Errorf("cancelled")
	}
	if choice < 1 || choice > len(sessions) {
		return 0, fmt.Errorf("invalid choice: %d", choice)
	}
	return choice - 1, nil
}
