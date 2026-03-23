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

	// Batch pgrep: find all claude processes and their parent PIDs in one call
	claudeProcs := BatchFindClaudeProcs()
	if len(claudeProcs) == 0 {
		return
	}

	// Build pane PID → claude args map
	type claudeMatch struct {
		args       string
		windowName string
		path       string
	}
	var cps []claudeMatch
	for _, p := range panes {
		if args, ok := claudeProcs[p.PID]; ok {
			absPath, _ := filepath.Abs(p.Path)
			if absPath != "" {
				cps = append(cps, claudeMatch{args: args, windowName: p.WindowName, path: absPath})
			}
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

// BatchFindClaudeProcs finds all claude processes and maps parent PID → args.
// Uses two commands total (pgrep + ps) instead of per-pane pgrep calls.
func BatchFindClaudeProcs() map[int]string {
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

	result := make(map[int]string)
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
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		args := strings.Join(fields[2:], " ")
		result[ppid] = args
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
		claudeProcs := BatchFindClaudeProcs()
		seen := make(map[string]bool)
		var paths []string
		for _, p := range panes {
			if _, ok := claudeProcs[p.PID]; ok {
				absPath, _ := filepath.Abs(p.Path)
				if absPath != "" && !seen[absPath] {
					seen[absPath] = true
					paths = append(paths, absPath)
				}
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
