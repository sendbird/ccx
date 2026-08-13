package tui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
)

// writeSavedRemote drops one saved remote session into the temp HOME that
// TestMain set up. The host is deliberately unroutable so any synchronous ping
// would have to wait out its connect timeout.
func writeSavedRemote(t *testing.T) {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".config", "ccx")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "" +
		"- pod_name: ssh-dead-host\n" +
		"  transport: ssh\n" +
		"  host: ccx-test-unreachable.invalid\n" +
		"  local_dir: /tmp/repo-remote\n" +
		"  status: unreachable\n"
	path := filepath.Join(dir, "remote-sessions.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Cleanup(func() { os.Remove(path) })
}

func TestNewAppDoesNotPingRemotesOnStartup(t *testing.T) {
	// A saved remote whose host is gone used to cost the full SSH
	// ConnectTimeout (~2.5s) inside NewApp, before ccx painted anything.
	writeSavedRemote(t)

	start := time.Now()
	app := NewApp([]session.Session{
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: time.Now()},
	}, Config{})
	elapsed := time.Since(start)

	// Generous bound: the point is "no network round-trip", not a benchmark.
	if elapsed > time.Second {
		t.Fatalf("NewApp took %v — it is pinging remotes on the startup path", elapsed)
	}
	// The saved remote must still show up as a row; only the liveness check moved.
	if !app.hasRemoteSessions() {
		t.Fatal("expected the saved remote session to be restored as a virtual row")
	}
}

func TestInitDispatchesRemoteCleanupWhenRemotesExist(t *testing.T) {
	writeSavedRemote(t)
	app := NewApp([]session.Session{
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: time.Now()},
	}, Config{})

	cmd := app.Init()
	if cmd == nil {
		t.Fatal("expected Init to return commands")
	}
	// Assert the command was *composed*, not that it ran: executing it would
	// make a real SSH connection.
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("expected a batch of startup commands, got %T", cmd())
	}
	if len(batch) < 2 {
		t.Fatalf("expected the remote sweep alongside the scan, got %d commands", len(batch))
	}
}

func TestRemotesCleanedDropsVanishedRows(t *testing.T) {
	writeSavedRemote(t)
	app := NewApp([]session.Session{
		{ID: "a1", ShortID: "a1", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: time.Now()},
	}, Config{})
	m, _ := app.Update(tea.WindowSizeMsg{Width: 160, Height: 50})
	app = m.(*App)
	if !app.hasRemoteSessions() {
		t.Fatal("expected the remote row before the sweep")
	}

	// The sweep found the remote gone and rewrote the saved set.
	os.Remove(filepath.Join(os.Getenv("HOME"), ".config", "ccx", "remote-sessions.yaml"))
	m, _ = app.Update(remotesCleanedMsg{changed: true})
	app = m.(*App)

	if app.hasRemoteSessions() {
		t.Fatal("expected the vanished remote's row to be dropped")
	}
	for _, s := range app.sessions {
		if s.ID == "a1" {
			return
		}
	}
	t.Fatal("expected local sessions to survive the sweep")
}
