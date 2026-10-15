package tmux

import (
	"context"
	"os"
	"testing"

	tk "github.com/keyolk/tuikit/tmux"

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

	// Opt-in diagnostic: the one test allowed to talk to the real tmux.
	SetClient(tk.New(nil))
	SetProcSource(nil)
	t.Cleanup(func() {
		SetClient(tk.Client{Runner: tk.NewFake()})
		SetProcSource(func(context.Context) ([]byte, error) { return nil, nil })
	})
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
	ids := claudeSessionsInWindow(panes, live, sessionName, windowIdx, mustProcs(t))
	t.Logf("attributed session IDs (%d): %v", len(ids), ids)
}

func mustProcs(t *testing.T) tk.Procs {
	p, ok := loadProcs()
	if !ok {
		t.Fatal("ps snapshot failed")
	}
	return p
}

func splitTarget(s string) (session, window string, ok bool) {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			return s[:i], s[i+1:], i > 0 && i < len(s)-1
		}
	}
	return "", "", false
}
