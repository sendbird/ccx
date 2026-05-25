// Package clauderegistry reads Claude Code's on-disk live-session registry
// at $CLAUDE_CONFIG_DIR/sessions/<pid>.json — one file per top-level
// claude process, written at startup and mutated in place as state
// changes. CCX uses it to detect which sessions are alive and which are
// actively producing a turn.
//
// Only the fields ccx actually consumes are decoded below. The full
// schema, lifecycle, and quirks (status values, name vs custom-title,
// PID reuse, WSL leak) are documented in
// docs/claude-code/live-session-registry.md.
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

// Field values we actually branch on.
const (
	statusBusy       = "busy"        // actively processing a turn
	kindInteractive  = "interactive" // normal user session
)

// LiveSession is the subset of a registry entry that ccx consumes. The
// on-disk file has many more fields — see the docs.
//
// Status, in particular, may be absent on a file captured between
// registration and the first REPL state update. Empty Status is treated
// as "not responding".
type LiveSession struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	Status    string `json:"status,omitempty"`
	Kind      string `json:"kind,omitempty"`
}

// IsResponding reports whether the model is actively generating right
// now. Only "busy" counts: "shell" (REPL idle, background Bash still
// running) and "waiting" (blocked on user input) deliberately don't, or
// a session that leaves a long-running tool in the background would
// show a permanent responding badge.
func (s LiveSession) IsResponding() bool {
	return s.Status == statusBusy
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
// Ghost entries (process gone) are filtered out. A missing directory
// returns (nil, nil) — older Claude Code versions don't write this
// directory and we treat that as "registry unavailable".
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
		// Kind defaults to "interactive" when unset. Skip bg/daemon
		// variants — those aren't user sessions.
		if s.Kind != "" && s.Kind != kindInteractive {
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
// sends no signal but performs the existence + permission check.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// Cwds returns absolute project paths of every live registry entry,
// deduplicated and preserving the registry's enumeration order. Used by
// callers that only need "which project paths have a claude running"
// and don't care about pane attribution.
func Cwds() []string {
	live, err := Read()
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

// CwdSet is the set form of Cwds.
func CwdSet() map[string]bool {
	paths := Cwds()
	out := make(map[string]bool, len(paths))
	for _, p := range paths {
		out[p] = true
	}
	return out
}
