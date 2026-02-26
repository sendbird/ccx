package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	"github.com/gavin-jeong/csb/internal/session"
)

// SplitPane manages a list + viewport split layout with focus toggling.
type SplitPane struct {
	List    *list.Model
	Preview viewport.Model

	// State
	Show     bool
	Focus    bool   // true = preview focused, false = list focused
	CacheKey string // tracks last-rendered item ID to avoid redundant updates

	// Optional fold support (nil = simple scroll-only preview)
	Folds *FoldState
}

// FoldState holds fold/unfold and block cursor state for previews
// that render structured content blocks.
type FoldState struct {
	Collapsed   foldSet
	Formatted   foldSet
	Entry       session.Entry
	BlockCursor int
	BlockStarts []int
}

// ListWidth returns the list width given total width and split ratio.
func (sp *SplitPane) ListWidth(totalW, splitRatio int) int {
	if !sp.Show {
		return totalW
	}
	return max(totalW*splitRatio/100, 30)
}

// PreviewWidth returns the preview width (totalW - listW - 1 for border).
func (sp *SplitPane) PreviewWidth(totalW, splitRatio int) int {
	return max(totalW-sp.ListWidth(totalW, splitRatio)-1, 1)
}

// ContentHeight returns the usable content height (totalH - 3 for title+help).
func ContentHeight(totalH int) int {
	return max(totalH-3, 1)
}

// Render draws the split layout: list | border | preview.
// If Show is false or dimensions too small, returns list-only view.
func (sp *SplitPane) Render(totalW, totalH, splitRatio int) string {
	if !sp.Show || totalW < 40 || totalH < 10 {
		return sp.List.View()
	}

	listW := sp.ListWidth(totalW, splitRatio)
	previewW := sp.PreviewWidth(totalW, splitRatio)
	contentH := ContentHeight(totalH)

	if sp.List.Width() > 0 && (sp.List.Width() != listW || sp.List.Height() != contentH) {
		sp.List.SetSize(listW, contentH)
	}

	if sp.Preview.Width != previewW || sp.Preview.Height != contentH {
		sp.Preview.Width = previewW
		sp.Preview.Height = max(contentH, 1)
		sp.CacheKey = "" // force re-render at new width
	}

	borderColor := colorBorderDim
	if sp.Focus {
		borderColor = colorBorderFocused
	}

	leftStyle := lipgloss.NewStyle().Width(listW).MaxWidth(listW).Height(contentH).MaxHeight(contentH)
	rightStyle := lipgloss.NewStyle().Width(previewW).MaxWidth(previewW).Height(contentH).MaxHeight(contentH)
	borderStyle := lipgloss.NewStyle().Foreground(borderColor).Height(contentH).MaxHeight(contentH)

	left := leftStyle.Render(sp.List.View())
	border := borderStyle.Render(strings.Repeat("│\n", max(contentH-1, 0)) + "│")
	right := rightStyle.Render(sp.Preview.View())

	return lipgloss.JoinHorizontal(lipgloss.Top, left, border, right)
}

type SplitKeyResult int

const (
	splitKeyUnhandled  SplitKeyResult = iota
	splitKeyHandled                   // handled, no special action
	splitKeyClosed                    // esc closed the preview
	splitKeyOpened                    // right opened the preview
	splitKeyFocused                   // right/tab focused the preview
	splitKeyUnfocused                 // left unfocused the preview
)

// HandleSplitKey processes common split pane keys (esc, left, right, tab, [, ]).
func (sp *SplitPane) HandleSplitKey(key string, totalW, totalH, splitRatio int) SplitKeyResult {
	switch key {
	case "esc":
		if sp.Show {
			idx := sp.List.Index()
			sp.Show = false
			sp.Focus = false
			contentH := ContentHeight(totalH)
			sp.List.SetSize(sp.ListWidth(totalW, splitRatio), contentH)
			sp.List.Select(idx)
			return splitKeyClosed
		}
		return splitKeyUnhandled

	case "tab":
		if !sp.Show {
			return splitKeyUnhandled
		}
		sp.Focus = !sp.Focus
		if sp.Focus {
			return splitKeyFocused
		}
		return splitKeyUnfocused

	case "left":
		if !sp.Focus {
			return splitKeyUnhandled
		}
		// If fold-aware, delegate to fold handler first
		if sp.Folds != nil {
			result := sp.Folds.HandleKey(key)
			if result == foldHandled {
				return splitKeyHandled
			}
			// foldSwitchToList: fall through to unfocus
		}
		sp.Focus = false
		return splitKeyUnfocused

	case "right":
		if !sp.Focus {
			if !sp.Show {
				idx := sp.List.Index()
				sp.Show = true
				sp.CacheKey = ""
				contentH := ContentHeight(totalH)
				sp.List.SetSize(sp.ListWidth(totalW, splitRatio), contentH)
				sp.List.Select(idx)
			}
			sp.Focus = true
			return splitKeyOpened
		}
		// If fold-aware, delegate to fold handler
		if sp.Folds != nil {
			result := sp.Folds.HandleKey(key)
			if result == foldHandled {
				return splitKeyHandled
			}
		}
		return splitKeyHandled
	}

	return splitKeyUnhandled
}

// HandlePreviewScroll processes scroll keys when preview is focused.
func (sp *SplitPane) HandlePreviewScroll(key string) bool {
	if !sp.Focus || !sp.Show {
		return false
	}
	switch key {
	case "pgdown", "pgup", "home", "end":
		scrollPreview(&sp.Preview, key)
		return true
	}
	// For fold-aware panes, up/down move block cursor (handled in HandleFoldNav)
	// For simple panes, up/down scroll
	if sp.Folds == nil {
		switch key {
		case "down", "up":
			scrollPreview(&sp.Preview, key)
			return true
		}
	}
	return false
}

// HandleFoldNav processes fold-specific navigation keys (up/down/f/F) when
// Folds is non-nil and preview is focused.
func (sp *SplitPane) HandleFoldNav(key string) bool {
	if sp.Folds == nil || !sp.Focus || !sp.Show {
		return false
	}
	result := sp.Folds.HandleKey(key)
	return result == foldHandled
}

// Resize adjusts list dimensions after terminal resize.
func (sp *SplitPane) Resize(totalW, totalH, splitRatio int) {
	if sp.List.Width() == 0 {
		return
	}
	idx := sp.List.Index()
	contentH := ContentHeight(totalH)
	sp.List.SetSize(sp.ListWidth(totalW, splitRatio), contentH)
	sp.List.Select(idx)
	sp.CacheKey = "" // force preview re-render
}

// HandleMouseScroll handles mouse wheel events for the split pane.
func (sp *SplitPane) HandleMouseScroll(mouseX int, up bool, totalW, splitRatio int) {
	if sp.Show && mouseX > sp.ListWidth(totalW, splitRatio) {
		mouseScrollVP(&sp.Preview, up)
	} else {
		mouseScrollList(sp.List, up)
	}
}

// HandleMouseClick handles mouse click to toggle focus between list and preview.
func (sp *SplitPane) HandleMouseClick(mouseX, contentY int, totalW, splitRatio int) {
	if sp.Show && mouseX > sp.ListWidth(totalW, splitRatio) {
		sp.Focus = true
	} else {
		sp.Focus = false
		mouseClickList(sp.List, contentY)
	}
}

// SetPreviewContent sets the preview viewport content and resets to top.
func (sp *SplitPane) SetPreviewContent(content string, totalW, totalH, splitRatio int) {
	previewW := sp.PreviewWidth(totalW, splitRatio)
	contentH := ContentHeight(totalH)
	sp.Preview = viewport.New(previewW, contentH)
	sp.Preview.SetContent(content)
}

// RefreshFoldPreview re-renders fold-aware preview content.
func (sp *SplitPane) RefreshFoldPreview(totalW, splitRatio int) {
	if sp.Folds == nil {
		return
	}
	previewW := sp.PreviewWidth(totalW, splitRatio)
	oldOffset := sp.Preview.YOffset

	cursor := -1
	if sp.Focus {
		cursor = sp.Folds.BlockCursor
	}
	rp := renderFullMessageWithCursor(sp.Folds.Entry, previewW, sp.Folds.Collapsed, sp.Folds.Formatted, cursor)
	sp.Folds.BlockStarts = rp.blockStarts

	if sp.Preview.Width != previewW {
		sp.Preview.Width = previewW
	}
	sp.Preview.SetContent(rp.content)

	// Clamp offset to valid range
	totalLines := strings.Count(rp.content, "\n") + 1
	maxOffset := max(totalLines-sp.Preview.Height, 0)
	if oldOffset > maxOffset {
		oldOffset = maxOffset
	}
	sp.Preview.YOffset = oldOffset
}

// ScrollToBlock adjusts the preview viewport to keep the block cursor visible.
func (sp *SplitPane) ScrollToBlock() {
	if sp.Folds == nil {
		return
	}
	fs := sp.Folds
	if fs.BlockCursor < 0 || fs.BlockCursor >= len(fs.BlockStarts) {
		return
	}
	blockLine := fs.BlockStarts[fs.BlockCursor]
	vpHeight := sp.Preview.Height
	totalLines := sp.Preview.TotalLineCount()

	if blockLine < sp.Preview.YOffset {
		sp.Preview.YOffset = max(blockLine-1, 0)
		return
	}
	if blockLine >= sp.Preview.YOffset+vpHeight {
		sp.Preview.YOffset = min(blockLine-vpHeight/2, max(totalLines-vpHeight, 0))
	}
}

// --- FoldState ---

type foldResult int

const (
	foldUnhandled    foldResult = iota
	foldHandled                // key was consumed
	foldSwitchToList           // left on already-folded block
)

// HandleKey processes fold navigation keys.
func (fs *FoldState) HandleKey(key string) foldResult {
	if fs.Collapsed == nil {
		return foldUnhandled
	}
	nBlocks := len(fs.Entry.Content)
	if nBlocks == 0 {
		return foldUnhandled
	}

	switch key {
	case "up":
		if fs.BlockCursor > 0 {
			fs.BlockCursor--
		}
		return foldHandled
	case "down":
		if fs.BlockCursor < nBlocks-1 {
			fs.BlockCursor++
		}
		return foldHandled
	case "left":
		if fs.Formatted != nil && fs.Formatted[fs.BlockCursor] {
			delete(fs.Formatted, fs.BlockCursor)
			return foldHandled
		}
		if !fs.Collapsed[fs.BlockCursor] {
			fs.Collapsed[fs.BlockCursor] = true
			return foldHandled
		}
		return foldSwitchToList
	case "right":
		if fs.Collapsed[fs.BlockCursor] {
			delete(fs.Collapsed, fs.BlockCursor)
			return foldHandled
		}
		if fs.Formatted == nil || !fs.Formatted[fs.BlockCursor] {
			if fs.Formatted == nil {
				fs.Formatted = make(foldSet)
			}
			fs.Formatted[fs.BlockCursor] = true
			return foldHandled
		}
		return foldHandled
	case "pgdown":
		fs.BlockCursor = min(fs.BlockCursor+10, nBlocks-1)
		return foldHandled
	case "pgup":
		fs.BlockCursor = max(fs.BlockCursor-10, 0)
		return foldHandled
	case "home":
		fs.BlockCursor = 0
		return foldHandled
	case "end":
		fs.BlockCursor = nBlocks - 1
		return foldHandled
	case "f":
		fs.Collapsed = defaultFolds(fs.Entry)
		fs.Formatted = nil
		return foldHandled
	case "F":
		fs.Collapsed = make(foldSet)
		fs.Formatted = nil
		return foldHandled
	}
	return foldUnhandled
}

// Reset initializes fold state for a new entry.
func (fs *FoldState) Reset(entry session.Entry) {
	fs.Entry = entry
	fs.Collapsed = defaultFolds(entry)
	fs.Formatted = nil
	fs.BlockCursor = 0
}

// GrowBlocks extends fold defaults for newly-appended blocks (live tail).
func (fs *FoldState) GrowBlocks(entry session.Entry, oldBlockCount int) {
	fs.Entry = entry
	for i := oldBlockCount; i < len(entry.Content); i++ {
		block := entry.Content[i]
		if block.Type == "tool_use" || block.Type == "tool_result" || block.Type == "thinking" {
			fs.Collapsed[i] = true
		}
	}
	if fs.BlockCursor >= len(entry.Content) {
		fs.BlockCursor = max(len(entry.Content)-1, 0)
	}
}
