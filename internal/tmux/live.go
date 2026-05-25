package tmux

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sendbird/ccx/internal/clauderegistry"
	"github.com/sendbird/ccx/internal/session"
)

// MarkLiveSessions sets IsLive, IsResponding, TmuxWindowName, and
// IsCurrentWindow on sessions by joining the local Claude registry
// (~/.claude/sessions/*.json) with the cached session list.
//
// Liveness comes from the registry: each running claude writes a file
// keyed by PID with its sessionId. We match by sessionId — no args
// parsing or most-recent-by-path fallback is needed because the
// registry is authoritative about which session the process is running.
//
// In tmux, we still need the process tree to attribute each live claude
// to a pane (for window name and current-window markers): walk PPID
// from the registry PID up to a pane PID.
func MarkLiveSessions(sessions []session.Session) {
	live, err := clauderegistry.Read()
	if err != nil {
		return
	}

	// Pin TmuxWindowName for every session whose ProjectPath matches a
	// pane CWD, independent of liveness. Shell-only panes still count.
	var panes []Pane
	var currentKey string
	var pathWindow map[string]string
	var currentWindowPaths map[string]bool
	if InTmux() {
		panes, _ = ListPanes()
		currentKey = CurrentWindowKey()
		pathWindow = make(map[string]string, len(panes))
		currentWindowPaths = make(map[string]bool)
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
	}

	if len(live) == 0 {
		return
	}

	bySessionID := make(map[string]clauderegistry.LiveSession, len(live))
	for _, l := range live {
		bySessionID[l.SessionID] = l
	}

	// Resolve each live entry's owning tmux pane (if any) so we can pin
	// TmuxWindowName/IsCurrentWindow even when the claude isn't a direct
	// pane child (wrapped by ccproxy/teen/sudo, or attributed via cwd).
	paneByLivePID := map[int]Pane{}
	if InTmux() && len(panes) > 0 {
		panePIDToPane := make(map[int]Pane, len(panes))
		panePIDs := make(map[int]bool, len(panes))
		for _, p := range panes {
			panePIDToPane[p.PID] = p
			panePIDs[p.PID] = true
		}
		ppidOf := batchPPIDMap()
		for _, l := range live {
			if shellPID := walkToPane(l.PID, panePIDs, ppidOf); shellPID != 0 {
				paneByLivePID[l.PID] = panePIDToPane[shellPID]
			}
		}
	}

	for i := range sessions {
		l, ok := bySessionID[sessions[i].ID]
		if !ok {
			continue
		}
		sessions[i].IsLive = true
		sessions[i].IsResponding = l.Status == "busy"
		if p, has := paneByLivePID[l.PID]; has {
			if p.WindowName != "" {
				sessions[i].TmuxWindowName = p.WindowName
			}
			if currentKey != "" && p.Session+"|"+p.Window == currentKey {
				sessions[i].IsCurrentWindow = true
			}
		}
	}
}

// walkToPane walks from startPID up the PPID chain to a tmux pane PID.
// Returns 0 if no pane is reached (orphaned/wrapped beyond the visible
// process tree). Bounded against cycles in a corrupt ppid map.
func walkToPane(startPID int, panePIDs map[int]bool, ppidOf map[int]int) int {
	cur := startPID
	steps := len(ppidOf) + 4
	for range steps {
		if cur <= 1 {
			return 0
		}
		if panePIDs[cur] {
			return cur
		}
		next, ok := ppidOf[cur]
		if !ok {
			return 0
		}
		cur = next
	}
	return 0
}

// DetectLiveProjectPaths returns absolute project paths of currently
// running claude processes. Used for fast phase-1 session scanning at
// startup and by callers that don't need full session attribution.
func DetectLiveProjectPaths() []string {
	live, err := clauderegistry.Read()
	if err != nil || len(live) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(live))
	paths := make([]string, 0, len(live))
	for _, l := range live {
		abs, _ := filepath.Abs(l.CWD)
		if abs == "" {
			abs = l.CWD
		}
		if abs == "" || seen[abs] {
			continue
		}
		seen[abs] = true
		paths = append(paths, abs)
	}
	return paths
}

// FindLiveProjectPaths returns project paths with an active Claude
// process as a set. Kept for callers that need set semantics.
func FindLiveProjectPaths() map[string]bool {
	live, err := clauderegistry.Read()
	if err != nil || len(live) == 0 {
		return map[string]bool{}
	}
	out := make(map[string]bool, len(live))
	for _, l := range live {
		abs, _ := filepath.Abs(l.CWD)
		if abs == "" {
			abs = l.CWD
		}
		if abs != "" {
			out[abs] = true
		}
	}
	return out
}

// batchPPIDMap returns a pid → ppid map for every process visible to ps.
// Used by walkToPane to skip past wrappers (ccproxy / tee / sudo) when
// resolving the pane that owns a live claude. Empty map on failure —
// callers degrade to "no pane attribution" rather than crashing.
func batchPPIDMap() map[int]int {
	out, err := exec.Command("ps", "-e", "-o", "pid=,ppid=").Output()
	if err != nil {
		return map[int]int{}
	}
	m := make(map[int]int)
	for line := range strings.SplitSeq(string(out), "\n") {
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
