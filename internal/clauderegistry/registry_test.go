package clauderegistry

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// withDir points CLAUDE_CONFIG_DIR at a fresh tempdir. Returns the
// "sessions" subdirectory where files belong.
func withDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", root)
	sessDir := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return sessDir
}

func TestRead_WellFormed(t *testing.T) {
	dir := withDir(t)
	self := os.Getpid()
	writeFile(t, dir, "1.json",
		`{"pid":`+itoa(self)+`,"sessionId":"abc","cwd":"/x","status":"busy","kind":"interactive"}`)
	got, err := Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SessionID != "abc" || got[0].Status != "busy" {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestRead_FiltersGhost(t *testing.T) {
	dir := withDir(t)
	// PID 1 is init — exists. Use an obviously dead PID instead: pick a
	// huge number unlikely to be allocated.
	writeFile(t, dir, "ghost.json",
		`{"pid":2147480000,"sessionId":"dead","cwd":"/x","kind":"interactive"}`)
	got, err := Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected ghost filtered, got %+v", got)
	}
}

func TestRead_FiltersNonInteractive(t *testing.T) {
	dir := withDir(t)
	self := os.Getpid()
	writeFile(t, dir, "p.json",
		`{"pid":`+itoa(self)+`,"sessionId":"sub","kind":"print"}`)
	got, err := Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected non-interactive filtered, got %+v", got)
	}
}

func TestRead_MissingDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "nope"))
	got, err := Read()
	if err != nil {
		t.Fatalf("missing dir should not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %+v", got)
	}
}

func TestRead_SkipsCorruptFile(t *testing.T) {
	dir := withDir(t)
	self := os.Getpid()
	// One good, one truncated mid-write. Bad file should be skipped, good
	// one returned.
	writeFile(t, dir, "good.json",
		`{"pid":`+itoa(self)+`,"sessionId":"ok","kind":"interactive"}`)
	writeFile(t, dir, "bad.json", `{"pid":`+itoa(self)+`,"session`)
	got, err := Read()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SessionID != "ok" {
		t.Fatalf("expected only good entry, got %+v", got)
	}
}

// itoa avoids pulling strconv into the test for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
