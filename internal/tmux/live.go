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

	// Group session indices by ProjectPath
	pathIdx := map[string][]int{}
	for i, s := range sessions {
		pathIdx[s.ProjectPath] = append(pathIdx[s.ProjectPath], i)
	}

	// Set TmuxWindowName for ALL sessions by matching ProjectPath to pane CWD
	pathWindow := make(map[string]string, len(panes))
	for _, p := range panes {
		absPath, _ := filepath.Abs(p.Path)
		if absPath != "" && p.WindowName != "" {
			if _, exists := pathWindow[absPath]; !exists {
				pathWindow[absPath] = p.WindowName
			}
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

	// Separate direct-child procs (ppid matches a pane) from orphaned (ppid=1)
	panePIDs := make(map[int]bool, len(panes))
	for _, p := range panes {
		panePIDs[p.PID] = true
	}
	directByPPID := make(map[int]string)   // ppid → args (for pane-matched procs)
	var orphaned []ClaudeProc              // ppid=1 or ppid not matching any pane
	for _, cp := range allProcs {
		if panePIDs[cp.PPID] {
			directByPPID[cp.PPID] = cp.Args
		} else {
			orphaned = append(orphaned, cp)
		}
	}

	// Build pane PID → claude args map (direct children)
	type claudeMatch struct {
		args       string
		windowName string
		path       string
	}
	var cps []claudeMatch
	for _, p := range panes {
		if args, ok := directByPPID[p.PID]; ok {
			absPath, _ := filepath.Abs(p.Path)
			if absPath != "" {
				cps = append(cps, claudeMatch{args: args, windowName: p.WindowName, path: absPath})
			}
		}
	}

	// For orphaned claude procs, resolve their cwd via lsof and match to sessions
	if len(orphaned) > 0 {
		orphanCwds := resolveOrphanCwds(orphaned)
		for _, oc := range orphanCwds {
			cps = append(cps, claudeMatch{args: oc.args, path: oc.cwd})
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
		}
	}
}

// ClaudeProc holds information about a running claude process.
type ClaudeProc struct {
	PID  int
	PPID int
	Args string
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
