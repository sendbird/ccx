package tmux

import (
	"os"
	"testing"

	"github.com/sendbird/ccx/internal/clauderegistry"
)

// Diagnostic probe against the machine's real tmux + process tree. Skipped
// unless CCX_PROBE_WINDOW is set (e.g. CCX_PROBE_WINDOW=local:5), so it never
// runs in CI or a normal `go test ./...`.
func TestProbeLiveWindowAttribution(t *testing.T) {
	target := os.Getenv("CCX_PROBE_WINDOW")
	if target == "" {
		t.Skip("set CCX_PROBE_WINDOW=<session>:<window> to probe a live tmux window")
	}
	sessionName, windowIdx, ok := splitTarget(target)
	if !ok {
		t.Fatalf("CCX_PROBE_WINDOW=%q, want <session>:<window>", target)
	}

	panes, err := ListPanes()
	if err != nil {
		t.Fatalf("ListPanes: %v", err)
	}
	live, err := clauderegistry.Read()
	if err != nil {
		t.Fatalf("registry: %v", err)
	}

	for _, p := range panes {
		if p.Session == sessionName && p.Window == windowIdx {
			t.Logf("pane %s.%s pid=%d cwd=%s", p.Window, p.Pane, p.PID, p.Path)
		}
	}
	ids := claudeSessionsInWindow(panes, live, sessionName, windowIdx, batchPPIDMap())
	t.Logf("attributed session IDs (%d): %v", len(ids), ids)
}

func splitTarget(s string) (session, window string, ok bool) {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			return s[:i], s[i+1:], i > 0 && i < len(s)-1
		}
	}
	return "", "", false
}
