package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sendbird/ccx/internal/session"
)

// The `x` actions menu on an output row must be HONEST: it lists exactly the
// actions the row can perform and nothing else. A PR has a URL and no file; a
// changed file has a path and no URL; a plan slug inherited from a parent
// session has neither a URL nor a recorded entry to jump to. Offering the same
// fixed menu for all three is the confusion these tests pin down.

// outputsDigestApp builds a session-row App with the per-session Outputs digest
// focused and the given rows already collected.
func outputsDigestApp(t *testing.T, rows []session.SessionOutput) *App {
	t.Helper()
	sessions := []session.Session{{
		ID: "maker", ShortID: "maker", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a",
		ModTime: dayOf(0),
	}}
	app := newTestApp(sessions)
	app.sessPreviewMode = sessPreviewOutputs
	app.sessSplit.Show = true
	app.sessSplit.Focus = true
	app.sessOutputsCacheID = "maker"
	app.sessOutputsRows = rows
	app.sessOutputsCursor = 0
	return app
}

// TestOutputActionsOnlyOfferWhatTheRowCanDo is the heart of the request: each
// row kind gets exactly its applicable actions.
func TestOutputActionsOnlyOfferWhatTheRowCanDo(t *testing.T) {
	app := outputsDigestApp(t, nil)

	cases := []struct {
		name    string
		out     session.SessionOutput
		anchor  bool
		want    []outputActionKind
		notWant []outputActionKind
	}{
		{
			name: "PR with a URL and a first mention",
			out: session.SessionOutput{
				Kind: session.OutputPR, Title: "sendbird/ccx#5",
				URL: "https://github.com/sendbird/ccx/pull/5", MessageUUID: "u2",
			},
			anchor:  true,
			want:    []outputActionKind{outputActionOpenURL, outputActionJump, outputActionCopy},
			notWant: []outputActionKind{outputActionEdit},
		},
		{
			name: "changed file — a local path, no URL",
			out: session.SessionOutput{
				Kind: session.OutputChange, Title: "app.go",
				Path: "/tmp/repo-a/app.go", MessageUUID: "u7",
			},
			anchor:  true,
			want:    []outputActionKind{outputActionEdit, outputActionJump, outputActionCopy},
			notWant: []outputActionKind{outputActionOpenURL},
		},
		{
			name: "scratchpad file found on disk — no transcript entry",
			out: session.SessionOutput{
				Kind: session.OutputScratchpad, Title: "notes.md",
				Path: "/tmp/scratch/notes.md",
			},
			anchor:  true,
			want:    []outputActionKind{outputActionEdit, outputActionSession, outputActionCopy},
			notWant: []outputActionKind{outputActionOpenURL, outputActionJump},
		},
		{
			name: "plan slug inherited from a parent session — no uuid, no file",
			out:  session.SessionOutput{Kind: session.OutputPlan, Title: "some-plan"},
			// No anchor session either: nothing at all applies.
			anchor:  false,
			want:    nil,
			notWant: []outputActionKind{outputActionOpenURL, outputActionJump, outputActionEdit, outputActionCopy, outputActionSession},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := app.outputActionsFor(tc.out, tc.anchor)
			have := map[outputActionKind]bool{}
			for _, act := range got {
				have[act.kind] = true
			}
			for _, k := range tc.want {
				if !have[k] {
					t.Errorf("action %v should apply to this row but was not offered (got %+v)", k, got)
				}
			}
			for _, k := range tc.notWant {
				if have[k] {
					t.Errorf("action %v cannot apply to this row but was offered anyway (got %+v)", k, got)
				}
			}
		})
	}
}

// TestOutputActionsHintBoxHidesInapplicableEntries guards the rendering half:
// the menu the user actually sees must not advertise an action the row cannot
// perform, and must name the row so they know what they are acting on.
func TestOutputActionsHintBoxHidesInapplicableEntries(t *testing.T) {
	app := outputsDigestApp(t, []session.SessionOutput{{
		Kind: session.OutputChange, Title: "app.go",
		Path: "/tmp/repo-a/app.go", MessageUUID: "u7",
	}})

	box := app.renderOutputActionsHintBox()
	if !strings.Contains(box, "app.go") {
		t.Errorf("hint box does not say which row it acts on:\n%s", box)
	}
	if !strings.Contains(box, "$EDITOR") {
		t.Errorf("a file row must offer the editor, got:\n%s", box)
	}
	if strings.Contains(box, "browser") {
		t.Errorf("a row with no URL must not advertise the browser, got:\n%s", box)
	}

	// A PR is the mirror image.
	app.sessOutputsRows = []session.SessionOutput{{
		Kind: session.OutputPR, Title: "sendbird/ccx#5",
		URL: "https://github.com/sendbird/ccx/pull/5", MessageUUID: "u2",
	}}
	box = app.renderOutputActionsHintBox()
	if !strings.Contains(box, "browser") {
		t.Errorf("a PR row must offer the browser, got:\n%s", box)
	}
	if strings.Contains(box, "$EDITOR") {
		t.Errorf("a row with no local file must not advertise the editor, got:\n%s", box)
	}
}

// TestOutputsActionsMenuOpensAndOpensURL walks the full key path: `x` opens the
// menu for the focused digest, and the next key runs the row's action.
func TestOutputsActionsMenuOpensAndOpensURL(t *testing.T) {
	app := outputsDigestApp(t, []session.SessionOutput{{
		Kind: session.OutputPR, Title: "sendbird/ccx#5",
		URL: "https://github.com/sendbird/ccx/pull/5",
	}})

	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	got := m.(*App)
	if !got.actionsMenu {
		t.Fatal("x did not open the actions menu on a focused outputs digest")
	}
	if !strings.Contains(got.renderActionsHintBox(), "sendbird/ccx#5") {
		t.Errorf("the session actions menu rendered instead of the output one:\n%s", got.renderActionsHintBox())
	}

	var opened string
	got.openURL = func(u string) error { opened = u; return nil }
	m, _ = got.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	got = m.(*App)

	if opened != "https://github.com/sendbird/ccx/pull/5" {
		t.Errorf("x→o should open the PR, opened %q", opened)
	}
	if got.actionsMenu {
		t.Error("the menu should close after a key is picked")
	}
}

// TestOutputActionsEditGoesThroughExecProcess pins the terminal-safety
// requirement: shelling out to $EDITOR from inside a running Bubble Tea program
// must hand the terminal over via tea.ExecProcess. Asserting the command is
// CONSTRUCTED (not running it — that would spawn a real editor) is as far as a
// test can go, so this checks the action routes to openInEditor at all.
func TestOutputActionsEditGoesThroughExecProcess(t *testing.T) {
	app := outputsDigestApp(t, []session.SessionOutput{{
		Kind: session.OutputChange, Title: "app.go", Path: "/tmp/repo-a/app.go",
	}})

	row, ok := app.outputActionTarget()
	if !ok {
		t.Fatal("expected the digest row to be the action target")
	}
	_, cmd := app.runOutputAction(row, app.keymap.Actions.Edit)
	if cmd == nil {
		t.Fatal("e on a file row produced no command — $EDITOR was never launched")
	}
	// tea.ExecProcess returns an execMsg-producing cmd, which is a distinct
	// type from a plain tick/nil. Running it would spawn the editor, so only
	// its existence is asserted here; the terminal-handover contract lives in
	// openInEditor, which every other edit path in the app also uses.
}

// TestOutputActionsCopyFallsBackToPath pins the copy target: URL when there is
// one, path otherwise.
func TestOutputActionsCopyFallsBackToPath(t *testing.T) {
	app := outputsDigestApp(t, []session.SessionOutput{{
		Kind: session.OutputScratchpad, Title: "notes.md", Path: "/tmp/scratch/notes.md",
	}})

	var copied string
	orig := clipboardWrite
	clipboardWrite = func(text string) error { copied = text; return nil }
	t.Cleanup(func() { clipboardWrite = orig })

	row, _ := app.outputActionTarget()
	app.runOutputAction(row, app.keymap.Actions.CopyPath)
	if copied != "/tmp/scratch/notes.md" {
		t.Errorf("copied %q, want the row's path", copied)
	}
}

// TestOutputActionTargetUsesTheDigestSession guards the jump anchor: the digest
// tracks exactly one session (sessOutputsCacheID), which is NOT necessarily the
// one the list cursor resolves to. Jumping against the cursor's session would
// open the wrong transcript.
func TestOutputActionTargetUsesTheDigestSession(t *testing.T) {
	app := outputsDigestApp(t, []session.SessionOutput{{
		Kind: session.OutputPR, Title: "sendbird/ccx#5", MessageUUID: "u2",
	}})
	// The digest is still showing the session it was built for while the list
	// cursor has moved on.
	app.sessOutputsCacheID = "previous-session"

	row, ok := app.outputActionTarget()
	if !ok {
		t.Fatal("expected a target row")
	}
	if row.sessID != "previous-session" {
		t.Errorf("action anchored to %q, want the session the digest tracks", row.sessID)
	}
}

// TestOutputsActionsDoNotHijackTheSessionMenu pins ownership the other way: with
// the preview unfocused, `x` is still the session actions menu.
func TestOutputsActionsDoNotHijackTheSessionMenu(t *testing.T) {
	app := outputsDigestApp(t, []session.SessionOutput{{
		Kind: session.OutputPR, Title: "sendbird/ccx#5", URL: "https://example.com/1",
	}})
	app.sessSplit.Focus = false

	if app.outputsPreviewActionsActive() {
		t.Fatal("an unfocused pane must not own the actions menu")
	}

	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	got := m.(*App)
	if !got.actionsMenu {
		t.Fatal("x should still open the session actions menu from the list")
	}
	if box := got.renderActionsHintBox(); strings.Contains(box, "open in browser") {
		t.Errorf("the output menu leaked into the list context:\n%s", box)
	}
}

// TestOutputsDirectKeysStillWork guards the fast path: `x` is additive, so
// Enter/o/y must keep working without going through the menu.
func TestOutputsDirectKeysStillWork(t *testing.T) {
	app := outputsDigestApp(t, []session.SessionOutput{{
		Kind: session.OutputPR, Title: "sendbird/ccx#5",
		URL: "https://github.com/sendbird/ccx/pull/5",
	}})

	var opened string
	app.openURL = func(u string) error { opened = u; return nil }
	if _, _, handled := app.handleOutputsPreviewKeys(&app.sessSplit, "o"); !handled {
		t.Fatal("the digest stopped handling the direct o key")
	}
	if opened != "https://github.com/sendbird/ccx/pull/5" {
		t.Errorf("direct o opened %q", opened)
	}
}

// TestOutputsActionsOnAnEmptyDigestSaysSo pins the empty case: a focused digest
// with nothing in it must not fall through to the list's session actions, which
// would act on a row the user is not looking at.
func TestOutputsActionsOnAnEmptyDigestSaysSo(t *testing.T) {
	app := outputsDigestApp(t, nil)

	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	got := m.(*App)
	if got.actionsMenu {
		t.Error("x opened a menu for a digest with no rows")
	}
	if got.copiedMsg == "" {
		t.Error("x was swallowed silently on an empty digest")
	}
}
