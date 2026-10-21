package tui

import (
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/keyolk/tuikit/link"
	"github.com/keyolk/tuikit/tuitest"

	"github.com/sendbird/ccx/internal/extract"
	"github.com/sendbird/ccx/internal/session"
)

const (
	prURL   = "https://github.com/sendbird/ccx/pull/5"
	jiraURL = "https://sendbird.atlassian.net/browse/CPLAT-12912"
)

// stubLauncher replaces link.Launcher for the test and records every URL it
// is handed. The launcher runs inside the command, off the test goroutine,
// so it records and never fails the test itself.
func stubLauncher(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var got []string
	prev := link.Launcher
	link.Launcher = func(u string) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, u)
		return nil
	}
	t.Cleanup(func() { link.Launcher = prev })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

// pressOpenKey sends one rune key and runs the command it returns, feeding every
// link.OpenedMsg back as Bubble Tea would. Toast ticks are skipped by Drain.
func pressOpenKey(t *testing.T, app *App, key rune) (*App, []link.OpenedMsg) {
	t.Helper()
	app, cmd := sendCmd(app, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{key}})
	var opened []link.OpenedMsg
	for _, msg := range tuitest.Drain(cmd, 500*time.Millisecond) {
		if om, ok := msg.(link.OpenedMsg); ok {
			opened = append(opened, om)
			app = send(app, om)
		}
	}
	return app, opened
}

// linkTo returns the frame's OSC 8 link to u, or false.
func linkTo(frame, u string) (tuitest.Link, bool) {
	for _, l := range tuitest.Links(frame) {
		if l.URL == u {
			return l, true
		}
	}
	return tuitest.Link{}, false
}

// assertRowsFit fails when a wrapped label widened any row past the screen.
func assertRowsFit(t *testing.T, app *App) {
	t.Helper()
	for i, row := range strings.Split(app.View(), "\n") {
		if w := ansi.StringWidth(row); w > app.width {
			t.Fatalf("row %d is %d cells, wider than the %d-cell screen: %q", i, w, app.width, ansi.Strip(row))
		}
	}
}

func toastText(t *testing.T, app *App) string {
	t.Helper()
	tt, ok := app.statusToast()
	if !ok {
		t.Fatal("no toast shown")
	}
	return tt.Text
}

// The Outputs digest shows a PR by its key and a Jira issue by its key; the
// URL is not on screen, so the title is an OSC 8 link to it. A javascript:
// URL from a transcript is not a link.
func TestOutputsDigestLinksTitlesThatHideTheirURL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sessions := []session.Session{{
		ID: "maker", ShortID: "maker", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a", ModTime: dayOf(0),
		RefsResolved: true,
		Refs: []session.SessionRef{
			{Kind: session.RefPR, Label: "sendbird/ccx#5", URL: prURL, Resolved: true},
			{Kind: session.RefJira, Label: "CPLAT-12912", URL: jiraURL, Resolved: true},
			{Kind: session.RefArtifact, Label: "evil-page", URL: "javascript:alert(1)"},
		},
	}}
	app := newTestApp(sessions)
	app.sessionList.Select(0)
	app.sessPreviewMode = sessPreviewOutputs
	app.sessSplit.Show = true
	app.sessSplit.Focus = true
	_ = app.updateSessionPreview()
	frame := app.View()
	for u, label := range map[string]string{prURL: "sendbird/ccx#5", jiraURL: "CPLAT-12912"} {
		l, ok := linkTo(frame, u)
		if !ok {
			t.Fatalf("no OSC 8 link to %s in the digest:\n%s", u, ansi.Strip(frame))
		}
		if l.Text != label {
			t.Fatalf("link to %s covers %q; want exactly the title %q", u, l.Text, label)
		}
	}
	if !strings.Contains(frame, "\x1b]8;;"+prURL+"\x1b\\") {
		t.Fatal("the PR link is not an OSC 8 escape around the title")
	}
	if !strings.Contains(ansi.Strip(frame), "evil-page") {
		t.Fatalf("the javascript: row is not drawn:\n%s", ansi.Strip(frame))
	}
	for _, l := range tuitest.Links(frame) {
		if l.URL != prURL && l.URL != jiraURL {
			t.Fatalf("unexpected link %+v: only http(s) URLs may be linked", l)
		}
	}
	assertRowsFit(t, app)
}

// The References preview links each ref's label (key plus state) to its URL.
func TestRefsPreviewLinksLabel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	sessions := fakeSessions()
	sessions[0].Refs = []session.SessionRef{
		{Kind: session.RefPR, URL: prURL, Label: "sendbird/ccx#5", State: session.RefStateOpen, Resolved: true},
		{Kind: session.RefArtifact, URL: "file:///etc/passwd", Label: "local", Title: "local file"},
	}
	a := newTestApp(sessions)
	a.sessionList.Select(0)
	a.sessPreviewMode = sessPreviewRefs
	a.sessSplit.Show = true
	a.sessSplit.Focus = true
	_ = a.updateSessionPreview()
	frame := a.View()
	l, ok := linkTo(frame, prURL)
	if !ok || l.Text != "sendbird/ccx#5 OPEN" {
		t.Fatalf("refs preview link = %+v (found %t); want the label \"sendbird/ccx#5 OPEN\"", l, ok)
	}
	if !strings.Contains(ansi.Strip(frame), "local file") {
		t.Fatalf("the file: ref is not drawn:\n%s", ansi.Strip(frame))
	}
	if _, ok := linkTo(frame, "file:///etc/passwd"); ok {
		t.Fatal("a file: URL from a transcript was linked")
	}
	assertRowsFit(t, a)
}

// The existing `o` on a refs row keeps opening it, but never hands a
// non-http URL to the opener.
func TestRefsPreviewOpenRefusesNonHTTPURL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, bad := range []string{"javascript:alert(1)", "file:///etc/passwd"} {
		a := newTestApp(fakeSessions())
		a.sessionList.Select(0)
		a.sessPreviewMode = sessPreviewRefs
		a.sessSplit.Show = true
		a.sessSplit.Focus = true
		a.sessPreviewRefs = []session.SessionRef{{Kind: session.RefArtifact, URL: bad, Label: "x"}}
		var opened []string
		a.openURL = func(u string) error { opened = append(opened, u); return nil }
		a = send(a, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
		if len(opened) != 0 {
			t.Fatalf("o handed %q to the opener", opened)
		}
	}
}

// The day pane links its PR rows too.
func TestDayPaneLinksPRTitle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	app := dayPaneApp(t, []session.Session{{
		ID: "maker", ShortID: "maker", ProjectPath: "/tmp/repo-a", ProjectName: "repo-a",
		ModTime: dayOf(0),
		Refs:    []session.SessionRef{{Kind: session.RefPR, Label: "sendbird/ccx#5", URL: prURL, Resolved: true}},
	}})
	l, ok := linkTo(app.View(), prURL)
	if !ok || l.Text != "sendbird/ccx#5" {
		t.Fatalf("day pane link = %+v (found %t); want the title sendbird/ccx#5", l, ok)
	}
	assertRowsFit(t, app)
}

// sessionActionsApp is the session list with the actions menu open on sess.
func sessionActionsApp(t *testing.T, sess session.Session) *App {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	app := newTestApp([]session.Session{sess})
	app.actionsMenu = true
	app.actionsSess = sess
	return app
}

// x o on a session opens its first PR/Jira link with link.Open, and the
// toast says which one and that there were more.
func TestSessionActionsOpenLinkOpensFirstRef(t *testing.T) {
	launched := stubLauncher(t)
	app := sessionActionsApp(t, session.Session{
		ID: "s1", ShortID: "s1", ProjectPath: "/tmp/p", ProjectName: "p", ModTime: time.Now(),
		Refs: []session.SessionRef{
			{Kind: session.RefPR, URL: prURL, Label: "sendbird/ccx#5"},
			{Kind: session.RefJira, URL: jiraURL, Label: "CPLAT-12912"},
		},
	})
	app, opened := pressOpenKey(t, app, 'o')
	if got := launched(); len(got) != 1 || got[0] != prURL {
		t.Fatalf("launcher got %q; want exactly [%s]", got, prURL)
	}
	if len(opened) != 1 || opened[0].URL != prURL || opened[0].Err != nil {
		t.Fatalf("OpenedMsg = %+v", opened)
	}
	if got := toastText(t, app); got != "Opened "+prURL+" (the first of 2 links)" {
		t.Fatalf("toast = %q", got)
	}
}

// A session with no ref launches nothing and says so.
func TestSessionActionsOpenLinkWithoutLinkSaysSo(t *testing.T) {
	launched := stubLauncher(t)
	app := sessionActionsApp(t, session.Session{
		ID: "s1", ShortID: "s1", ProjectPath: "/tmp/p", ProjectName: "p", ModTime: time.Now(),
		Refs: []session.SessionRef{{Kind: session.RefArtifact, URL: "javascript:alert(1)", Label: "x"}},
	})
	app, _ = pressOpenKey(t, app, 'o')
	if got := launched(); len(got) != 0 {
		t.Fatalf("launcher got %q; want nothing", got)
	}
	if got := toastText(t, app); got != "No link here" {
		t.Fatalf("toast = %q, want No link here", got)
	}
}

// Refs not loaded yet are read from the transcript, offline.
func TestSessionActionsOpenLinkReadsTranscriptRefs(t *testing.T) {
	launched := stubLauncher(t)
	path := writeTranscript(t,
		`{"type":"assistant","uuid":"u2","timestamp":"2026-08-13T01:01:00Z","message":{"role":"assistant","content":[{"type":"text","text":"opened `+prURL+`"}]}}`,
	)
	app := sessionActionsApp(t, session.Session{
		ID: "s1", ShortID: "s1", FilePath: path, ProjectPath: "/tmp/p", ProjectName: "p", ModTime: time.Now(),
	})
	pressOpenKey(t, app, 'o')
	if got := launched(); len(got) != 1 || got[0] != prURL {
		t.Fatalf("launcher got %q; want [%s]", got, prURL)
	}
}

// The actions hint box and the help overlay both show the new binding.
func TestOpenLinkBindingShowsInHelp(t *testing.T) {
	app := sessionActionsApp(t, session.Session{ID: "s1", ShortID: "s1", ProjectPath: "/tmp/p", ModTime: time.Now()})
	if box := ansi.Strip(app.renderActionsHintBox()); !strings.Contains(box, "o:open-link") {
		t.Fatalf("actions hint box lacks o open-link:\n%s", box)
	}
	found := false
	for _, r := range app.sessionsListHelpRows() {
		if r.key == "x o" && strings.Contains(r.desc, "link") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the ? overlay has no x o row: %+v", app.sessionsListHelpRows())
	}
	if conflicts := app.keymap.Conflicts(); len(conflicts) != 0 {
		t.Fatalf("keymap conflicts: %v", conflicts)
	}
}

// convLinkApp is a conversation whose only message carries two http URLs and
// a javascript: one, with the conversation actions menu open.
func convLinkApp(t *testing.T, text string) *App {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	app := setupConvApp(t, []session.Entry{makeTextEntry("assistant", base, text)}, 160, 40)
	app.convList.Select(0)
	app.updateConvPreview()
	return send(app, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
}

// x o in the conversation opens the first URL of the selected message.
func TestConversationActionsOpenLinkOpensFirstURL(t *testing.T) {
	launched := stubLauncher(t)
	app := convLinkApp(t, "try javascript:alert(1) then see "+prURL+" (and https://example.com/b).")
	if !app.convActionsMenu {
		t.Fatal("x did not open the conversation actions menu")
	}
	if box := ansi.Strip(app.renderConvActionsHintBox()); !strings.Contains(box, "o:open-link") {
		t.Fatalf("conversation actions lack o open-link:\n%s", box)
	}
	app, opened := pressOpenKey(t, app, 'o')
	if got := launched(); len(got) != 1 || got[0] != prURL {
		t.Fatalf("launcher got %q; want [%s]", got, prURL)
	}
	if len(opened) != 1 || opened[0].Of != 2 {
		t.Fatalf("OpenedMsg = %+v; want the first of 2", opened)
	}
	if got := toastText(t, app); !strings.Contains(got, "the first of 2 links") {
		t.Fatalf("toast = %q", got)
	}
}

func TestConversationActionsOpenLinkWithoutURLSaysSo(t *testing.T) {
	launched := stubLauncher(t)
	app := convLinkApp(t, "no links, only javascript:alert(1) and file:///etc/passwd")
	app, _ = pressOpenKey(t, app, 'o')
	if got := launched(); len(got) != 0 {
		t.Fatalf("launcher got %q; want nothing", got)
	}
	if got := toastText(t, app); got != "No link here" {
		t.Fatalf("toast = %q", got)
	}
}

// urlMenuApp opens the URL menu on the given items.
func urlMenuApp(t *testing.T, scope string, items []extract.Item) *App {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	app := newTestApp(fakeSessions())
	m, _ := app.openURLMenuFromItems(items, scope)
	return m.(*App)
}

// The URL menu links each label to its URL, and o opens the row under the
// cursor without closing the menu.
func TestURLMenuLinksLabelsAndOOpensCursorRow(t *testing.T) {
	launched := stubLauncher(t)
	a := "https://example.com/a"
	b := "https://example.com/b"
	app := urlMenuApp(t, "session", []extract.Item{
		{URL: a, Label: "example a", Category: "other"},
		{URL: b, Label: "example b", Category: "other"},
	})
	frame := app.View()
	if l, ok := linkTo(frame, b); !ok || l.Text != "example b" {
		t.Fatalf("URL menu link to b = %+v (found %t)", l, ok)
	}
	assertRowsFit(t, app)
	app = send(app, tea.KeyMsg{Type: tea.KeyDown})
	app, opened := pressOpenKey(t, app, 'o')
	if got := launched(); len(got) != 1 || got[0] != b {
		t.Fatalf("launcher got %q; want [%s]", got, b)
	}
	if len(opened) != 1 || !app.urlMenu {
		t.Fatalf("opened %+v, menu open %t; want one open and the menu kept", opened, app.urlMenu)
	}
}

// File rows carry paths: no link, and o says there is none.
func TestURLMenuFileRowHasNoLink(t *testing.T) {
	launched := stubLauncher(t)
	app := urlMenuApp(t, "session files", []extract.Item{{URL: "/tmp/repo/app.go", Label: "app.go", Category: "file"}})
	if links := tuitest.Links(app.View()); len(links) != 0 {
		t.Fatalf("file row linked: %+v", links)
	}
	app, _ = pressOpenKey(t, app, 'o')
	if got := launched(); len(got) != 0 {
		t.Fatalf("launcher got %q", got)
	}
	if got := toastText(t, app); got != "No link here" {
		t.Fatalf("toast = %q", got)
	}
}
