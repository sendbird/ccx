package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sendbird/ccx/internal/clauderegistry"
	"github.com/sendbird/ccx/internal/extract"
	"github.com/sendbird/ccx/internal/opener"
	"github.com/sendbird/ccx/internal/session"
	"github.com/sendbird/ccx/internal/tmux"
	"github.com/sendbird/ccx/internal/tui"
	"golang.org/x/term"
)

// Commands documents all available subcommands.
var Commands = []struct {
	Name string
	Desc string
}{
	{"urls", "List URLs from the Claude session (interactive on TTY)"},
	{"refs", "List PR/Jira references with resolved status (interactive on TTY)"},
	{"files", "List file paths touched by the session (interactive on TTY)"},
	{"changes", "List file changes made by the session (interactive on TTY)"},
	{"images", "List image paths from the session (interactive on TTY)"},
	{"conversation", "List conversation turns from the Claude session (interactive on TTY)"},
	{"info", "Show the matched Claude session metadata"},
	{"sessions", "List session IDs with metadata (use --pick for TUI JSON picker)"},
	{"move", "Move a session's project path to a new location (--from, --session, --help)"},
	{"config", "View/edit ccx config and get/set dot-path values"},
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
	// "sessions" is handled directly in main.go with its own flags
	if command == "sessions" {
		return nil, RunSessions(claudeDir, false)
	}
	if command == "info" {
		return nil, RunInfo(claudeDir)
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
	entries, err := session.LoadMessages(filePath)
	if err != nil {
		return err
	}

	switch command {
	case "urls":
		return printItems(extract.SessionURLs(filePath), "urls")
	case "refs":
		return printRefs(filePath)
	case "files":
		return printItems(extract.SessionFilePaths(filePath), "files")
	case "changes":
		return printChanges(extract.SessionChanges(filePath))
	case "conversation":
		return printConversation(extractConversationWithContext(entries, sessID))
	case "images":
		return printImages(filePath, sessID, claudeDir)
	default:
		return fmt.Errorf("unknown command: %s\nRun 'ccx help' for usage", command)
	}
}

func runInteractive(command, filePath, sessID, claudeDir string) (*RunResult, error) {
	items, err := extractItems(command, filePath, sessID)
	if err != nil {
		return nil, err
	}

	// Load the URL-opener config so the picker's open action honors
	// open.command_template, exactly like the TUI's open paths.
	openerCfg := loadOpenerConfig()
	// Hand the picker the context it needs to re-extract on `R` (refresh).
	ctx := pickerContext{command: command, filePath: filePath, sessID: sessID}
	result, err := RunPicker(command, items, openerCfg, ctx)
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

// pickerContext carries what the picker needs to re-extract its items when the
// user presses `R` (refresh): the subcommand and the session file it reads.
type pickerContext struct {
	command  string
	filePath string
	sessID   string
}

// extractItems loads the session transcript and builds the picker items for the
// given subcommand. Shared by initial launch and the picker's refresh.
func extractItems(command, filePath, sessID string) ([]PickerItem, error) {
	entries, err := session.LoadMessages(filePath)
	if err != nil {
		return nil, err
	}
	home, _ := os.UserHomeDir()
	switch command {
	case "urls":
		return extractURLsWithContext(entries, sessID), nil
	case "refs":
		return extractRefsWithContext(entries, sessID), nil
	case "files":
		return extractFilesWithContext(entries, sessID), nil
	case "changes":
		return extractChangesWithContext(entries, sessID), nil
	case "images":
		return extractImagesWithContext(entries, sessID, home), nil
	case "conversation":
		return extractConversationWithContext(entries, sessID), nil
	default:
		return nil, fmt.Errorf("unknown command: %s", command)
	}
}

func isTerminal() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// loadOpenerConfig reads the URL-opener config from the unified config file so
// the CLI picker opens URLs the same way the TUI does. Missing/unreadable
// config yields the zero value, which opener treats as the OS default.
func loadOpenerConfig() opener.Config {
	configPath := filepath.Join(os.Getenv("HOME"), ".config", "ccx", "config.yaml")
	_, _, _, _, _, oc := tui.LoadCCXConfig(configPath)
	return oc
}

func printHelp() {
	fmt.Fprintf(os.Stderr, "ccx — Claude Code Explorer\n\n")
	fmt.Fprintf(os.Stderr, "Usage:\n")
	fmt.Fprintf(os.Stderr, "  ccx              Launch the TUI\n")
	fmt.Fprintf(os.Stderr, "  ccx <command>    Run a subcommand\n")
	fmt.Fprintf(os.Stderr, "  ccx config <view|edit|path|get|set> ...\n\n")
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
	fmt.Fprintf(os.Stderr, "  ccx refs              Interactive PR/Jira reference picker\n")
	fmt.Fprintf(os.Stderr, "  ccx refs --plain      PR/Jira refs with resolved status (tab-separated)\n")
	fmt.Fprintf(os.Stderr, "  ccx files             Interactive file picker\n")
	fmt.Fprintf(os.Stderr, "  ccx changes           Interactive changed-files picker\n")
	fmt.Fprintf(os.Stderr, "  ccx images            Interactive image picker\n")
	fmt.Fprintf(os.Stderr, "  ccx conversation      Interactive conversation picker\n")
	fmt.Fprintf(os.Stderr, "  ccx info              Show current matched session metadata\n")
	fmt.Fprintf(os.Stderr, "  ccx move <new-path>             Move current session's project path\n")
	fmt.Fprintf(os.Stderr, "  ccx move --session <id> <new>  Move a session by ID\n")
	fmt.Fprintf(os.Stderr, "  ccx move --from <dir> <new>     Move a project dir by path (no session ID needed)\n")
	fmt.Fprintf(os.Stderr, "  ccx config view       Print ~/.config/ccx/config.yaml\n")
	fmt.Fprintf(os.Stderr, "  ccx config edit       Open config in $EDITOR\n")
	fmt.Fprintf(os.Stderr, "  ccx config set remote.pod_name ccx-worker\n\n")
	fmt.Fprintf(os.Stderr, "Picker keys:\n")
	fmt.Fprintf(os.Stderr, "  ↵ enter    Jump to message in full ccx TUI\n")
	fmt.Fprintf(os.Stderr, "  o          Open URL in browser\n")
	fmt.Fprintf(os.Stderr, "  e          Open file/image in $EDITOR\n")
	fmt.Fprintf(os.Stderr, "  y          Copy to clipboard\n")
	fmt.Fprintf(os.Stderr, "  space      Toggle multi-select\n")
	fmt.Fprintf(os.Stderr, "  a          Select all visible items\n")
	fmt.Fprintf(os.Stderr, "  A          Deselect all\n")
	fmt.Fprintf(os.Stderr, "  /          Search filter\n")
	fmt.Fprintf(os.Stderr, "  R          Refresh (re-read the session)\n")
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
		// Items arrive newest-first; append the timestamp as a trailing tab
		// column so existing 3-column parsers keep working.
		if !item.Timestamp.IsZero() {
			fmt.Fprintf(os.Stdout, "%s\t%s\t%s\t%s\n", cat, item.Label, item.URL, item.Timestamp.Format("2006-01-02 15:04:05"))
		} else {
			fmt.Fprintf(os.Stdout, "%s\t%s\t%s\n", cat, item.Label, item.URL)
		}
	}
	return nil
}

func printRefs(filePath string) error {
	refs := session.ExtractSessionRefsFromFile(filePath)
	if len(refs) == 0 {
		return fmt.Errorf("no PR/Jira references found in session")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	refs = session.ResolveRefs(ctx, refs)
	for _, r := range refs {
		kind := strings.ToUpper(string(r.Kind))
		if len(kind) < 4 {
			kind += strings.Repeat(" ", 4-len(kind))
		}
		status := session.RefStatusText(r)
		fmt.Fprintf(os.Stdout, "%s\t%s\t%s\t%s\n", kind, r.Label, status, r.URL)
	}
	return nil
}

func printChanges(items []extract.ChangeItem) error {
	if len(items) == 0 {
		return fmt.Errorf("no changes found in session")
	}
	for _, item := range items {
		cat := ""
		if len(item.ToolNames) > 0 {
			cat = strings.ToUpper(item.ToolNames[0])
		}
		if len(cat) < 5 {
			cat += strings.Repeat(" ", 5-len(cat))
		}
		fmt.Fprintf(os.Stdout, "%s\t%s\t%s\n", cat, item.Item.Label, item.Summary)
	}
	return nil
}

func printConversation(items []PickerItem) error {
	if len(items) == 0 {
		return fmt.Errorf("no conversation entries found in session")
	}
	for _, item := range items {
		ref := item.FirstRef()
		role := strings.ToUpper(ref.Role)
		if len(role) < 5 {
			role += strings.Repeat(" ", 5-len(role))
		}
		fmt.Fprintf(os.Stdout, "%s\t%s\t%s\n", role, item.Item.Label, ref.EntryUUID)
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

// runSessions lists sessions sorted by modification time (most recent first).
// By default, filters to sessions matching the current tmux window's project paths.
// With --all, lists every session.
// Output: ID  SHORT_ID  MODIFIED  MSGS  PROJECT  PROMPT
func RunSessions(claudeDir string, all bool) error {
	allSessions := session.LoadCachedSessions(claudeDir)
	if len(allSessions) == 0 {
		var err error
		allSessions, err = session.ScanSessions(claudeDir)
		if err != nil {
			return fmt.Errorf("no sessions found: %w", err)
		}
	}
	if len(allSessions) == 0 {
		return fmt.Errorf("no sessions found")
	}

	// Mark live status before filtering
	tmux.MarkLiveSessions(allSessions)

	var sessions []session.Session
	if all {
		sessions = allSessions
	} else {
		// Default: only live sessions in the current tmux window
		projPaths := tmux.CurrentWindowClaudes()
		if len(projPaths) == 0 {
			live := clauderegistry.CwdSet()
			for p := range live {
				projPaths = append(projPaths, p)
			}
		}
		if len(projPaths) == 0 {
			return fmt.Errorf("no Claude session found in current window (use --all for all sessions)")
		}
		absSet := make(map[string]bool)
		for _, p := range projPaths {
			abs, _ := filepath.Abs(p)
			if abs != "" {
				absSet[abs] = true
			}
			absSet[p] = true
		}
		for _, s := range allSessions {
			if !s.IsLive {
				continue
			}
			abs, _ := filepath.Abs(s.ProjectPath)
			if absSet[s.ProjectPath] || (abs != "" && absSet[abs]) {
				sessions = append(sessions, s)
			}
		}
		if len(sessions) == 0 {
			return fmt.Errorf("no live sessions in current window (use --all for all sessions)")
		}
	}

	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].ModTime.After(sessions[j].ModTime)
	})

	for _, s := range sessions {
		prompt := s.FirstPrompt
		if len(prompt) > 80 {
			prompt = prompt[:80] + "…"
		}
		prompt = strings.ReplaceAll(prompt, "\n", " ")
		prompt = strings.ReplaceAll(prompt, "\t", " ")
		fmt.Fprintf(os.Stdout, "%s\t%s\t%s\t%d\t%s\t%s\n",
			s.ID, s.ShortID, s.ModTime.Format("2006-01-02 15:04"), s.MsgCount, s.ProjectName, prompt)
	}
	return nil
}

// RunMove moves a project's session directory to newPath, taking every
// session under it along. oldDir, if set, is used directly. Otherwise
// sessionID resolves to its project dir; if that's empty too, the session
// is resolved the same way other subcommands do (tmux window / live
// registry match).
func RunMove(claudeDir, sessionID, oldDir, newPath string) error {
	newPath = strings.TrimSpace(newPath)
	if newPath == "" {
		return fmt.Errorf("usage: ccx move [--from <dir> | --session <id>] <new-path>")
	}
	abs, err := filepath.Abs(newPath)
	if err != nil {
		return fmt.Errorf("resolve new path: %w", err)
	}
	newPath = abs

	oldPath := strings.TrimSpace(oldDir)
	if oldPath != "" {
		abs, err := filepath.Abs(oldPath)
		if err != nil {
			return fmt.Errorf("resolve old path: %w", err)
		}
		oldPath = abs
	} else if sessionID != "" {
		sess, ok := session.FindSessionByID(claudeDir, sessionID)
		if !ok {
			return fmt.Errorf("session %s not found", sessionID)
		}
		oldPath = sess.ProjectPath
	} else {
		_, sessID, err := findSessionFile(claudeDir)
		if err != nil {
			return err
		}
		sess, ok := session.FindSessionByID(claudeDir, sessID)
		if !ok {
			return fmt.Errorf("session %s not found", sessID)
		}
		oldPath = sess.ProjectPath
	}

	if oldPath == newPath {
		return fmt.Errorf("new path is the same as the current path: %s", oldPath)
	}
	if err := session.MoveProject(oldPath, newPath); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "%s -> %s\n", oldPath, newPath)
	return nil
}

func RunInfo(claudeDir string) error {
	_, sessID, err := findSessionFile(claudeDir)
	if err != nil {
		return err
	}
	sess, ok := session.FindSessionByID(claudeDir, sessID)
	if !ok {
		return fmt.Errorf("session %s not found", sessID)
	}
	fmt.Fprintf(os.Stdout, "id\t%s\n", sess.ID)
	fmt.Fprintf(os.Stdout, "short_id\t%s\n", sess.ShortID)
	fmt.Fprintf(os.Stdout, "project\t%s\n", sess.ProjectName)
	fmt.Fprintf(os.Stdout, "project_path\t%s\n", sess.ProjectPath)
	fmt.Fprintf(os.Stdout, "transcript\t%s\n", sess.FilePath)
	fmt.Fprintf(os.Stdout, "modified\t%s\n", sess.ModTime.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(os.Stdout, "messages\t%d\n", sess.MsgCount)
	if sess.GitBranch != "" {
		fmt.Fprintf(os.Stdout, "git_branch\t%s\n", sess.GitBranch)
	}
	if sess.FirstPrompt != "" {
		prompt := strings.ReplaceAll(sess.FirstPrompt, "\n", " ")
		fmt.Fprintf(os.Stdout, "first_prompt\t%s\n", prompt)
	}
	return nil
}

// findSessionFile detects Claude sessions in the same tmux window.
// If multiple sessions are found, prompts the user to choose one.
func findSessionFile(claudeDir string) (string, string, error) {
	projPaths := tmux.CurrentWindowClaudes()
	if len(projPaths) == 0 {
		live := clauderegistry.CwdSet()
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
