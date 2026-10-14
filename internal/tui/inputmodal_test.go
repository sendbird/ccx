package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func typeInto(m *inputModal, s string) {
	for _, r := range s {
		m.handleKey(string(r))
	}
}

// Hangul runes are two cells wide. Wrapping by rune count let a line of
// Korean text run to twice the modal's inner width and break the border.
func TestInputModalWrapsHangulByCellWidth(t *testing.T) {
	m := newInputModal()
	text := strings.Repeat("안녕하세요 반갑습니다 ", 8)
	typeInto(&m, text)

	const innerW = 20
	rows, cursorVis := m.layout(innerW)
	if len(rows) < 2 {
		t.Fatalf("expected the Hangul line to wrap, got %d row(s)", len(rows))
	}
	var joined strings.Builder
	for _, r := range rows {
		seg := string([]rune(m.lines[r.line])[r.start:r.end])
		if w := lipgloss.Width(seg); w > innerW {
			t.Fatalf("row %q is %d cells, exceeds %d", seg, w, innerW)
		}
		joined.WriteString(seg)
	}
	if joined.String() != text {
		t.Fatalf("wrapping lost text:\n got %q\nwant %q", joined.String(), text)
	}
	if cursorVis != len(rows)-1 {
		t.Fatalf("cursor at end of text should be on the last row, got %d of %d", cursorVis, len(rows))
	}

	// Cursor in the middle lands on the row that contains it.
	m.handleKey("home")
	for i := 0; i < 15; i++ {
		m.handleKey("right")
	}
	rows, cursorVis = m.layout(innerW)
	r := rows[cursorVis]
	if m.cursorCol < r.start || m.cursorCol >= r.end {
		t.Fatalf("cursor col %d not inside row [%d,%d)", m.cursorCol, r.start, r.end)
	}
}

// The modal must keep its fixed height: title, separator, contentH rows,
// separator, help, plus the border. With rune-count truncation a Hangul row
// was twice the inner width, lipgloss re-wrapped it, and the modal grew and
// shifted every row below it.
func TestInputModalRenderKeepsFixedHeightWithHangul(t *testing.T) {
	m := newInputModal()
	typeInto(&m, strings.Repeat("한글입력테스트", 12))
	m.handleKey("enter")
	typeInto(&m, strings.Repeat("가", 50))
	m.handleKey("up") // cursor off the second line: it renders as a plain row

	const screenW, screenH = 80, 40
	contentH := min(screenH/2, 20)
	out := m.render("", screenW, screenH)
	lines := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(ansi.Strip(line)) != "" {
			lines++
		}
		if w := lipgloss.Width(line); w > screenW {
			t.Fatalf("line is %d cells, wider than screen %d", w, screenW)
		}
	}
	if want := contentH + 4 + 2; lines != want {
		t.Fatalf("modal has %d lines, want %d\n%s", lines, want, out)
	}
}
