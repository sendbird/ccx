package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHasSessionScratchpadDetection(t *testing.T) {
	root := t.TempDir()
	base := t.TempDir()
	restore := SetScratchpadBaseOverride(base)
	defer restore()

	sess := &Session{ID: "s1", ProjectPath: root}
	// No dir yet → false.
	if hasSessionScratchpad(sess.ProjectPath, sess.ID) {
		t.Fatal("hasSessionScratchpad should be false before dir is created")
	}
	// refreshSessionDerivedState should set HasScratchpad = false.
	refreshSessionDerivedState(sess, "")
	if sess.HasScratchpad {
		t.Fatal("HasScratchpad should be false before dir is created")
	}

	dir := filepath.Join(base, EncodeProjectPath(root), "s1", "scratchpad")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !hasSessionScratchpad(sess.ProjectPath, sess.ID) {
		t.Fatal("hasSessionScratchpad should be true after dir has files")
	}
	refreshSessionDerivedState(sess, "")
	if !sess.HasScratchpad {
		t.Fatal("HasScratchpad should be true after scratchpad dir has files")
	}

	// Empty project or session → false, no panic.
	if hasSessionScratchpad("", "s1") {
		t.Fatal("empty projectPath should yield false")
	}
	if hasSessionScratchpad(root, "") {
		t.Fatal("empty sessionID should yield false")
	}
}
