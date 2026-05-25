package tmux

import "testing"

func TestWalkToPane_DirectChild(t *testing.T) {
	// claude 200 is a direct child of pane shell 100.
	panePIDs := map[int]bool{100: true}
	ppidOf := map[int]int{200: 100, 100: 1}
	if got := walkToPane(200, panePIDs, ppidOf); got != 100 {
		t.Fatalf("walkToPane = %d, want 100", got)
	}
}

func TestWalkToPane_WrappedByCcproxy(t *testing.T) {
	// pane (100) -> ccproxy (150) -> claude (200). The walk must skip
	// past the wrapper to find pane 100.
	panePIDs := map[int]bool{100: true}
	ppidOf := map[int]int{200: 150, 150: 100, 100: 1}
	if got := walkToPane(200, panePIDs, ppidOf); got != 100 {
		t.Fatalf("walkToPane = %d, want 100", got)
	}
}

func TestWalkToPane_Orphan(t *testing.T) {
	// claude reparented to init — no pane in the chain.
	panePIDs := map[int]bool{100: true}
	ppidOf := map[int]int{300: 1, 100: 1}
	if got := walkToPane(300, panePIDs, ppidOf); got != 0 {
		t.Fatalf("walkToPane = %d, want 0", got)
	}
}

func TestWalkToPane_CycleDefense(t *testing.T) {
	// Corrupt ppid map with a cycle. Must terminate and return 0.
	panePIDs := map[int]bool{100: true}
	ppidOf := map[int]int{400: 401, 401: 400}
	if got := walkToPane(400, panePIDs, ppidOf); got != 0 {
		t.Fatalf("walkToPane = %d, want 0", got)
	}
}

func TestWalkToPane_UnknownAncestor(t *testing.T) {
	// PPID chain breaks before reaching a pane — empty ppid map.
	panePIDs := map[int]bool{100: true}
	if got := walkToPane(500, panePIDs, map[int]int{}); got != 0 {
		t.Fatalf("walkToPane = %d, want 0", got)
	}
}
