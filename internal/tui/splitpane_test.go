package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	ansi "github.com/charmbracelet/x/ansi"
)

// findBorderColumns returns the set of CHA column positions where the border
// character │ is placed via \x1b[{col}G. Each row should have exactly one
// border CHA at the same column.
func findBorderCHAColumns(output string) []int {
	var cols []int
	for _, line := range strings.Split(output, "\n") {
		// Parse CHA sequences: \x1b[{n}G
		for i := 0; i < len(line); i++ {
			if i+2 < len(line) && line[i] == '\x1b' && line[i+1] == '[' {
				j := i + 2
				num := 0
				for j < len(line) && line[j] >= '0' && line[j] <= '9' {
					num = num*10 + int(line[j]-'0')
					j++
				}
				if j < len(line) && line[j] == 'G' {
					// Found CHA. Check if next visible char after styled border is │
					rest := line[j+1:]
					plain := ansi.Strip(rest)
					if len(plain) > 0 && []rune(plain)[0] == '│' {
						cols = append(cols, num)
						break // one border per line
					}
				}
			}
		}
	}
	return cols
}

func TestRenderFixedSplit_DividerAligned_ASCII(t *testing.T) {
	left := "hello\nworld\nfoo"
	right := "right side\ncontent here\nbar"
	listW, previewW, contentH := 30, 50, 5

	out := renderFixedSplit(left, right, listW, previewW, contentH, colorDim)
	cols := findBorderCHAColumns(out)

	if len(cols) != contentH {
		t.Fatalf("expected %d border columns, got %d", contentH, len(cols))
	}
	for i, col := range cols {
		if col != listW+1 {
			t.Errorf("row %d: border at column %d, want %d", i, col, listW+1)
		}
	}
}

func TestRenderFixedSplit_DividerAligned_Korean(t *testing.T) {
	left := "한글 텍스트 테스트\n두번째 줄입니다\nASCII mixed 한글"
	right := "오른쪽 패널\n내용입니다\n세번째"
	listW, previewW, contentH := 40, 60, 5

	out := renderFixedSplit(left, right, listW, previewW, contentH, colorDim)
	cols := findBorderCHAColumns(out)

	if len(cols) != contentH {
		t.Fatalf("expected %d border columns, got %d", contentH, len(cols))
	}
	for i, col := range cols {
		if col != listW+1 {
			t.Errorf("row %d: border at column %d, want %d", i, col, listW+1)
		}
	}
}

func TestRenderFixedSplit_DividerAligned_AmbiguousWidth(t *testing.T) {
	// ○, ●, ■, ✓ are East Asian Ambiguous width — 2 cells on CJK terminals
	left := "○ ■ Task one\n● ✓ Task two\n■ ○ ● mixed\nnormal ascii"
	right := "detail\nmore detail\nfinal"
	listW, previewW, contentH := 30, 40, 6

	out := renderFixedSplit(left, right, listW, previewW, contentH, colorDim)
	cols := findBorderCHAColumns(out)

	if len(cols) != contentH {
		t.Fatalf("expected %d border columns, got %d", contentH, len(cols))
	}
	for i, col := range cols {
		if col != listW+1 {
			t.Errorf("row %d: border at column %d, want %d", i, col, listW+1)
		}
	}
}

func TestRenderFixedSplit_DividerAligned_StyledContent(t *testing.T) {
	bold := lipgloss.NewStyle().Bold(true)
	dim := lipgloss.NewStyle().Faint(true)
	accent := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF0000"))

	left := bold.Render("USER 10:27 #1206 테마 적용해줘") + "\n" +
		dim.Render("ASST 10:28 #1207 Cannot read properties") + "\n" +
		accent.Render("○ ■ Recreate clean deck")
	right := "preview content\nsecond line\nthird"
	listW, previewW, contentH := 45, 55, 5

	out := renderFixedSplit(left, right, listW, previewW, contentH, colorDim)
	cols := findBorderCHAColumns(out)

	if len(cols) != contentH {
		t.Fatalf("expected %d border columns, got %d", contentH, len(cols))
	}
	for i, col := range cols {
		if col != listW+1 {
			t.Errorf("row %d: border at column %d, want %d", i, col, listW+1)
		}
	}
}

func TestRenderFixedSplit_DividerAligned_LongOverflow(t *testing.T) {
	// Left content wider than listW — must be truncated, border still fixed
	left := strings.Repeat("가나다라마바사아자차카타파하", 5) + "\n" +
		strings.Repeat("abcdefghij", 10)
	right := strings.Repeat("오른쪽 내용 ", 20)
	listW, previewW, contentH := 35, 45, 4

	out := renderFixedSplit(left, right, listW, previewW, contentH, colorDim)
	cols := findBorderCHAColumns(out)

	if len(cols) != contentH {
		t.Fatalf("expected %d border columns, got %d", contentH, len(cols))
	}
	for i, col := range cols {
		if col != listW+1 {
			t.Errorf("row %d: border at column %d, want %d", i, col, listW+1)
		}
	}
}

func TestRenderFixedSplit_DividerAligned_Resize(t *testing.T) {
	left := "○ Task A\n● Task B\n■ Task C\n한글 항목"
	right := "preview\ndetail\nmore\n끝"

	// Test at multiple widths to simulate resize
	for _, totalW := range []int{80, 100, 120, 60, 140, 90} {
		listW := totalW * 40 / 100
		previewW := totalW - listW - 1
		contentH := 6

		out := renderFixedSplit(left, right, listW, previewW, contentH, colorDim)
		cols := findBorderCHAColumns(out)

		if len(cols) != contentH {
			t.Fatalf("totalW=%d: expected %d border columns, got %d", totalW, contentH, len(cols))
		}
		for i, col := range cols {
			if col != listW+1 {
				t.Errorf("totalW=%d row %d: border at column %d, want %d", totalW, i, col, listW+1)
			}
		}
	}
}

func TestRenderFixedSplit_DividerAligned_EmptyPanes(t *testing.T) {
	out := renderFixedSplit("", "", 30, 50, 3, colorDim)
	cols := findBorderCHAColumns(out)

	if len(cols) != 3 {
		t.Fatalf("expected 3 border columns, got %d", len(cols))
	}
	for i, col := range cols {
		if col != 31 {
			t.Errorf("row %d: border at column %d, want 31", i, col)
		}
	}
}

func TestTruncateExact_AmbiguousChars(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		target int
	}{
		{"circle", "○ hello", 6},
		{"bullet", "● world", 6},
		{"square", "■ test", 5},
		{"check", "✓ done", 5},
		{"korean", "한글 테스트", 8},
		{"mixed", "○ 한글 test ■", 12},
		{"ascii", "plain text", 8},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, w := truncateExact(tc.input, tc.target)
			if w > tc.target {
				t.Errorf("truncateExact(%q, %d) width=%d exceeds target", tc.input, tc.target, w)
			}
			// Verify StringWidthWc agrees
			actual := ansi.StringWidthWc(result)
			if actual != w {
				t.Errorf("truncateExact(%q, %d) reported w=%d but StringWidthWc=%d", tc.input, tc.target, w, actual)
			}
		})
	}
}
