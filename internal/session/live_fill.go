package session

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sendbird/ccx/internal/clauderegistry"
)

// MergeLiveSessions returns sessions plus any session that Claude's live
// registry knows about but the given list is missing.
//
// Why this is needed: the fast paths that feed the session list
// (LoadCachedSessions, and the cache hits inside ScanSessions) are keyed by
// file modtime, and the cache file is only rewritten by a full ScanSessions —
// which costs ~30s on a large ~/.claude. So a session started after the last
// full scan is invisible to `ccx sessions`, `ccx urls`, and friends, even
// though it is running right now in the pane next door. The registry is
// authoritative about liveness, so anything it lists must appear.
//
// Resolution is by session ID, not by project path: Claude stores each
// transcript at projects/<encoded cwd>/<sessionId>.jsonl, and multiple live
// sessions routinely share one project path (so "newest file in the project
// dir" would drop all but one). Only the missing entries are read, so the
// common case — nothing missing — costs one registry read.
func MergeLiveSessions(claudeDir string, sessions []Session) []Session {
	live, err := clauderegistry.Read()
	if err != nil || len(live) == 0 {
		return sessions
	}

	known := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		known[s.ID] = true
	}
	var missing []clauderegistry.LiveSession
	for _, l := range live {
		if !known[l.SessionID] {
			missing = append(missing, l)
		}
	}
	if len(missing) == 0 {
		return sessions
	}

	claudeDir = resolveClaudeDir(claudeDir)
	if claudeDir == "" {
		return sessions
	}
	home := filepath.Dir(claudeDir)
	projectsDir := filepath.Join(claudeDir, "projects")
	badgeStore := LoadBadges(claudeDir)

	var byIDIndex map[string]string // built lazily, only if a direct hit misses
	added := false
	for _, l := range missing {
		path := ""
		if l.CWD != "" {
			p := filepath.Join(projectsDir, EncodeProjectPath(l.CWD), l.SessionID+".jsonl")
			if _, err := os.Stat(p); err == nil {
				path = p
			}
		}
		if path == "" {
			// CWD didn't map to the transcript's directory (session moved,
			// or the cwd recorded in the registry differs from the one the
			// project dir was created under). Fall back to a single index
			// of every transcript filename.
			if byIDIndex == nil {
				byIDIndex = indexTranscriptPaths(projectsDir)
			}
			path = byIDIndex[l.SessionID]
		}
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		sess := scanSessionStream(path, info.ModTime(), home, badgeStore)
		if sess.MsgCount == 0 {
			continue
		}
		refreshSessionDerivedState(&sess, home)
		sessions = append(sessions, sess)
		added = true
	}

	if added {
		sort.Slice(sessions, func(i, j int) bool {
			return sessions[i].ModTime.After(sessions[j].ModTime)
		})
	}
	return sessions
}

// indexTranscriptPaths maps session ID → transcript path for every project.
// One ReadDir per project directory; no file contents are read.
func indexTranscriptPaths(projectsDir string) map[string]string {
	out := make(map[string]string)
	dirs, err := os.ReadDir(projectsDir)
	if err != nil {
		return out
	}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(projectsDir, d.Name()))
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".jsonl") || strings.HasPrefix(name, "agent-") {
				continue
			}
			id := strings.TrimSuffix(name, ".jsonl")
			if _, exists := out[id]; !exists {
				out[id] = filepath.Join(projectsDir, d.Name(), name)
			}
		}
	}
	return out
}

// resolveClaudeDir applies the same default chain the scanners use:
// explicit arg → $CLAUDE_CONFIG_DIR → ~/.claude.
func resolveClaudeDir(claudeDir string) string {
	if claudeDir != "" {
		return claudeDir
	}
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}
