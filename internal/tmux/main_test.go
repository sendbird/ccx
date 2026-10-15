package tmux

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	tk "github.com/keyolk/tuikit/tmux"
)

// TestMain points the package at a Fake tmux with no script and an empty
// process table, so no test can reach the real tmux server or ps — even when
// the suite runs inside tmux. Tests that need answers install their own.
func TestMain(m *testing.M) {
	SetClient(tk.Client{Runner: tk.NewFake()})
	SetProcSource(func(context.Context) ([]byte, error) { return nil, nil })
	os.Exit(m.Run())
}

// withFake installs f (and, when non-nil, a canned ps table) for one test.
func withFake(t *testing.T, f *tk.Fake, ps string) {
	t.Helper()
	SetClient(tk.Client{Runner: f})
	SetProcSource(func(context.Context) ([]byte, error) { return []byte(ps), nil })
	InvalidateWindowClaudes()
	t.Cleanup(func() {
		SetClient(tk.Client{Runner: tk.NewFake()})
		SetProcSource(func(context.Context) ([]byte, error) { return nil, nil })
		InvalidateWindowClaudes()
	})
}

// paneRow is one list-panes row in tuikit's format: ten tab-separated fixed
// fields, then each free-text field as "<bytes>:<value>".
func paneRow(id, windowID string, windowIdx, paneIdx, pid int, session, windowName, cwd, cmd string) string {
	fixed := []string{id, windowID, "$1", fmt.Sprint(windowIdx), fmt.Sprint(paneIdx), fmt.Sprint(pid), "1", "1", "80", "24"}
	var b strings.Builder
	b.WriteString(strings.Join(fixed, "\t"))
	for _, f := range []string{session, windowName, cwd, cmd, ""} {
		fmt.Fprintf(&b, "\t%d:%s", len(f), f)
	}
	return b.String() + "\n"
}

// procsOf builds a process table from a pid -> ppid map.
func procsOf(ppid map[int]int) tk.Procs {
	p := tk.Procs{}
	for pid, parent := range ppid {
		p[pid] = tk.Proc{PID: pid, PPID: parent}
	}
	return p
}
