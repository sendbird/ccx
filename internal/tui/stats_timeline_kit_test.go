package tui

import (
	"strings"
	"testing"

	"github.com/keyolk/tuikit/render"
)

// The old ceil-sized buckets drew 9 points into 5 cells of an 8-cell budget
// and averaged a one-point tail as a whole bucket; tuikit/chart fills the
// width with equal shares.
func TestSparklineFillsWidthWhenBucketing(t *testing.T) {
	vals := []int{1, 2, 3, 4, 5, 6, 7, 8, 9}
	if got := sparkline(vals, 8); render.Width(got) != 8 {
		t.Fatalf("sparkline(9 points, 8) = %q, width %d", got, render.Width(got))
	}
	// A short series stays one cell per point.
	if got := sparkline([]int{1, 2}, 8); render.Width(got) != 2 {
		t.Fatalf("short series stretched: %q", got)
	}
}

func TestSparklineEdges(t *testing.T) {
	if got := sparkline([]int{1, 2, 3}, 0); got != "" { // used to divide by zero
		t.Fatalf("width 0 = %q", got)
	}
	floor := chartLook.Glyphs.Levels[0]
	if got := sparkline([]int{0, 0, 0}, 8); got != strings.Repeat(floor, 3) {
		t.Fatalf("idle series = %q, want it on the floor", got)
	}
}
