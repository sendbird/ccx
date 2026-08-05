package cli

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/extract"
	"github.com/sendbird/ccx/internal/opener"
	"github.com/sendbird/ccx/internal/session"
)

// refPickerItems builds n PR items with realistic long org/repo#num labels.
func refPickerItems(n int) []PickerItem {
	base := time.Now()
	var items []PickerItem
	for i := 0; i < n; i++ {
		url := fmt.Sprintf("https://github.com/sendbird/delight-core-k8s/pull/%d", 1000+i)
		items = append(items, PickerItem{
			Item: extract.Item{
				URL:       url,
				Label:     fmt.Sprintf("sendbird/delight-core-k8s#%d  1d ago", 1000+i),
				Category:  "pr",
				Timestamp: base,
			},
			SessionID: "s1",
			Refs: []ItemRef{
				{EntryUUID: "u1", Timestamp: base, Role: "user", Preview: "ctx line"},
				{EntryUUID: "u2", Timestamp: base, Role: "user", Preview: "ctx line"},
			},
		})
	}
	return items
}

// resolveAll feeds a fully-resolved, maximally long PR status for every item,
// mimicking the async resolver landing after the picker is already on screen.
func resolveAll(t *testing.T, m pickerModel, items []PickerItem) pickerModel {
	t.Helper()
	for _, it := range items {
		mm, _ := m.Update(pickerRefStatusMsg{ref: session.SessionRef{
			Kind:           session.RefPR,
			URL:            it.Item.URL,
			Label:          it.Item.Label,
			State:          session.RefStateOpen,
			ReviewDecision: "CHANGES_REQUESTED",
			ChecksState:    "FAILURE",
			Resolved:       true,
		}})
		m = mm.(pickerModel)
	}
	return m
}

func assertFitsTerminal(t *testing.T, view string, w, h int, what string) {
	t.Helper()
	if got := lipgloss.Height(view); got > h {
		t.Errorf("%s: view height %d exceeds terminal height %d", what, got, h)
	}
	for i, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > w {
			t.Errorf("%s: line %d width %d exceeds terminal width %d: %q", what, i, got, w, line)
		}
	}
}

// TestPickerViewFitsTerminalAfterRefResolve is the regression guard for the
// layout collapsing once async PR/Jira status arrives: the resolved status was
// appended to rows unconditionally, so rows outgrew the list pane, lipgloss
// wrapped each one onto a second line, and the view grew past the last row.
func TestPickerViewFitsTerminalAfterRefResolve(t *testing.T) {
	sizes := []struct{ w, h int }{
		{100, 24}, // the classic floor
		{80, 24},
		{60, 20}, // narrow tmux split
		{40, 10},
		{200, 50},
	}
	items := refPickerItems(54)
	for _, s := range sizes {
		t.Run(fmt.Sprintf("%dx%d", s.w, s.h), func(t *testing.T) {
			m := newPickerModel("refs", items, opener.Config{}, pickerContext{command: "refs"})
			mm, _ := m.Update(tea.WindowSizeMsg{Width: s.w, Height: s.h})
			m = mm.(pickerModel)

			assertFitsTerminal(t, m.View(), s.w, s.h, "before resolve")
			m = resolveAll(t, m, items)
			assertFitsTerminal(t, m.View(), s.w, s.h, "after resolve")
		})
	}
}

// TestPickerViewFitsWhileSearchingAndPreviewFocused covers the two footers that
// replace the default hint row — both were fixed-length strings wide enough to
// wrap on a narrow terminal.
func TestPickerViewFitsWhileSearchingAndPreviewFocused(t *testing.T) {
	items := refPickerItems(20)
	for _, w := range []int{40, 60, 80, 120} {
		m := newPickerModel("refs", items, opener.Config{}, pickerContext{command: "refs"})
		mm, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: 20})
		m = mm.(pickerModel)
		m = resolveAll(t, m, items)

		model, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
		searching := model.(pickerModel)
		assertFitsTerminal(t, searching.View(), w, 20, fmt.Sprintf("searching w=%d", w))

		focused := m
		focused.previewFocused = true
		assertFitsTerminal(t, focused.View(), w, 20, fmt.Sprintf("preview-focused w=%d", w))
	}
}

// TestPickerViewFitsAllKinds guards the other pickers, whose rows and previews
// carry long file paths and multi-line conversation bodies.
func TestPickerViewFitsAllKinds(t *testing.T) {
	longPath := "/Users/someone/src/org/repo/" + strings.Repeat("deep/", 12) + "file.go"
	body := strings.Repeat("conversation body text ", 20)
	kinds := map[string][]PickerItem{
		"urls": {{
			Item: extract.Item{URL: "https://example.com/" + strings.Repeat("seg/", 30), Label: strings.Repeat("label", 30), Category: "url"},
			Refs: []ItemRef{{EntryUUID: "u", Role: "user", Preview: strings.Repeat("preview ", 60)}},
		}},
		"files": {{
			Item: extract.Item{URL: longPath, Label: longPath, Category: "Read"},
			Refs: []ItemRef{{EntryUUID: "u", Role: "user", Preview: strings.Repeat("preview ", 60)}},
		}},
		"conversation": {{
			Item:             extract.Item{URL: "u", Label: strings.Repeat("longlabel", 12), Category: "conversation"},
			ConversationText: body + "\n" + body + "\n" + body,
			Refs:             []ItemRef{{EntryUUID: "e", Role: "user", Preview: body}},
			ConversationArtifacts: []extract.Item{
				{URL: longPath, Label: longPath, Category: "file"},
			},
		}},
	}
	for kind, items := range kinds {
		for i := 0; i < 25; i++ {
			items = append(items, items[0])
		}
		for _, s := range []struct{ w, h int }{{100, 24}, {60, 18}, {40, 10}} {
			m := newPickerModel(kind, items, opener.Config{}, pickerContext{command: kind})
			mm, _ := m.Update(tea.WindowSizeMsg{Width: s.w, Height: s.h})
			m = mm.(pickerModel)
			assertFitsTerminal(t, m.View(), s.w, s.h, fmt.Sprintf("%s %dx%d", kind, s.w, s.h))
		}
	}
}

// TestRefStatusPlainDegradesToFit verifies the status shrinks by dropping the
// least useful parts (review, then CI) and disappears entirely rather than
// eating the label when the pane is too narrow for even the state token.
func TestRefStatusPlainDegradesToFit(t *testing.T) {
	url := "https://github.com/o/r/pull/1"
	m := newPickerModel("refs", refPickerItems(1), opener.Config{}, pickerContext{})
	m.refStatus[url] = session.SessionRef{
		Kind:           session.RefPR,
		URL:            url,
		State:          session.RefStateOpen,
		ReviewDecision: "APPROVED",
		ChecksState:    "SUCCESS",
		Resolved:       true,
	}
	cases := []struct {
		budget int
		want   string
	}{
		{60, "OPEN · approved · checks ✓"},
		{20, "OPEN · checks ✓"},
		{8, "OPEN"},
		{4, ""}, // 4 cells cannot hold "OPEN" plus its 2-cell separator
		{0, ""},
	}
	for _, c := range cases {
		if got := m.refStatusPlain(url, c.budget); got != c.want {
			t.Errorf("budget %d: got %q want %q", c.budget, got, c.want)
		}
	}
}

// TestRenderListRowNeverExceedsPane is the unit-level invariant behind the
// layout tests: whatever the label and status, a row is at most listW cells.
func TestRenderListRowNeverExceedsPane(t *testing.T) {
	items := refPickerItems(1)
	url := items[0].Item.URL
	m := newPickerModel("refs", items, opener.Config{}, pickerContext{})
	m.refStatus[url] = session.SessionRef{
		Kind: session.RefPR, URL: url, State: session.RefStateOpen,
		ReviewDecision: "CHANGES_REQUESTED", ChecksState: "FAILURE", Resolved: true,
	}
	plain := lipgloss.NewStyle()
	for listW := 1; listW <= 120; listW++ {
		for _, cursored := range []bool{false, true} {
			row := m.renderListRow(items[0], cursored, "  ", listW, plain, plain, plain)
			if got := lipgloss.Width(row); got > listW {
				t.Fatalf("listW=%d cursored=%v: row width %d: %q", listW, cursored, got, row)
			}
		}
	}
}

// TestRenderListRowKeepsStatusIntact guards the separator-budget bug: the label
// budget must account for the two spaces before the status, otherwise the final
// width clamp shears the status tail ("MERGED" → "MERG").
func TestRenderListRowKeepsStatusIntact(t *testing.T) {
	items := refPickerItems(1)
	url := items[0].Item.URL
	m := newPickerModel("refs", items, opener.Config{}, pickerContext{})
	m.refStatus[url] = session.SessionRef{
		Kind: session.RefPR, URL: url, State: session.RefStateMerged, Resolved: true,
	}
	plain := lipgloss.NewStyle()
	// Wide enough that the status is shown at all; every such width must show it
	// in full, never a prefix of it.
	for listW := 20; listW <= 120; listW++ {
		row := m.renderListRow(items[0], false, "  ", listW, plain, plain, plain)
		if strings.Contains(row, "MERGED") {
			continue
		}
		// Status was dropped for width — acceptable — but a partial token is not.
		for _, partial := range []string{"MERGE", "MERG", "MER", "ME"} {
			if strings.HasSuffix(strings.TrimRight(row, " "), partial) {
				t.Errorf("listW=%d: status truncated to %q: %q", listW, partial, row)
				break
			}
		}
	}
}

// TestFitHintPicksWidestThatFits covers the footer degradation ladder.
func TestFitHintPicksWidestThatFits(t *testing.T) {
	long, mid, short := "aaaaaaaaaa", "bbbbb", "cc"
	if got := fitHint(10, long, mid, short); got != long {
		t.Errorf("width 10: got %q want %q", got, long)
	}
	if got := fitHint(9, long, mid, short); got != mid {
		t.Errorf("width 9: got %q want %q", got, mid)
	}
	if got := fitHint(3, long, mid, short); got != short {
		t.Errorf("width 3: got %q want %q", got, short)
	}
	// Below the shortest variant, hard-truncate instead of overflowing.
	if got := lipgloss.Width(fitHint(1, long, mid, short)); got > 1 {
		t.Errorf("width 1: got width %d, want <= 1", got)
	}
}

// refFilterItems builds three PR items: one open, one merged, one closed.
func refFilterItems() []PickerItem {
	return []PickerItem{
		{Item: extract.Item{URL: "https://github.com/o/r/pull/1", Label: "o/r#1", Category: "pr"}},
		{Item: extract.Item{URL: "https://github.com/o/r/pull/2", Label: "o/r#2", Category: "pr"}},
		{Item: extract.Item{URL: "https://github.com/o/r/pull/3", Label: "o/r#3", Category: "pr"}},
	}
}

// resolveRefs sets each item's resolved PR state via the picker's Update loop.
// A WindowSizeMsg is sent first so the preview viewport is initialized before
// ref-status updates trigger updatePreview.
func resolveRefs(m pickerModel, states ...session.RefState) pickerModel {
	mm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	m = mm.(pickerModel)
	for i, st := range states {
		url := m.allItems[i].Item.URL
		mm, _ := m.Update(pickerRefStatusMsg{ref: session.SessionRef{
			Kind: session.RefPR, URL: url, State: st, Resolved: true,
		}})
		m = mm.(pickerModel)
	}
	return m
}

func pickerLabels(m pickerModel) []string {
	out := make([]string, len(m.items))
	for i, it := range m.items {
		out[i] = it.Item.Label
	}
	return out
}

// TestRefsFilterDefaultsToOpen guards the default open-only filter: after
// status resolution, merged and closed PRs drop out of the list while open
// ones stay. Unresolved refs (no status yet) remain visible.
func TestRefsFilterDefaultsToOpen(t *testing.T) {
	items := refFilterItems()
	m := newPickerModel("refs", items, opener.Config{}, pickerContext{command: "refs"})
	m.filterItems()
	// Pre-resolution: all three visible (unresolved → surface).
	if got := len(m.items); got != 3 {
		t.Fatalf("pre-resolve: want 3 items, got %d", got)
	}
	m = resolveRefs(m, session.RefStateOpen, session.RefStateMerged, session.RefStateClosed)
	want := []string{"o/r#1"}
	if got := pickerLabels(m); !equalSlices(got, want) {
		t.Errorf("post-resolve default filter: got %v, want %v", got, want)
	}
}

// A completed lookup with no state means resolution failed, not that the PR is
// inactive. Keep it visible until GitHub positively reports MERGED or CLOSED.
func TestRefsFilterKeepsFailedResolutionVisible(t *testing.T) {
	items := refFilterItems()
	m := newPickerModel("refs", items, opener.Config{}, pickerContext{command: "refs"})
	m.refStatus[items[0].Item.URL] = session.SessionRef{
		Kind:     session.RefPR,
		URL:      items[0].Item.URL,
		State:    session.RefStateUnknown,
		Resolved: true,
	}
	m.filterItems()

	if got := pickerLabels(m); !equalSlices(got, []string{"o/r#1", "o/r#2", "o/r#3"}) {
		t.Errorf("failed resolution filter: got %v, want all refs visible", got)
	}
}

// TestRefsFilterShowsAllWithToggle guards the M toggle: after flipping
// showAllRefs, every ref reappears regardless of state.
func TestRefsFilterShowsAllWithToggle(t *testing.T) {
	items := refFilterItems()
	m := newPickerModel("refs", items, opener.Config{}, pickerContext{command: "refs"})
	m = resolveRefs(m, session.RefStateOpen, session.RefStateMerged, session.RefStateClosed)
	// Default: only open.
	if got := len(m.items); got != 1 {
		t.Fatalf("default: want 1 item, got %d", got)
	}
	m.showAllRefs = true
	m.filterItems()
	if got := len(m.items); got != 3 {
		t.Errorf("showAllRefs: want 3 items, got %d", got)
	}
}

// TestRefsFilterIsMergedNarrows guards that `is:merged` lifts the default
// hide AND narrows to only merged refs (not all refs).
func TestRefsFilterIsMergedNarrows(t *testing.T) {
	items := refFilterItems()
	m := newPickerModel("refs", items, opener.Config{}, pickerContext{command: "refs"})
	m = resolveRefs(m, session.RefStateOpen, session.RefStateMerged, session.RefStateClosed)
	m.searchTerm = "is:merged"
	m.filterItems()
	want := []string{"o/r#2"}
	if got := pickerLabels(m); !equalSlices(got, want) {
		t.Errorf("is:merged: got %v, want %v", got, want)
	}
}

// TestRefsFilterIsClosedNarrows guards `is:closed` narrows to closed refs.
func TestRefsFilterIsClosedNarrows(t *testing.T) {
	items := refFilterItems()
	m := newPickerModel("refs", items, opener.Config{}, pickerContext{command: "refs"})
	m = resolveRefs(m, session.RefStateOpen, session.RefStateMerged, session.RefStateClosed)
	m.searchTerm = "is:closed"
	m.filterItems()
	want := []string{"o/r#3"}
	if got := pickerLabels(m); !equalSlices(got, want) {
		t.Errorf("is:closed: got %v, want %v", got, want)
	}
}

// TestRefsFilterIsAllShowsEverything guards `is:all` lifts the default filter
// without narrowing by state.
func TestRefsFilterIsAllShowsEverything(t *testing.T) {
	items := refFilterItems()
	m := newPickerModel("refs", items, opener.Config{}, pickerContext{command: "refs"})
	m = resolveRefs(m, session.RefStateOpen, session.RefStateMerged, session.RefStateClosed)
	m.searchTerm = "is:all"
	m.filterItems()
	if got := len(m.items); got != 3 {
		t.Errorf("is:all: want 3 items, got %d", got)
	}
}

// TestRefsFilterDoesNotApplyToUrls guards that the open-only default is scoped
// to the refs picker — the urls picker must keep showing every item.
func TestRefsFilterDoesNotApplyToUrls(t *testing.T) {
	items := refFilterItems()
	m := newPickerModel("urls", items, opener.Config{}, pickerContext{command: "urls"})
	m = resolveRefs(m, session.RefStateOpen, session.RefStateMerged, session.RefStateClosed)
	m.filterItems()
	if got := len(m.items); got != 3 {
		t.Errorf("urls picker: want 3 items (no state filter), got %d", got)
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
