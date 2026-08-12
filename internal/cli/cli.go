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

	sessions, err := resolveWindowSessions(claudeDir)
	if err != nil {
		return nil, err
	}
	sources := pickerSources(sessions)

	interactive := !plain && isTerminal()

	if interactive {
		return runInteractive(command, sources)
	}
	return nil, runPlain(command, sources, claudeDir)
}

func runPlain(command string, sources []pickerSource, claudeDir string) error {
	switch command {
	case "urls":
		return printItems(collectExtracted(sources, extract.SessionURLs, itemStamp), "urls")
	case "refs":
		return printRefs(sources)
	case "files":
		return printItems(collectExtracted(sources, extract.SessionFilePaths, itemStamp), "files")
	case "changes":
		return printChanges(collectExtracted(sources, extract.SessionChanges, changeStamp))
	case "conversation":
		items, err := collectItems(command, sources)
		if err != nil {
			return err
		}
		return printConversation(items)
	case "images":
		return printImages(sources, claudeDir)
	default:
		return fmt.Errorf("unknown command: %s\nRun 'ccx help' for usage", command)
	}
}

func runInteractive(command string, sources []pickerSource) (*RunResult, error) {
	items, err := collectItems(command, sources)
	if err != nil {
		return nil, err
	}

	// Load the URL-opener config so the picker's open action honors
	// open.command_template, exactly like the TUI's open paths.
	openerCfg := loadOpenerConfig()
	// Hand the picker the context it needs to re-extract on `R` (refresh).
	ctx := pickerContext{command: command, sources: sources}
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
// user presses `R` (refresh): the subcommand and every transcript it reads.
type pickerContext struct {
	command string
	sources []pickerSource
}

// pickerSource is one transcript a subcommand reads, plus the compact label its
// items carry. Every subcommand aggregates all the sessions in the tmux window
// into one list rather than asking the user to pick one, so each row has to say
// where it came from.
type pickerSource struct {
	filePath string
	sessID   string
	label    string
}

// pickerSources pairs each session with its origin label. The label is the base
// name of the project directory — that is what distinguishes sibling worktrees,
// whereas ProjectName is the full shortened path and far too wide for a list
// row. Sessions that would collide on the base name keep their short ID.
func pickerSources(sessions []session.Session) []pickerSource {
	seen := make(map[string]int, len(sessions))
	for _, s := range sessions {
		seen[sourceBase(s)]++
	}
	out := make([]pickerSource, 0, len(sessions))
	for _, s := range sessions {
		label := sourceBase(s)
		if seen[label] > 1 {
			label += ":" + s.ShortID
		}
		out = append(out, pickerSource{filePath: s.FilePath, sessID: s.ID, label: label})
	}
	return out
}

func sourceBase(s session.Session) string {
	if s.ProjectPath != "" {
		if b := filepath.Base(s.ProjectPath); b != "" && b != "." && b != string(filepath.Separator) {
			return b
		}
	}
	if s.ProjectName != "" {
		return s.ProjectName
	}
	return s.ShortID
}

// collectItems builds the picker items for every session in the window, tagging
// each with its origin and merging them into one time-ordered list.
//
// Items are NOT deduplicated across sessions: the same PR touched by two
// sessions is two facts, and collapsing them would have to drop one session's
// jump target. Within a session the existing per-command dedup still applies.
//
// A transcript that fails to load is skipped rather than failing the whole
// command — one unreadable session should not hide the others. An error is
// returned only when nothing could be read at all.
func collectItems(command string, sources []pickerSource) ([]PickerItem, error) {
	if len(sources) == 0 {
		return nil, fmt.Errorf("no Claude session found in current window")
	}
	var all []PickerItem
	var firstErr error
	for _, src := range sources {
		items, err := extractItems(command, src.filePath, src.sessID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for i := range items {
			items[i].Source = src.label
		}
		all = append(all, items...)
	}
	if all == nil && firstErr != nil {
		return nil, firstErr
	}
	sortSourcedItems(command, all)
	return all, nil
}

// sortSourcedItems interleaves items from several sessions while keeping each
// command's single-session ordering: newest-first everywhere except
// conversation, which reads as a chronological transcript.
func sortSourcedItems(command string, items []PickerItem) {
	ascending := command == "conversation"
	sort.SliceStable(items, func(i, j int) bool {
		ti, tj := itemTime(items[i]), itemTime(items[j])
		if ti.Equal(tj) {
			return false
		}
		if ascending {
			return ti.Before(tj)
		}
		return ti.After(tj)
	})
}

// itemTime is the occurrence an aggregated list orders by: the stamp
// sortAndStampItems already computed, or the newest ref for the commands that
// do not stamp their items (conversation).
func itemTime(p PickerItem) time.Time {
	t := p.Item.Timestamp
	for _, r := range p.Refs {
		if r.Timestamp.After(t) {
			t = r.Timestamp
		}
	}
	return t
}

// sourced pairs a value from one session's transcript with that session's
// origin label, for the --plain paths that render extract types directly
// instead of going through PickerItem.
type sourced[T any] struct {
	val    T
	source string
}

// collectExtracted runs a per-transcript extractor over every session and
// merges the results newest-first, so --plain output interleaves sessions the
// same way the interactive list does.
func collectExtracted[T any](sources []pickerSource, list func(filePath string) []T, at func(T) time.Time) []sourced[T] {
	var out []sourced[T]
	for _, src := range sources {
		for _, v := range list(src.filePath) {
			out = append(out, sourced[T]{val: v, source: src.label})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return at(out[i].val).After(at(out[j].val))
	})
	return out
}

func itemStamp(i extract.Item) time.Time         { return i.Timestamp }
func changeStamp(c extract.ChangeItem) time.Time { return c.Timestamp }

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
	_, _, _, _, _, oc, _ := tui.LoadCCXConfig(configPath)
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
	fmt.Fprintf(os.Stderr, "Item lists cover every Claude session in the current tmux window;\n")
	fmt.Fprintf(os.Stderr, "each row shows its origin session (search it with session:<name>).\n")
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

func printItems(items []sourced[extract.Item], kind string) error {
	if len(items) == 0 {
		return fmt.Errorf("no %s found in session", kind)
	}
	for _, it := range items {
		item := it.val
		cat := strings.ToUpper(item.Category)
		if len(cat) < 5 {
			cat += strings.Repeat(" ", 5-len(cat))
		}
		// Items arrive newest-first; the timestamp and the origin session are
		// trailing tab columns so existing 3-column parsers keep working. The
		// timestamp column is always emitted (empty when unknown) so the source
		// stays in a fixed position.
		ts := ""
		if !item.Timestamp.IsZero() {
			ts = item.Timestamp.Format("2006-01-02 15:04:05")
		}
		fmt.Fprintf(os.Stdout, "%s\t%s\t%s\t%s\t%s\n", cat, item.Label, item.URL, ts, it.source)
	}
	return nil
}

// printRefs resolves and prints the PR/Jira references of every session in the
// window. Refs are ordered PRs-then-Jira and newest-first across sessions, the
// same ordering a single session produces. Status resolution is URL-keyed and
// cached, so a ref shared by two sessions costs one lookup.
func printRefs(sources []pickerSource) error {
	var all []sourced[session.SessionRef]
	for _, src := range sources {
		for _, r := range session.ExtractSessionRefsFromFile(src.filePath) {
			all = append(all, sourced[session.SessionRef]{val: r, source: src.label})
		}
	}
	if len(all) == 0 {
		return fmt.Errorf("no PR/Jira references found in session")
	}
	kindOrder := map[session.RefKind]int{session.RefPR: 0, session.RefJira: 1, session.RefArtifact: 2}
	sort.SliceStable(all, func(i, j int) bool {
		ki, kj := kindOrder[all[i].val.Kind], kindOrder[all[j].val.Kind]
		if ki != kj {
			return ki < kj
		}
		return all[i].val.FirstSeen.After(all[j].val.FirstSeen)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	for _, it := range all {
		r := session.ResolveRef(ctx, it.val)
		kind := strings.ToUpper(string(r.Kind))
		if len(kind) < 4 {
			kind += strings.Repeat(" ", 4-len(kind))
		}
		status := session.RefStatusText(r)
		fmt.Fprintf(os.Stdout, "%s\t%s\t%s\t%s\t%s\n", kind, r.Label, status, r.URL, it.source)
	}
	return nil
}

func printChanges(items []sourced[extract.ChangeItem]) error {
	if len(items) == 0 {
		return fmt.Errorf("no changes found in session")
	}
	for _, it := range items {
		item := it.val
		cat := ""
		if len(item.ToolNames) > 0 {
			cat = strings.ToUpper(item.ToolNames[0])
		}
		if len(cat) < 5 {
			cat += strings.Repeat(" ", 5-len(cat))
		}
		fmt.Fprintf(os.Stdout, "%s\t%s\t%s\t%s\n", cat, item.Item.Label, item.Summary, it.source)
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
		fmt.Fprintf(os.Stdout, "%s\t%s\t%s\t%s\n", role, item.Item.Label, ref.EntryUUID, item.Source)
	}
	return nil
}

func printImages(sources []pickerSource, claudeDir string) error {
	home, _ := os.UserHomeDir()
	found := 0
	for _, src := range sources {
		entries, err := session.LoadMessages(src.filePath)
		if err != nil {
			continue
		}
		for _, e := range entries {
			for _, b := range e.Content {
				if b.Type != "image" || b.ImagePasteID <= 0 {
					continue
				}
				p := session.ImageCachePath(home, src.sessID, b.ImagePasteID)
				if p == "" {
					p, _ = session.ExtractImageToTemp(home, src.filePath, src.sessID, b.ImagePasteID)
				}
				if p != "" {
					ts := ""
					if !e.Timestamp.IsZero() {
						ts = e.Timestamp.Format("15:04:05")
					}
					fmt.Fprintf(os.Stdout, "%d\t%s\t%s\t%s\n", b.ImagePasteID, ts, p, src.label)
					found++
				}
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
	// The cache is only rewritten by a full scan, so a session started since
	// then is missing here — exactly the sessions this command cares about.
	allSessions = session.MergeLiveSessions(claudeDir, allSessions)
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

// RunMove moves a session's project path to newPath. With --from, oldDir is
// used directly and the whole project directory (every session under it) is
// moved. Otherwise sessionID resolves to a specific session (defaulting to
// the current tmux window's session), and only that session's transcript
// (plus its subagent/scratchpad data) is moved, leaving sibling sessions
// under the old project path untouched.
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

		if oldPath == newPath {
			return fmt.Errorf("new path is the same as the current path: %s", oldPath)
		}
		if err := session.MoveProject(oldPath, newPath); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "%s -> %s\n", oldPath, newPath)
		return nil
	}

	var sess session.Session
	if sessionID != "" {
		s, ok := session.FindSessionByID(claudeDir, sessionID)
		if !ok {
			return fmt.Errorf("session %s not found", sessionID)
		}
		sess = s
	} else {
		_, sessID, err := findSessionFile(claudeDir)
		if err != nil {
			return err
		}
		s, ok := session.FindSessionByID(claudeDir, sessID)
		if !ok {
			return fmt.Errorf("session %s not found", sessID)
		}
		sess = s
	}

	if sess.ProjectPath == newPath {
		return fmt.Errorf("new path is the same as the current path: %s", sess.ProjectPath)
	}
	if err := session.MoveSession(sess.ProjectPath, newPath, sess.ID); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "%s -> %s\n", sess.ProjectPath, newPath)
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

// findSessionFile resolves exactly one session in the current tmux window,
// prompting when several match. Only `info` and `move` use it — they act on a
// single session by nature. The item-listing subcommands aggregate every session
// via resolveWindowSessions instead of making the user choose.
func findSessionFile(claudeDir string) (string, string, error) {
	matches, err := resolveWindowSessions(claudeDir)
	if err != nil {
		return "", "", err
	}
	if len(matches) == 1 {
		return matches[0].FilePath, matches[0].ID, nil
	}
	idx, err := promptSessionChoice(matches)
	if err != nil {
		return "", "", err
	}
	return matches[idx].FilePath, matches[idx].ID, nil
}

// resolveWindowSessions returns every Claude session running in this process's
// tmux window, most relevant first.
func resolveWindowSessions(claudeDir string) ([]session.Session, error) {
	// Prefer identity over location: the live registry knows exactly which
	// session each Claude process runs, so panes are attributed by process
	// ancestry. Path matching below cannot separate several panes of one window
	// that report the same cwd while running sessions from different projects.
	if matches := sessionsByID(claudeDir, tmux.CurrentWindowClaudeSessionIDs(liveSessions())); len(matches) > 0 {
		return matches, nil
	}

	projPaths := tmux.CurrentWindowClaudes()
	if len(projPaths) == 0 {
		live := clauderegistry.CwdSet()
		for p := range live {
			projPaths = append(projPaths, p)
		}
	}
	if len(projPaths) == 0 {
		return nil, fmt.Errorf("no Claude session found in current window")
	}

	cached := session.LoadCachedSessions(claudeDir)
	matches := matchSessionsForPaths(cached, projPaths, samePath)

	// The cache is only written by the TUI's full scan (session.ScanSessions),
	// so the CLI alone never refreshes it: a session started since the last TUI
	// run isn't in the cache at all. Scan the pane's project dirs directly.
	//
	// The result is NOT re-filtered by ProjectPath. ScanSessionsForPaths reads
	// the directory encoded from each pane path, so the directory itself is the
	// link — while the scanned ProjectPath is whatever the transcript's *last*
	// isMeta line carried, which for a worktree with a nested module flips
	// between the project and its subdirectory. Filtering on it would reproduce
	// the very miss this fallback exists to fix.
	if len(matches) == 0 {
		if fresh, err := session.ScanSessionsForPaths(claudeDir, projPaths); err == nil {
			matches = fresh
		}
	}

	// Last resort: the pane sits in a subdirectory of a project that has a
	// session (a nested module inside a worktree, say). Only this direction is
	// safe. Matching downward too would let a pane in a broad parent like
	// ~/src pick an arbitrary deep session under it, which is worse than
	// failing. This pass runs against the cache — the fresh scan reads only
	// the dir encoded from the pane path itself — and says so, since the
	// session isn't at the current path.
	if len(matches) == 0 {
		if matches = matchSessionsForPaths(cached, projPaths, paneUnderSession); len(matches) > 0 {
			nearest := make([]string, 0, len(matches))
			for _, m := range matches {
				nearest = append(nearest, m.ProjectPath)
			}
			fmt.Fprintf(os.Stderr, "ccx: no session at the current path; using nearest parent: %s\n",
				strings.Join(nearest, ", "))
		}
	}

	if len(matches) == 0 {
		return nil, fmt.Errorf("no session found matching project paths: %s", strings.Join(projPaths, ", "))
	}
	return matches, nil
}

// liveSessions returns the live Claude registry, or nil when it is unavailable
// (older Claude Code versions don't write it). Callers degrade to path matching.
func liveSessions() []clauderegistry.LiveSession {
	live, err := clauderegistry.Read()
	if err != nil {
		return nil
	}
	return live
}

// sessionsByID resolves session IDs to their transcripts, preserving the given
// order and skipping IDs with no readable session. The cache is consulted first
// because it already holds the parsed metadata; a session started since the last
// TUI scan is absent from it, so those fall back to a direct lookup on disk.
func sessionsByID(claudeDir string, ids []string) []session.Session {
	if len(ids) == 0 {
		return nil
	}
	byID := make(map[string]session.Session)
	for _, s := range session.LoadCachedSessions(claudeDir) {
		byID[s.ID] = s
	}
	out := make([]session.Session, 0, len(ids))
	for _, id := range ids {
		if s, ok := byID[id]; ok && s.FilePath != "" {
			out = append(out, s)
			continue
		}
		if s, ok := session.FindSessionByID(claudeDir, id); ok && s.FilePath != "" {
			out = append(out, s)
		}
	}
	return out
}

// matchSessionsForPaths returns the best session for each project path,
// deduplicated by session ID and ordered by projPaths. match decides whether a
// session's absolute project path counts as a hit for a pane's absolute path.
// Among hits, the fewest path segments between the two wins, and ties break on
// the most recent ModTime — so an exact match always beats a nearby one, and a
// direct parent beats a distant ancestor.
func matchSessionsForPaths(sessions []session.Session, projPaths []string, match func(sessPath, panePath string) bool) []session.Session {
	var matches []session.Session
	seen := make(map[string]bool)
	for _, projPath := range projPaths {
		absProj := absOrSelf(projPath)
		var best *session.Session
		bestDist := 0
		for i := range sessions {
			absSP := absOrSelf(sessions[i].ProjectPath)
			if !match(absSP, absProj) {
				continue
			}
			dist := pathDistance(absSP, absProj)
			if best == nil || dist < bestDist ||
				(dist == bestDist && sessions[i].ModTime.After(best.ModTime)) {
				best = &sessions[i]
				bestDist = dist
			}
		}
		if best != nil && !seen[best.ID] {
			seen[best.ID] = true
			matches = append(matches, *best)
		}
	}
	return matches
}

// pathDistance counts the path segments separating a from b when one contains
// the other (0 when equal). Unrelated paths get a large value so they always
// lose to a related one.
func pathDistance(a, b string) int {
	switch {
	case a == b:
		return 0
	case isUnder(a, b):
		return strings.Count(strings.TrimPrefix(a, b), string(filepath.Separator))
	case isUnder(b, a):
		return strings.Count(strings.TrimPrefix(b, a), string(filepath.Separator))
	default:
		return 1 << 30
	}
}

func absOrSelf(p string) string {
	abs, _ := filepath.Abs(p)
	if abs == "" {
		return p
	}
	return abs
}

func samePath(sessPath, panePath string) bool { return sessPath == panePath }

// paneUnderSession reports whether the pane sits inside the session's project
// directory — the nested-module case. Deliberately one-directional: see the
// call site for why the reverse is unsafe.
func paneUnderSession(sessPath, panePath string) bool {
	return isUnder(panePath, sessPath)
}

func isUnder(child, parent string) bool {
	if child == "" || parent == "" {
		return false
	}
	return strings.HasPrefix(child, strings.TrimSuffix(parent, string(filepath.Separator))+string(filepath.Separator))
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
