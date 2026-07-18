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
