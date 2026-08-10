package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/extract"
	"github.com/sendbird/ccx/internal/opener"
	"github.com/sendbird/ccx/internal/session"
)

// writeTranscript writes a minimal JSONL transcript and returns its path.
func writeTranscript(t *testing.T, dir, name string, lines ...string) string {
	t.Helper()
	path := filepath.Join(dir, name+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Subcommands used to make the user pick one of the window's sessions, which
// hid the other sessions' items entirely. Every session's items must now appear
// in one list, each tagged with where it came from.
func TestCollectItemsAggregatesEverySession(t *testing.T) {
	dir := t.TempDir()
	a := writeTranscript(t, dir, "a",
		`{"type":"user","uuid":"a1","timestamp":"2026-08-05T10:00:00Z","message":{"role":"user","content":"https://example.com/alpha"}}`)
	b := writeTranscript(t, dir, "b",
		`{"type":"user","uuid":"b1","timestamp":"2026-08-05T11:00:00Z","message":{"role":"user","content":"https://example.com/beta"}}`)

	items, err := collectItems("urls", []pickerSource{
		{filePath: a, sessID: "sess-a", label: "tower"},
		{filePath: b, sessID: "sess-b", label: "crossplane"},
	})
	if err != nil {
		t.Fatal(err)
	}

	bySource := map[string]string{}
	for _, it := range items {
		bySource[it.Source] = it.Item.URL
	}
	if bySource["tower"] != "https://example.com/alpha" {
		t.Errorf("tower item = %q, want the alpha URL", bySource["tower"])
	}
	if bySource["crossplane"] != "https://example.com/beta" {
		t.Errorf("crossplane item = %q, want the beta URL", bySource["crossplane"])
	}
	// Newest first, interleaved across sessions rather than grouped by session.
	if len(items) != 2 || items[0].Source != "crossplane" {
		t.Errorf("aggregated order = %v, want the newer session's item first", sourcesOf(items))
	}
}

// The same PR referenced by two sessions is two facts. Collapsing them would
// have to discard one session's jump target, so both rows survive.
func TestCollectItemsKeepsPerSessionRowsForASharedURL(t *testing.T) {
	dir := t.TempDir()
	shared := `{"type":"user","uuid":"%s","timestamp":"2026-08-05T1%d:00:00Z","message":{"role":"user","content":"https://github.com/o/r/pull/7"}}`
	a := writeTranscript(t, dir, "a", strings.ReplaceAll(strings.Replace(shared, "%d", "0", 1), "%s", "a1"))
	b := writeTranscript(t, dir, "b", strings.ReplaceAll(strings.Replace(shared, "%d", "1", 1), "%s", "b1"))

	items, err := collectItems("refs", []pickerSource{
		{filePath: a, sessID: "sess-a", label: "tower"},
		{filePath: b, sessID: "sess-b", label: "crossplane"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d rows for a shared ref, want one per session: %v", len(items), sourcesOf(items))
	}
	if items[0].SessionID == items[1].SessionID {
		t.Errorf("both rows carry session %q; each must keep its own jump target", items[0].SessionID)
	}
}

// One unreadable transcript must not hide the sessions that do load.
func TestCollectItemsSkipsUnreadableTranscript(t *testing.T) {
	dir := t.TempDir()
	ok := writeTranscript(t, dir, "ok",
		`{"type":"user","uuid":"u1","timestamp":"2026-08-05T10:00:00Z","message":{"role":"user","content":"https://example.com/one"}}`)

	items, err := collectItems("urls", []pickerSource{
		{filePath: filepath.Join(dir, "missing.jsonl"), sessID: "gone", label: "gone"},
		{filePath: ok, sessID: "sess-ok", label: "ok"},
	})
	if err != nil {
		t.Fatalf("one missing transcript failed the whole command: %v", err)
	}
	if len(items) != 1 || items[0].Source != "ok" {
		t.Errorf("items = %v, want only the readable session's item", sourcesOf(items))
	}
}

func sourcesOf(items []PickerItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Source
	}
	return out
}

// The origin label has to fit a list row, so it is the project directory's base
// name — ProjectName is the full shortened path. Sibling worktrees that share a
// base name are disambiguated by short ID rather than silently colliding.
func TestPickerSourcesLabelsByBaseNameAndDisambiguates(t *testing.T) {
	sources := pickerSources([]session.Session{
		{ID: "1", ShortID: "aaaa1111", ProjectPath: "/Users/x/exp/cohome/tower"},
		{ID: "2", ShortID: "bbbb2222", ProjectPath: "/Users/x/exp/cohome"},
		{ID: "3", ShortID: "cccc3333", ProjectPath: "/Users/x/other/tower"},
	})

	got := []string{sources[0].label, sources[1].label, sources[2].label}
	want := []string{"tower:aaaa1111", "cohome", "tower:cccc3333"}
	if !equalSlices(got, want) {
		t.Errorf("labels = %v, want %v", got, want)
	}
}

func TestPickerSourcesFallsBackWhenPathIsUnknown(t *testing.T) {
	sources := pickerSources([]session.Session{{ID: "1", ShortID: "abc12345"}})
	if sources[0].label != "abc12345" {
		t.Errorf("label = %q, want the short ID when there is no project path", sources[0].label)
	}
}

// realIndex maps a filtered row back to allItems. Keyed on the URL alone, both
// rows of a shared ref resolved to the first one, so selecting or jumping from
// the second acted on the wrong session.
func TestRealIndexDistinguishesSessionsSharingAURL(t *testing.T) {
	url := "https://github.com/o/r/pull/7"
	items := []PickerItem{
		{Item: extract.Item{URL: url, Label: "o/r#7", Category: "pr"}, SessionID: "sess-a", Source: "tower"},
		{Item: extract.Item{URL: url, Label: "o/r#7", Category: "pr"}, SessionID: "sess-b", Source: "crossplane"},
	}
	m := newPickerModel("refs", items, opener.Config{}, pickerContext{command: "refs"})

	if got := m.realIndex(1); got != 1 {
		t.Errorf("realIndex(1) = %d, want 1 — the second session's row", got)
	}
	if got := m.realIndex(0); got != 0 {
		t.Errorf("realIndex(0) = %d, want 0", got)
	}
}

func TestPickerShowsSourceOnlyWhenAggregating(t *testing.T) {
	one := []PickerItem{
		{Item: extract.Item{URL: "https://e/1", Label: "one", Category: "pr"}, Source: "tower"},
		{Item: extract.Item{URL: "https://e/2", Label: "two", Category: "pr"}, Source: "tower"},
	}
	if newPickerModel("refs", one, opener.Config{}, pickerContext{}).showSource {
		t.Error("single-session list spends row width on a redundant source column")
	}

	two := append(append([]PickerItem{}, one...),
		PickerItem{Item: extract.Item{URL: "https://e/3", Label: "three", Category: "pr"}, Source: "crossplane"})
	if !newPickerModel("refs", two, opener.Config{}, pickerContext{}).showSource {
		t.Error("aggregated list does not show which session each row came from")
	}
}

// The source column is optional detail: it must never widen a row past the list
// pane, which would make lipgloss wrap it and break the height budget.
func TestRenderListRowKeepsSourceInsideListWidth(t *testing.T) {
	item := PickerItem{
		Item:   extract.Item{URL: "https://github.com/sendbird/delight-core-k8s/pull/1234", Label: "sendbird/delight-core-k8s#1234  1d ago", Category: "pr"},
		Source: "delight-nest-remove-cell-iam",
		Refs:   []ItemRef{{}, {}},
	}
	sel := lipgloss.NewStyle()
	for _, listW := range []int{10, 18, 30, 48, 80} {
		m := newPickerModel("refs", []PickerItem{item,
			{Item: extract.Item{URL: "https://e/2", Label: "other", Category: "pr"}, Source: "tower"}},
			opener.Config{}, pickerContext{command: "refs"})
		row := m.renderListRow(m.allItems[0], true, "  ", listW, sel, sel, sel)
		if w := lipgloss.Width(row); w > listW {
			t.Errorf("listW=%d produced a %d-cell row: %q", listW, w, row)
		}
		if strings.Contains(row, "\n") {
			t.Errorf("listW=%d wrapped the row: %q", listW, row)
		}
	}
}

func TestRenderConversationRowKeepsSourceInsideListWidth(t *testing.T) {
	item := PickerItem{
		Item:             extract.Item{Label: "usr  10:00  #1-4  a fairly long first prompt line", Category: "conversation"},
		ConversationText: strings.Repeat("body text ", 12),
		Source:           "delight-nest-remove-cell-iam",
	}
	sel := lipgloss.NewStyle()
	for _, listW := range []int{10, 20, 40, 80} {
		for _, line := range renderConversationListRow(item, true, "  ", "  ", "", listW, sel, sel, item.Source) {
			if w := lipgloss.Width(line); w > listW {
				t.Errorf("listW=%d produced a %d-cell line: %q", listW, w, line)
			}
		}
	}
}

// An aggregated list is searchable by session so one origin can be isolated.
func TestFilterBySourceNarrowsToOneSession(t *testing.T) {
	items := []PickerItem{
		{Item: extract.Item{URL: "https://e/1", Label: "one", Category: "pr"}, Source: "tower"},
		{Item: extract.Item{URL: "https://e/2", Label: "two", Category: "pr"}, Source: "crossplane"},
	}
	m := newPickerModel("urls", items, opener.Config{}, pickerContext{command: "urls"})
	m.searchTerm = "session:tower"
	m.filterItems()

	if len(m.items) != 1 || m.items[0].Source != "tower" {
		t.Errorf("session:tower matched %v, want only the tower row", sourcesOf(m.items))
	}
}
