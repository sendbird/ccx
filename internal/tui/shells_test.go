package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

func TestFormatBashFolded_BackgroundFlag(t *testing.T) {
	out := stripANSI(formatBashFolded(`{"command":"npm run watch","run_in_background":true}`))
	if !strings.Contains(out, "[bg]") {
		t.Fatalf("expected background hint in folded bash, got %q", out)
	}
	if !strings.Contains(out, "$ npm run watch") {
		t.Fatalf("expected command in folded bash, got %q", out)
	}

	plain := stripANSI(formatBashFolded(`{"command":"ls"}`))
	if strings.Contains(plain, "[bg]") {
		t.Fatalf("foreground bash should not show bg hint, got %q", plain)
	}
}

func TestFormatMonitorFolded_PersistentFlag(t *testing.T) {
	out := stripANSI(formatMonitorFolded(`{"command":"while true; do sleep 60; done","persistent":true,"description":"watch logs"}`))
	if !strings.Contains(out, "[monitor·persistent]") {
		t.Fatalf("expected persistent monitor tag, got %q", out)
	}
	if !strings.Contains(out, "watch logs") {
		t.Fatalf("expected description, got %q", out)
	}

	once := stripANSI(formatMonitorFolded(`{"command":"echo hi"}`))
	if !strings.Contains(once, "[monitor]") || strings.Contains(once, "persistent") {
		t.Fatalf("unexpected monitor folded output: %q", once)
	}
}

func TestBuildShellsPreviewContent(t *testing.T) {
	app := newTestApp(fakeSessions())
	sess := session.Session{
		ID: "shellsess", ShortID: "shellses",
		HasShellJobs: true,
		ShellJobs: []session.ShellJob{
			{
				ID: "tu1", ToolName: "Bash",
				Command: "npm run dev", Description: "dev server",
				TimeoutMS:   120000,
				StartedAt:   time.Now().Add(-3 * time.Minute),
				LastEventAt: time.Now().Add(-1 * time.Minute),
				PollCount:   2, Status: "polled",
			},
			{
				ID: "tu2", ToolName: "Monitor",
				Command:     "while true; do echo .; sleep 60; done",
				Description: "watch secrets",
				Persistent:  true, TimeoutMS: 300000,
				StartedAt:   time.Now().Add(-5 * time.Minute),
				LastEventAt: time.Now().Add(-5 * time.Minute),
				Status:      "running",
			},
		},
	}

	out := stripANSI(app.buildShellsPreviewContent(sess))
	for _, want := range []string{
		"Background shells",
		"Bash",
		"Monitor",
		"persistent",
		"npm run dev",
		"watch secrets",
		"polls",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected shells preview to contain %q, got %q", want, out)
		}
	}
}

func TestBuildShellsPreviewContent_EmptyState(t *testing.T) {
	app := newTestApp(fakeSessions())
	sess := session.Session{ID: "x", ShortID: "x"}
	out := stripANSI(app.buildShellsPreviewContent(sess))
	if !strings.Contains(out, "No background shells") {
		t.Fatalf("expected empty state, got %q", out)
	}
}
