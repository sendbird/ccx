package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sendbird/ccx/internal/session"
)

func searchApp(t *testing.T, sessions []session.Session, results []session.SearchResult) *App {
	t.Helper()
	a := newTestApp(sessions)
	a.enterSearchMode()
	a.searchInput.Blur() // focus the result list, not the query box
	a.searchQuery = "q"
	a.updateSearchResults(results)
	return a
}

// A search hit has to say whether its session is still running: that is what
// decides between "jump to the pane" and "revive it".
func TestSearchResultShowsLiveState(t *testing.T) {
	live := session.Session{ID: "live-1", ShortID: "live-1", ProjectName: "proj-live", IsLive: true}
	dead := session.Session{ID: "dead-1", ShortID: "dead-1", ProjectName: "proj-dead"}

	liveCopy, deadCopy := live, dead
	a := searchApp(t, []session.Session{live, dead}, []session.SearchResult{
		{Session: &liveCopy, Snippet: "hit in live"},
		{Session: &deadCopy, Snippet: "hit in dead"},
	})

	items := a.searchResultList.Items()
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}

	byProject := map[string]searchResultItem{}
	for _, raw := range items {
		item := raw.(searchResultItem)
		byProject[item.result.Session.ProjectName] = item
	}
	if !byProject["proj-live"].live {
		t.Error("live session's result row is not marked live")
	}
	if byProject["proj-dead"].live {
		t.Error("dead session's result row is marked live")
	}
	if !strings.Contains(stripANSI(byProject["proj-live"].Title()), "LIVE") {
		t.Errorf("live row title has no LIVE badge: %q", stripANSI(byProject["proj-live"].Title()))
	}
	if strings.Contains(stripANSI(byProject["proj-dead"].Title()), "LIVE") {
		t.Errorf("dead row title claims LIVE: %q", stripANSI(byProject["proj-dead"].Title()))
	}
}

// The search result carries a snapshot from when the search ran. Live state has
// to come from the store instead, or a session that started or exited while the
// results were on screen is reported wrongly.
func TestSearchResultLiveStateComesFromStore(t *testing.T) {
	// Snapshot says dead...
	snapshot := session.Session{ID: "s1", ShortID: "s1", ProjectName: "proj", IsLive: false}
	// ...but the store says it is live now.
	a := newTestApp([]session.Session{{ID: "s1", ShortID: "s1", ProjectName: "proj", IsLive: true}})
	a.enterSearchMode()
	a.searchInput.Blur()
	a.updateSearchResults([]session.SearchResult{{Session: &snapshot, Snippet: "hit"}})

	item := a.searchResultList.Items()[0].(searchResultItem)
	if !item.live {
		t.Error("stale snapshot won over the store — a session that came up is shown as dead")
	}

	// And the reverse: store says dead, snapshot claimed live.
	staleLive := session.Session{ID: "s2", ShortID: "s2", ProjectName: "proj2", IsLive: true}
	b := newTestApp([]session.Session{{ID: "s2", ShortID: "s2", ProjectName: "proj2", IsLive: false}})
	b.enterSearchMode()
	b.searchInput.Blur()
	b.updateSearchResults([]session.SearchResult{{Session: &staleLive, Snippet: "hit"}})

	if b.searchResultList.Items()[0].(searchResultItem).live {
		t.Error("stale snapshot won over the store — an exited session is still shown live")
	}
}

// Resuming from a search hit must reach resumeSession, which is what branches
// on live (attach to pane) vs not (revive in a tmux window).
func TestResumeKeyFromSearchResults(t *testing.T) {
	sess := session.Session{ID: "s1", ShortID: "s1", ProjectName: "proj", ProjectPath: t.TempDir()}
	snapshot := sess
	a := searchApp(t, []session.Session{sess}, []session.SearchResult{
		{Session: &snapshot, Snippet: "hit"},
	})
	if !a.searchActive {
		t.Fatal("search mode not active")
	}

	key := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(a.keymap.Actions.Resume)}
	m, _ := a.handleSearchKey(key)
	got := m.(*App)

	// Whatever the resume outcome (no tmux in tests), the modal must close —
	// otherwise the search overlay stays on top of the session being resumed.
	if got.searchActive {
		t.Error("resume key did not close the search modal")
	}
}

// A rebound Resume key must not swallow list navigation.
func TestSearchNavigationKeysWinOverRebooundResume(t *testing.T) {
	sessions := []session.Session{
		{ID: "s1", ShortID: "s1", ProjectName: "p1"},
		{ID: "s2", ShortID: "s2", ProjectName: "p2"},
	}
	s1, s2 := sessions[0], sessions[1]
	a := searchApp(t, sessions, []session.SearchResult{
		{Session: &s1, Snippet: "hit 1"},
		{Session: &s2, Snippet: "hit 2"},
	})
	// Pathological rebinding: Resume bound to the down key.
	a.keymap.Actions.Resume = "j"

	before := a.searchResultList.Index()
	m, _ := a.handleSearchKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	got := m.(*App)

	if !got.searchActive {
		t.Fatal("j triggered resume instead of moving the cursor")
	}
	if got.searchResultList.Index() == before {
		t.Error("j did not move the result cursor")
	}
}

// The resume key must be discoverable from the modal itself.
func TestSearchHelpMentionsResumeKey(t *testing.T) {
	sess := session.Session{ID: "s1", ShortID: "s1", ProjectName: "proj"}
	snapshot := sess
	a := searchApp(t, []session.Session{sess}, []session.SearchResult{
		{Session: &snapshot, Snippet: "hit"},
	})
	a.width, a.height = 120, 40

	view := stripANSI(a.renderSearchModal(""))
	if !strings.Contains(view, "resume") {
		t.Errorf("search modal help does not mention resume:\n%s", view)
	}
}

// A session that starts or exits while the results are on screen must be
// reflected in the badge — that badge is what the resume decision is made from.
func TestSearchBadgesTrackLiveChangesWhileOpen(t *testing.T) {
	sess := session.Session{ID: "s1", ShortID: "s1", ProjectName: "proj"}
	snapshot := sess
	a := searchApp(t, []session.Session{sess}, []session.SearchResult{
		{Session: &snapshot, Snippet: "hit"},
	})
	if a.searchResultList.Items()[0].(searchResultItem).live {
		t.Fatal("session started out live")
	}

	// The session comes up while the modal is open.
	for i := range a.sessions {
		if a.sessions[i].ID == "s1" {
			a.sessions[i].IsLive = true
		}
	}
	a.handleTick()

	if !a.searchResultList.Items()[0].(searchResultItem).live {
		t.Error("badge did not pick up the session coming live while results were open")
	}

	// And back down again.
	for i := range a.sessions {
		if a.sessions[i].ID == "s1" {
			a.sessions[i].IsLive = false
		}
	}
	a.handleTick()

	if a.searchResultList.Items()[0].(searchResultItem).live {
		t.Error("badge did not pick up the session exiting while results were open")
	}
}

// Refreshing the rows must not move the user's cursor out from under them.
func TestSearchBadgeRefreshPreservesCursor(t *testing.T) {
	sessions := []session.Session{
		{ID: "s1", ShortID: "s1", ProjectName: "p1"},
		{ID: "s2", ShortID: "s2", ProjectName: "p2"},
		{ID: "s3", ShortID: "s3", ProjectName: "p3"},
	}
	s1, s2, s3 := sessions[0], sessions[1], sessions[2]
	a := searchApp(t, sessions, []session.SearchResult{
		{Session: &s1, Snippet: "hit 1"},
		{Session: &s2, Snippet: "hit 2"},
		{Session: &s3, Snippet: "hit 3"},
	})

	a.searchResultList.Select(2)
	a.handleTick()

	if got := a.searchResultList.Index(); got != 2 {
		t.Errorf("cursor moved on refresh: index = %d, want 2", got)
	}
}
