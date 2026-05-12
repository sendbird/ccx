package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

// MarkLiveSessions sets IsLive and IsResponding on sessions by matching
// running Claude processes. In tmux, matches by session ID in process args
// with fallback to most-recent-for-path. Outside tmux, matches by path only.
// Also sets TmuxWindowName on all sessions whose ProjectPath matches a tmux pane CWD.
func MarkLiveSessions(sessions []session.Session) {
	if InTmux() {
		markLiveSessionsTmux(sessions)
	} else {
		markLiveSessionsNonTmux(sessions)
	}
	// Set IsResponding for live sessions
	for i := range sessions {
		if sessions[i].IsLive {
			info, err := os.Stat(sessions[i].FilePath)
			if err == nil {
				sessions[i].IsResponding = time.Since(info.ModTime()) < 10*time.Second
			}
		}
	}
}

func markLiveSessionsTmux(sessions []session.Session) {
	panes, err := ListPanes()
	if err != nil || len(panes) == 0 {
		return
	}

	currentKey := CurrentWindowKey() // "session|window" for the current ccx pane

	// Group session indices by ProjectPath
	pathIdx := map[string][]int{}
	for i, s := range sessions {
		pathIdx[s.ProjectPath] = append(pathIdx[s.ProjectPath], i)
	}

	// Set TmuxWindowName for ALL sessions by matching ProjectPath to pane CWD.
	// Track which absolute paths appear in the current tmux window so even
	// sessions without a live process can be pinned when their project path
	// matches a pane in this window (e.g. shell-only panes).
	pathWindow := make(map[string]string, len(panes))
	currentWindowPaths := make(map[string]bool)
	for _, p := range panes {
		absPath, _ := filepath.Abs(p.Path)
		if absPath == "" {
			continue
		}
		if p.WindowName != "" {
			if _, exists := pathWindow[absPath]; !exists {
				pathWindow[absPath] = p.WindowName
			}
		}
		if currentKey != "" && p.Session+"|"+p.Window == currentKey {
			currentWindowPaths[absPath] = true
		}
	}
	for i := range sessions {
		if wn, ok := pathWindow[sessions[i].ProjectPath]; ok && sessions[i].TmuxWindowName == "" {
			sessions[i].TmuxWindowName = wn
		}
	}

	// Batch pgrep: find all claude processes with their PIDs, PPIDs, and args
	allProcs := batchFindClaudeProcsAll()
	if len(allProcs) == 0 {
		return
	}

	// Walk every claude process up its PPID chain to find which tmux pane
	// shell — if any — owns it. This handles claudes that aren't direct
	// children of a pane shell (e.g. wrapped by ccproxy/teen/sudo). Subagents
	// (claude under another claude) and true orphans (no pane in the ancestor
	// chain) are filtered out of pane attribution.
	panePIDs := make(map[int]bool, len(panes))
	for _, p := range panes {
		panePIDs[p.PID] = true
	}
	ppidOf := batchPPIDMap()
	directByPaneShell, orphaned := classifyClaudeProcsByAncestry(allProcs, panePIDs, ppidOf)

	// Build pane PID → claude args map. Only panes whose shell owns a real
	// claude (per the PPID walk) get a cps entry.
	type claudeMatch struct {
		args          string
		windowName    string
		path          string
		currentWindow bool
	}
	var cps []claudeMatch
	for _, p := range panes {
		if args, ok := directByPaneShell[p.PID]; ok {
			absPath, _ := filepath.Abs(p.Path)
			if absPath != "" {
				inCur := currentKey != "" && p.Session+"|"+p.Window == currentKey
				cps = append(cps, claudeMatch{args: args, windowName: p.WindowName, path: absPath, currentWindow: inCur})
			}
		}
	}

	// True orphans (no tmux pane in their ancestry) are still alive but don't
	// belong to any visible window. Mark them LIVE via their cwd, but never
	// mark them as belonging to the current window — even when their cwd
	// matches a pane in this window, the orphan itself isn't in this window.
	if len(orphaned) > 0 {
		_ = currentWindowPaths // retained for documentation; orphans deliberately ignore it
		orphanCwds := resolveOrphanCwds(orphaned)
		for _, oc := range orphanCwds {
			cps = append(cps, claudeMatch{args: oc.args, path: oc.cwd, currentWindow: false})
		}
	}

	matched := make([]bool, len(cps))
	for ci, cp := range cps {
		for _, si := range pathIdx[cp.path] {
			if sessions[si].IsLive {
				continue
			}
			if strings.Contains(cp.args, sessions[si].ID) {
				sessions[si].IsLive = true
				sessions[si].TmuxWindowName = cp.windowName
				if cp.currentWindow {
					sessions[si].IsCurrentWindow = true
				}
				matched[ci] = true
				break
			}
		}
	}

	// Fallback: unmatched panes → most recently modified session for that path
	for ci, cp := range cps {
		if matched[ci] {
			continue
		}
		bestIdx := -1
		for _, si := range pathIdx[cp.path] {
			if sessions[si].IsLive {
				continue
			}
			if bestIdx == -1 || sessions[si].ModTime.After(sessions[bestIdx].ModTime) {
				bestIdx = si
			}
		}
		if bestIdx >= 0 {
			sessions[bestIdx].IsLive = true
			sessions[bestIdx].TmuxWindowName = cp.windowName
			if cp.currentWindow {
				sessions[bestIdx].IsCurrentWindow = true
			}
		}
	}
}

// ClaudeProc holds information about a running claude process.
type ClaudeProc struct {
	PID  int
	PPID int
	Args string
}

// classifyClaudeProcsByAncestry walks each claude process up its PPID chain
// using ppidOf and classifies it as:
//   - direct: the chain reaches a tmux pane shell PID without first passing
//     through another claude → return value `directByPaneShell` maps that
//     pane shell PID → claude args.
//   - subagent: the chain passes through another claude before reaching a
//     pane shell → silently dropped (the parent claude already attributes
//     the work to its own session).
//   - true orphan: the chain ends at init (PPID 0 / 1) or a dead process
//     without ever reaching a pane shell → returned in `orphaned`.
//
// The PPID walk is bounded by len(ppidOf) iterations to defend against
// cycles in a corrupt process map. When ppidOf is empty (lookup unavailable)
// the function falls back to the immediate PPID — only direct pane children
// and direct subagents are recognised.
func classifyClaudeProcsByAncestry(procs []ClaudeProc, panePIDs map[int]bool, ppidOf map[int]int) (directByPaneShell map[int]string, orphaned []ClaudeProc) {
	claudePIDs := make(map[int]bool, len(procs))
	for _, cp := range procs {
		claudePIDs[cp.PID] = true
	}
	directByPaneShell = make(map[int]string)

	walkOwningPane := func(startPID int) (paneShellPID int, isSubagent bool) {
		cur := startPID
		if ppid, ok := ppidOf[cur]; ok {
			cur = ppid
		} else {
			// No process tree available; only direct parent is known via procs.
			for _, cp := range procs {
				if cp.PID == startPID {
					cur = cp.PPID
					break
				}
			}
		}
		// Bound the walk so a corrupt cycle can't hang us.
		steps := len(ppidOf) + len(procs) + 4
		for i := 0; i < steps && cur > 1; i++ {
			if cur != startPID && claudePIDs[cur] {
				return 0, true
			}
			if panePIDs[cur] {
				return cur, false
			}
			next, ok := ppidOf[cur]
			if !ok {
				return 0, false
			}
			cur = next
		}
		return 0, false
	}

	for _, cp := range procs {
		paneShell, sub := walkOwningPane(cp.PID)
		if sub {
			continue
		}
		if paneShell != 0 {
			directByPaneShell[paneShell] = cp.Args
			continue
		}
		orphaned = append(orphaned, cp)
	}
	return directByPaneShell, orphaned
}

// classifyClaudeProcs partitions claude processes by their immediate PPID.
// This is the cheap pre-walk classification kept for backwards compatibility
// with callers that only inspect direct parent relationships. New code should
// prefer classifyClaudeProcsByAncestry.
func classifyClaudeProcs(procs []ClaudeProc, panePIDs map[int]bool) (directByPPID map[int]string, orphaned []ClaudeProc) {
	claudePIDs := make(map[int]bool, len(procs))
	for _, cp := range procs {
		claudePIDs[cp.PID] = true
	}
	directByPPID = make(map[int]string)
	for _, cp := range procs {
		if panePIDs[cp.PPID] {
			directByPPID[cp.PPID] = cp.Args
			continue
		}
		if claudePIDs[cp.PPID] {
			continue // subagent of another claude
		}
		orphaned = append(orphaned, cp)
	}
	return directByPPID, orphaned
}

// batchPPIDMap returns a pid → ppid map for every process visible to ps.
// Used by classifyClaudeProcsByAncestry to walk past intermediate wrappers
// such as ccproxy / tee / sudo when looking for the owning pane shell.
// Returns an empty map if the lookup fails — callers should still degrade
// gracefully (treating claudes as direct or orphan based on immediate PPID).
func batchPPIDMap() map[int]int {
	out, err := exec.Command("ps", "-e", "-o", "pid=,ppid=").Output()
	if err != nil {
		return map[int]int{}
	}
	m := make(map[int]int)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}
		pid, err1 := strconv.Atoi(fields[0])
		ppid, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			continue
		}
		m[pid] = ppid
	}
	return m
}

// BatchFindClaudeProcs finds all claude processes and maps parent PID → args.
// When multiple processes share ppid=1 (orphaned/reparented), they are stored
// in the OrphanedProcs slice instead to avoid map key collisions.
func BatchFindClaudeProcs() map[int]string {
	procs := batchFindClaudeProcsAll()
	result := make(map[int]string)
	for _, p := range procs {
		result[p.PPID] = p.Args
	}
	return result
}

// batchFindClaudeProcsAll returns all claude processes with pid, ppid, and args.
func batchFindClaudeProcsAll() []ClaudeProc {
	// Get all claude PIDs (exact binary name match to avoid matching ccx itself)
	pidOut, err := exec.Command("pgrep", "-x", "claude").Output()
	if err != nil {
		return nil
	}
	pids := strings.Fields(strings.TrimSpace(string(pidOut)))
	if len(pids) == 0 {
		return nil
	}

	// Single ps call to get ppid and args for all claude PIDs
	psArgs := []string{"-o", "pid=,ppid=,args=", "-p", strings.Join(pids, ",")}
	psOut, err := exec.Command("ps", psArgs...).Output()
	if err != nil {
		return nil
	}

	var result []ClaudeProc
	for line := range strings.SplitSeq(strings.TrimSpace(string(psOut)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Format: "  PID  PPID ARGS..."
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		args := strings.Join(fields[2:], " ")
		result = append(result, ClaudeProc{PID: pid, PPID: ppid, Args: args})
	}
	return result
}

// orphanCwd holds a resolved cwd and args for an orphaned claude process.
type orphanCwd struct {
	cwd  string
	args string
}

// resolveOrphanCwds uses lsof to find the cwd of orphaned claude processes.
func resolveOrphanCwds(procs []ClaudeProc) []orphanCwd {
	if len(procs) == 0 {
		return nil
	}
	pidStrs := make([]string, len(procs))
	for i, p := range procs {
		pidStrs[i] = strconv.Itoa(p.PID)
	}
	out, err := exec.Command("lsof", "-a", "-d", "cwd", "-Fpn", "-p", strings.Join(pidStrs, ",")).Output()
	if err != nil {
		return nil
	}

	// Parse lsof output: "p<pid>\nn<path>\n" pairs
	pidArgs := make(map[int]string, len(procs))
	for _, p := range procs {
		pidArgs[p.PID] = p.Args
	}

	var result []orphanCwd
	var currentPID int
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "p") {
			pid, err := strconv.Atoi(line[1:])
			if err == nil {
				currentPID = pid
			}
		} else if strings.HasPrefix(line, "n") && currentPID > 0 {
			path := strings.TrimSpace(line[1:])
			if path != "" {
				if args, ok := pidArgs[currentPID]; ok {
					result = append(result, orphanCwd{cwd: path, args: args})
				}
			}
		}
	}
	return result
}

func markLiveSessionsNonTmux(sessions []session.Session) {
	livePaths := FindLiveProjectPaths()
	// Match most recent session per live path
	bestForPath := map[string]int{}
	for i, s := range sessions {
		if !livePaths[s.ProjectPath] {
			continue
		}
		if prev, ok := bestForPath[s.ProjectPath]; !ok || s.ModTime.After(sessions[prev].ModTime) {
			bestForPath[s.ProjectPath] = i
		}
	}
	for _, idx := range bestForPath {
		sessions[idx].IsLive = true
	}
}

// DetectLiveProjectPaths returns absolute project paths of currently running
// Claude processes. Used for fast phase-1 session scanning at startup.
func DetectLiveProjectPaths() []string {
	if InTmux() {
		panes, err := ListPanes()
		if err != nil {
			return nil
		}
		allProcs := batchFindClaudeProcsAll()

		// Direct children: ppid matches a pane PID
		panePIDs := make(map[int]bool, len(panes))
		for _, p := range panes {
			panePIDs[p.PID] = true
		}
		directByPPID := make(map[int]string)
		var orphaned []ClaudeProc
		for _, cp := range allProcs {
			if panePIDs[cp.PPID] {
				directByPPID[cp.PPID] = cp.Args
			} else {
				orphaned = append(orphaned, cp)
			}
		}

		seen := make(map[string]bool)
		var paths []string
		for _, p := range panes {
			if _, ok := directByPPID[p.PID]; ok {
				absPath, _ := filepath.Abs(p.Path)
				if absPath != "" && !seen[absPath] {
					seen[absPath] = true
					paths = append(paths, absPath)
				}
			}
		}
		// Include orphaned process cwds
		for _, oc := range resolveOrphanCwds(orphaned) {
			if !seen[oc.cwd] {
				seen[oc.cwd] = true
				paths = append(paths, oc.cwd)
			}
		}
		return paths
	}
	live := FindLiveProjectPaths()
	paths := make([]string, 0, len(live))
	for p := range live {
		paths = append(paths, p)
	}
	return paths
}

// FindLiveProjectPaths returns project paths that have an active Claude process.
// Used as fallback for non-tmux environments.
func FindLiveProjectPaths() map[string]bool {
	live := make(map[string]bool)
	out, err := exec.Command("pgrep", "-x", "claude").Output()
	if err != nil {
		return live
	}
	pids := strings.Fields(strings.TrimSpace(string(out)))
	if len(pids) == 0 {
		return live
	}
	pidArg := strings.Join(pids, ",")
	out, err = exec.Command("lsof", "-a", "-d", "cwd", "-Fn", "-p", pidArg).Output()
	if err != nil {
		return live
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			path := strings.TrimSpace(line[1:])
			if path != "" {
				live[path] = true
			}
		}
	}
	return live
}
