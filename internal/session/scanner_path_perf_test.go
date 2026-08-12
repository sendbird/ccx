package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A directory name that resolves nowhere must fail fast. The resolver tries
// three branches per part and two of them recurse without touching the
// filesystem first, so an unresolvable name expands a tree exponential in the
// part count — each node paying an os.Stat. A real 25-segment scratchpad path
// (/private/tmp/claude-502/-Users-gavin-jeong/<uuid>/scratchpad/bench/repos/…)
// burned 6.5s of syscalls here, which was the whole cost of `ccx sessions`.
func TestDecodeProjectPathFailsFastOnLongUnresolvableName(t *testing.T) {
	// 40 parts, none of which exist under "/".
	name := "-" + strings.Join(repeatParts("zzq", 40), "-")

	start := time.Now()
	got := decodeProjectPath(name)
	elapsed := time.Since(start)

	if got != "" {
		t.Fatalf("decodeProjectPath(%q) = %q, want empty (nothing on disk matches)", name, got)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("decodeProjectPath took %s on an unresolvable name — the search is not bounded", elapsed)
	}
}

// The node budget must not break resolution of paths that genuinely exist,
// including the hyphen- and dot-merging branches the search needs.
func TestDecodeProjectPathStillResolvesRealPaths(t *testing.T) {
	root := t.TempDir()
	// Build <root>/my-project/sub.dir so resolution needs both a literal
	// hyphen merge and a dot merge.
	target := filepath.Join(root, "my-project", "sub.dir")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}

	encoded := EncodeProjectPath(target)
	decodedPathCache.Delete(encoded)

	if got := decodeProjectPath(encoded); got != target {
		t.Fatalf("decodeProjectPath(%q) = %q, want %q", encoded, got, target)
	}
}

// Depth-limit guard: a name with more parts than the resolver can consume is
// rejected before the search starts, since each part costs one depth level.
func TestDecodeProjectPathRejectsMorePartsThanDepthLimit(t *testing.T) {
	name := "-" + strings.Join(repeatParts("a", maxResolveDepth+1), "-")
	decodedPathCache.Delete(name)
	if got := decodeProjectPath(name); got != "" {
		t.Fatalf("decodeProjectPath(%q) = %q, want empty", name, got)
	}
}

func repeatParts(s string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = s
	}
	return out
}
