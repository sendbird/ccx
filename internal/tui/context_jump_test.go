package tui

import (
	"testing"

	"github.com/sendbird/ccx/internal/session"
)

func TestOpenRelatedContextNodeNoTarget(t *testing.T) {
	app := newTestApp(fakeSessions())
	m, cmd := app.openRelatedContextNode(session.ContextNode{})
	if m != app || cmd != nil {
		t.Fatalf("expected no-op openRelatedContextNode")
	}
	if app.copiedMsg != "No related destination" {
		t.Fatalf("unexpected message: %q", app.copiedMsg)
	}
}

// TestFlattenContextNodesOnlyDrillable verifies the flat cursor list contains
// only drill-targetable nodes (RelatedView set), in pre-order.
func TestFlattenContextNodesOnlyDrillable(t *testing.T) {
	tree := &session.SessionContextTree{
		Roots: []session.ContextNode{
			{Label: "skills", Children: []session.ContextNode{
				{Label: "skill-a", RelatedView: "config", RelatedPath: "/a"},
				{Label: "skill-b (no target)"},
				{Label: "skill-c", RelatedView: "plugin", RelatedPluginID: "p1"},
			}},
			{Label: "hooks", RelatedView: "config", RelatedPath: "/h"},
		},
	}
	nodes := flattenContextNodes(tree)
	if len(nodes) != 3 {
		t.Fatalf("flattened drillable nodes = %d, want 3 (%v)", len(nodes), nodes)
	}
	if nodes[0].Label != "skill-a" || nodes[1].Label != "skill-c" || nodes[2].Label != "hooks" {
		t.Fatalf("pre-order/filter wrong: %v", []string{nodes[0].Label, nodes[1].Label, nodes[2].Label})
	}
}

// TestOpenSelectedContextNodeUsesCursor verifies the cursor selects which node
// gets drilled into.
func TestOpenSelectedContextNodeUsesCursor(t *testing.T) {
	app := newTestApp(fakeSessions())
	app.sessCtxNodes = []session.ContextNode{
		{Label: "n0"}, // no RelatedView → "No related destination"
		{Label: "n1", RelatedView: "bogus"},
	}
	app.sessCtxCursor = 0
	app.openSelectedContextNode()
	if app.copiedMsg != "No related destination" {
		t.Fatalf("cursor 0 (no target) msg = %q", app.copiedMsg)
	}
	// Out-of-range cursor is a safe no-op.
	app.sessCtxCursor = 99
	if _, _, ok := app.openSelectedContextNode(); !ok {
		t.Fatal("expected handled=true for out-of-range cursor")
	}
}
