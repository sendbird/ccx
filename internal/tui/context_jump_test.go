package tui

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/sendbird/ccx/internal/extract"
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

func TestConvPageCopySelectedUsesRelatedPathForContexts(t *testing.T) {
	app := newTestApp(fakeSessions())
	app.convPage = convPageContexts
	app.convPageItems = []convPageItem{{
		Item:        extract.Item{Label: "context", URL: "ignored", Category: "context"},
		timestamp:   time.Time{},
		relatedPath: filepath.Clean("/tmp/context-file.md"),
		relatedView: "config",
		turnPreview: "preview",
	}}
	app.convPageCursor = 0
	_, _ = app.convPageCopySelected()
	if app.copiedMsg != "Copied path" {
		t.Fatalf("expected copy confirmation, got %q", app.copiedMsg)
	}
}
