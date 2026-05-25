// Package clauderegistry reads Claude Code's on-disk live-session registry
// at $CLAUDE_CONFIG_DIR/sessions/*.json. Each file is written by a running
// claude process and contains (pid, sessionId, cwd, status, ...). The
// directory is the source of truth for which sessions are live right now.
//
// Why files and not `claude agents --json`:
//   - reads are cheap (no process fork per refresh)
//   - watchable via fsnotify
//   - same data shape; --json is just the aggregated view
//
// `claude agents --json` remains a valid escape hatch if Claude Code ever
// changes the on-disk layout in a way we can't follow — revisit if Read()
// starts returning empty while pgrep -x claude has live PIDs.
package clauderegistry

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// LiveSession is one entry from the registry. Fields we don't need are
// dropped during unmarshal.
type LiveSession struct {
	PID        int    `json:"pid"`
	SessionID  string `json:"sessionId"`
	CWD        string `json:"cwd"`
	Status     string `json:"status"` // "busy" | "idle" | ...
	Kind       string `json:"kind"`   // "interactive" for normal sessions
	Entrypoint string `json:"entrypoint"`
	Name       string `json:"name"`
	UpdatedAt  int64  `json:"updatedAt"`
}

// Dir returns the registry directory honoring $CLAUDE_CONFIG_DIR, falling
// back to ~/.claude/sessions.
func Dir() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "sessions")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "sessions")
}

// Read returns every live interactive session known to Claude Code.
// Ghost entries (process gone) are filtered out. A missing or unreadable
// directory returns (nil, nil) — older Claude Code versions don't write
// this directory and we treat that as "registry unavailable", letting
// callers fall back to whatever path they used before.
func Read() ([]LiveSession, error) {
	dir := Dir()
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]LiveSession, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		s, ok := readOne(filepath.Join(dir, e.Name()))
		if !ok {
			continue
		}
		if s.Kind != "" && s.Kind != "interactive" {
			continue
		}
		if !processAlive(s.PID) {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// readOne parses a single registry file. Claude Code writes these files
// without an atomic rename, so a concurrent read can land mid-write and
// see truncated JSON. Retry a few times — the write window is microseconds.
func readOne(path string) (LiveSession, bool) {
	for range 3 {
		data, err := os.ReadFile(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return LiveSession{}, false
			}
			time.Sleep(5 * time.Millisecond)
			continue
		}
		var s LiveSession
		if err := json.Unmarshal(data, &s); err == nil && s.SessionID != "" {
			return s, true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return LiveSession{}, false
}

// processAlive returns true iff a process with this PID exists. kill(pid, 0)
// is the canonical liveness probe: it sends no signal but performs the
// existence + permission check.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
