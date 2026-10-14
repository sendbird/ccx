package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/keyolk/tuikit/prefs"
)

// withUIStateDir turns ui.json persistence on over private config and state
// dirs, and returns the config.yaml path.
func withUIStateDir(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	persistUIState = true
	t.Cleanup(func() { persistUIState = false })
	cfg := configPath()
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// quitAndWait runs quit's command and feeds the result back, as Bubble Tea
// would; it returns the command Update answered with.
func quitAndWait(t *testing.T, a *App) tea.Cmd {
	t.Helper()
	_, cmd := a.quit()
	if cmd == nil {
		t.Fatal("quit returned no command")
	}
	msg := cmd()
	saved, ok := msg.(prefs.Saved)
	if !ok || !saved.Final || saved.Err != nil {
		t.Fatalf("quit flush = %#v", msg)
	}
	_, next := a.Update(saved)
	return next
}

// The first run after the move reads the state an older build left in
// config.yaml; quitting writes it to ui.json and leaves only settings in
// config.yaml.
func TestUIStateMigratesFromConfigYAML(t *testing.T) {
	cfg := withUIStateDir(t)
	if err := os.WriteFile(cfg, []byte("preferences:\n  group_mode: flat\n  worktree_dir: .wt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := NewApp(fakeSessions(), Config{})
	if a.sessGroupMode != groupFlat {
		t.Fatalf("group mode from config.yaml not restored: %d", a.sessGroupMode)
	}
	if next := quitAndWait(t, a); next == nil || next() != tea.Quit() {
		t.Fatal("Final save did not quit")
	}
	data, err := os.ReadFile(prefs.Path(prefsApp))
	if err != nil || !strings.Contains(string(data), `"group_mode": "flat"`) {
		t.Fatalf("ui.json = %q, %v", data, err)
	}
	conf, _ := os.ReadFile(cfg)
	if strings.Contains(string(conf), "group_mode") || !strings.Contains(string(conf), "worktree_dir: .wt") {
		t.Fatalf("config.yaml after quit:\n%s", conf)
	}
}

// ui.json wins over a stale config.yaml value.
func TestUIStateFileWinsOverConfig(t *testing.T) {
	cfg := withUIStateDir(t)
	if err := os.WriteFile(cfg, []byte("preferences:\n  group_mode: flat\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := prefs.Path(prefsApp)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"group_mode": "tree"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if a := NewApp(fakeSessions(), Config{}); a.sessGroupMode != groupTree {
		t.Fatalf("group mode = %d, want tree from ui.json", a.sessGroupMode)
	}
}

// A change made while running is saved (debounced) without waiting for quit,
// and an unchanged frame schedules nothing.
func TestUIStateSavesOnChange(t *testing.T) {
	withUIStateDir(t)
	a := NewApp(fakeSessions(), Config{})
	if cmd := a.saveUIState(); cmd != nil {
		t.Fatal("unchanged state scheduled a save")
	}
	a.splitRatio = 40
	if cmd := a.saveUIState(); cmd == nil {
		t.Fatal("changed state scheduled no save")
	}
	if !a.uiSaver.Pending() {
		t.Fatal("saver not pending after a change")
	}
}

// Pick mode never writes state.
func TestPickModeQuitsWithoutSaving(t *testing.T) {
	withUIStateDir(t)
	a := NewApp(fakeSessions(), Config{PickMode: true})
	_, cmd := a.quit()
	if cmd == nil || cmd() != tea.Quit() {
		t.Fatal("pick mode quit is not immediate")
	}
	if _, err := os.Stat(prefs.Path(prefsApp)); !os.IsNotExist(err) {
		t.Fatalf("pick mode wrote ui.json: %v", err)
	}
}
