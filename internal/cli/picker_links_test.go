package cli

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/keyolk/tuikit/tuitest"

	"github.com/sendbird/ccx/internal/extract"
	"github.com/sendbird/ccx/internal/opener"
)

// stubPickerOpen records what the picker hands its opener instead of running
// one.
func stubPickerOpen(t *testing.T) *[]string {
	t.Helper()
	var got []string
	prev := pickerOpen
	pickerOpen = func(_ opener.Config, u string) error { got = append(got, u); return nil }
	t.Cleanup(func() { pickerOpen = prev })
	return &got
}

func pressO(m pickerModel) pickerModel {
	return feed(m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
}

// A URL row's label is an OSC 8 link to the full URL, and the row still fits
// the list column.
func TestPickerURLRowLabelIsLinked(t *testing.T) {
	m, _ := mousePicker(t, "urls", linkItems(3), 100, 24)
	frame := m.View()
	var found bool
	for _, l := range tuitest.Links(frame) {
		if l.URL == "https://example.com/1" {
			found = true
			if l.Text != "link-01" {
				t.Fatalf("link covers %q; want exactly the label link-01", l.Text)
			}
		}
	}
	if !found {
		t.Fatalf("no link to https://example.com/1:\n%s", ansi.Strip(frame))
	}
	if !strings.Contains(frame, "\x1b]8;;https://example.com/1\x1b\\") {
		t.Fatal("the label is not wrapped in an OSC 8 escape")
	}
	for i, row := range strings.Split(frame, "\n") {
		if w := ansi.StringWidth(row); w > m.width {
			t.Fatalf("row %d is %d cells, wider than %d", i, w, m.width)
		}
	}
}

// The picker's existing `o` keeps opening the row, and refuses a non-http URL
// a transcript put in the list.
func TestPickerOpenSkipsNonHTTPURL(t *testing.T) {
	items := linkItems(2)
	items[1].Item = extract.Item{URL: "javascript:alert(1)", Label: "evil", Category: "other"}
	m, _ := mousePicker(t, "urls", items, 100, 24)
	opened := stubPickerOpen(t)
	m = pressO(m)
	m.cursor = 1
	pressO(m)
	if len(*opened) != 1 || (*opened)[0] != "https://example.com/0" {
		t.Fatalf("opened %q; want only https://example.com/0", *opened)
	}
	if links := tuitest.Links(m.View()); len(links) != 1 {
		t.Fatalf("links %+v; the javascript: row must not be linked", links)
	}
}

// File pickers open local paths: the URL filter must not apply to them.
func TestPickerFilesStillOpenPaths(t *testing.T) {
	items := linkItems(1)
	items[0].Item = extract.Item{URL: "/tmp/repo/app.go", Label: "app.go", Category: "file"}
	m, _ := mousePicker(t, "files", items, 100, 24)
	opened := stubPickerOpen(t)
	pressO(m)
	if len(*opened) != 1 || (*opened)[0] != "/tmp/repo/app.go" {
		t.Fatalf("opened %q; want the path", *opened)
	}
	if links := tuitest.Links(m.View()); len(links) != 0 {
		t.Fatalf("a path was linked: %+v", links)
	}
}
