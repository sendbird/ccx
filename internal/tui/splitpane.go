package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/viewport"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
)

// SplitPane manages a list + viewport split layout with focus toggling.
type SplitPane struct {
	List    *list.Model
	Preview viewport.Model

	// State
	Show     bool
	Focus    bool   // true = preview focused, false = list focused
	CacheKey string // tracks last-rendered item ID to avoid redundant updates

	// Item height for mouse click calculations (delegate Height + Spacing)
	ItemHeight int

	// Optional fold support (nil = simple scroll-only preview)
	Folds *FoldState

	// BottomAlign pushes content to the bottom of viewport when shorter than viewport height.
	// Used during live tailing so new content appears at a stable bottom position.
	BottomAlign bool

	// Persistent per-type fold preferences. Survives across entry changes.
	// Updated when the user manually folds/unfolds blocks via HandleKey.
	TypeFoldPrefs map[string]bool // type → should collapse
	TypeFmtPrefs  map[string]bool // type → should format

	// Render cache: skip re-render when only block cursor moved
	cachedRP    *renderedPreview
	cachedFolds uint64 // hash of collapsed+formatted state at last render
}

// FoldState holds fold/unfold and block cursor state for previews
// that render structured content blocks.
type FoldState struct {
	Collapsed    foldSet
	Formatted    foldSet
	Entry        session.Entry
	BlockCursor  int
	BlockStarts  []int
	BlockVisible []bool   // nil = all visible; non-nil = per-block visibility
	BlockFilter  string   // current filter expression (empty = no filter)
	HideHooks    bool     // true = suppress hook badges/details in render
	Selected     foldSet  // block indices selected for copy
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
		sp.cachedRP = nil // force re-render at new size, but preserve CacheKey (entry identity)
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
	splitKeyUnhandled          SplitKeyResult = iota
	splitKeyHandled                           // handled, no special action
	splitKeyClosed                            // esc closed the preview
	splitKeyOpened                            // right opened the preview
	splitKeyFocused                           // right/tab focused the preview
	splitKeyUnfocused                         // left unfocused the preview
	splitKeySearchFromPreview                 // "/" pressed while preview focused
	splitKeyCursorMoved                       // block cursor moved, no content change
	splitKeyScrolled                          // viewport scrolled, no re-render needed
	splitKeyBoundaryDown                      // block cursor hit bottom boundary
	splitKeyBoundaryUp                        // block cursor hit top boundary
)

// HandleSplitKey processes common split pane keys (esc, left, right, tab, shift+tab, [, ]).
func (sp *SplitPane) HandleSplitKey(key string, totalW, totalH, splitRatio int, adjustRatio func(int)) SplitKeyResult {
	switch key {
	case "esc":
		if sp.Show {
			idx := sp.List.Index()
			sp.Show = false
			sp.Focus = false
			if sp.List.Width() > 0 {
				contentH := ContentHeight(totalH)
				sp.List.SetSize(sp.ListWidth(totalW, splitRatio), contentH)
				sp.List.Select(idx)
			}
			return splitKeyClosed
		}
		return splitKeyUnhandled

	case "tab":
		if sp.Show {
			return splitKeyHandled // no-op when already open (views add mode cycling)
		}
		if sp.List.Width() == 0 {
			return splitKeyUnhandled
		}
		// Open preview without focusing
		idx := sp.List.Index()
		sp.Show = true
		sp.CacheKey = ""
		contentH := ContentHeight(totalH)
		sp.List.SetSize(sp.ListWidth(totalW, splitRatio), contentH)
		sp.List.Select(idx)
		return splitKeyOpened

	case "shift+tab":
		if !sp.Show {
			return splitKeyUnhandled
		}
		// Views handle shift+tab for mode cycling; no-op at split level
		return splitKeyHandled

	case "left":
		if !sp.Focus {
			// List focused: close preview
			if sp.Show {
				idx := sp.List.Index()
				sp.Show = false
				if sp.List.Width() > 0 {
					contentH := ContentHeight(totalH)
					sp.List.SetSize(sp.ListWidth(totalW, splitRatio), contentH)
					sp.List.Select(idx)
				}
				return splitKeyClosed
			}
			return splitKeyUnhandled
		}
		// If fold-aware, delegate to fold handler first
		if sp.Folds != nil {
			result := sp.Folds.HandleKey(key)
			if result == foldHandled {
				sp.SyncTypePrefs(false) // left: don't sync format prefs (unformat is navigation, not intent)
				return splitKeyHandled
			}
			// foldSwitchToList: fall through to unfocus
		}
		sp.Focus = false
		return splitKeyUnfocused

	case "right":
		if !sp.Focus {
			if sp.List.Width() == 0 {
				return splitKeyUnhandled
			}
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
				sp.SyncTypePrefs(true) // right: sync format prefs (explicit format toggle)
				return splitKeyHandled
			}
		}
		return splitKeyHandled

	case "[":
		if sp.Show {
			adjustRatio(-5) // [ always shrinks list ratio (list smaller / preview bigger)
			return splitKeyHandled
		}
		return splitKeyUnhandled

	case "]":
		if sp.Show {
			adjustRatio(5) // ] always grows list ratio (list bigger / preview smaller)
			return splitKeyHandled
		}
		return splitKeyUnhandled
	}

	return splitKeyUnhandled
}

// HandlePreviewScroll processes scroll keys when preview is focused.
func (sp *SplitPane) HandlePreviewScroll(key string) bool {
	if !sp.Focus || !sp.Show {
		return false
	}
	switch key {
	case "up", "down", "pgup", "pgdown", "home", "end":
		scrollPreview(&sp.Preview, key)
		return true
	}
	return false
}

// HandleFocusedKeys processes keys when the preview pane is focused:
// "/" to start search, fold navigation, and scroll keys.
func (sp *SplitPane) HandleFocusedKeys(key string) SplitKeyResult {
	if !sp.Focus || !sp.Show {
		return splitKeyUnhandled
	}
	if key == "/" {
		sp.Focus = false
		return splitKeySearchFromPreview
	}
	if sp.Folds != nil {
		switch HandleFoldNav(sp.Folds, &sp.Preview, key) {
		case NavCursorMoved:
			sp.ScrollToBlock()
			return splitKeyCursorMoved
		case NavFoldChanged:
			sp.SyncTypePrefs(key != "left")
			sp.ScrollToBlock()
			return splitKeyHandled
		case NavSwitchToList:
			sp.Focus = false
			return splitKeyUnfocused
		case NavBoundaryDown:
			return splitKeyBoundaryDown
		case NavBoundaryUp:
			return splitKeyBoundaryUp
		case NavScrolled:
			return splitKeyScrolled
		}
	}
	if sp.HandlePreviewScroll(key) {
		return splitKeyScrolled
	}
	return splitKeyUnhandled
}

// HandleListBoundary handles cursor boundary behavior.
// For up/down: scrolls preview when at first/last item.
// For pgup/pgdown: moves cursor by one page of items (instead of bubbletea's
// page-based navigation), snapping to first/last on edges.
// Returns true if the key was handled.
func (sp *SplitPane) HandleListBoundary(key string) bool {
	items := sp.List.Items()
	if len(items) == 0 {
		return false
	}
	idx := sp.List.Index()

	// Overflow: at last/first item → scroll preview (only when preview is visible)
	if sp.Show && sp.Preview.Height > 0 {
		switch key {
		case "down", "pgdown":
			if idx >= len(items)-1 {
				scrollPreview(&sp.Preview, key)
				return true
			}
		case "up", "pgup":
			if idx <= 0 {
				scrollPreview(&sp.Preview, key)
				return true
			}
		}
	}

	// pgup/pgdown: move cursor by PerPage items (cursor-based, not page-based)
	perPage := max(sp.List.Paginator.PerPage, 1)
	switch key {
	case "pgdown":
		newIdx := min(idx+perPage, len(items)-1)
		sp.List.Select(newIdx)
		return true
	case "pgup":
		newIdx := max(idx-perPage, 0)
		sp.List.Select(newIdx)
		return true
	}
	return false
}

// Resize adjusts list dimensions after terminal resize.
// Preserves entry identity (CacheKey) and fold state; only re-renders preview content.
func (sp *SplitPane) Resize(totalW, totalH, splitRatio int) {
	if sp.List.Width() == 0 {
		return
	}
	idx := sp.List.Index()
	contentH := ContentHeight(totalH)
	sp.List.SetSize(sp.ListWidth(totalW, splitRatio), contentH)
	sp.List.Select(idx)
	sp.cachedRP = nil
	// Re-render preview at new dimensions, preserving fold state and scroll position
	if sp.Show && sp.Folds != nil && len(sp.Folds.Entry.Content) > 0 {
		sp.RefreshFoldPreview(totalW, splitRatio)
	}
}

// HandleMouseScroll handles mouse wheel events for the split pane.
func (sp *SplitPane) HandleMouseScroll(mouseX int, up bool, totalW, splitRatio int) {
	if sp.Show && mouseX > sp.ListWidth(totalW, splitRatio) {
		// Preview side: move block cursor for fold-aware panes, scroll for simple
		if sp.Folds != nil && sp.Focus && len(sp.Folds.BlockStarts) > 0 {
			if up {
				sp.Folds.HandleKey("up")
			} else {
				sp.Folds.HandleKey("down")
			}
			sp.ScrollToBlock()
		} else {
			mouseScrollVP(&sp.Preview, up)
		}
	} else {
		mouseScrollList(sp.List, up)
	}
}

// HandleMouseClick handles mouse click to toggle focus between list and preview.
func (sp *SplitPane) HandleMouseClick(mouseX, contentY int, totalW, splitRatio int) {
	if sp.Show && mouseX > sp.ListWidth(totalW, splitRatio) {
		sp.Focus = true
		// For fold-aware panes, move block cursor to clicked block
		if sp.Folds != nil && len(sp.Folds.BlockStarts) > 0 {
			clickedLine := sp.Preview.YOffset + contentY
			sp.Folds.SelectBlockAtLine(clickedLine)
		}
	} else {
		sp.Focus = false
		mouseClickList(sp.List, contentY, sp.ItemHeight)
	}
}

// HandleMouseDoubleClick handles double-click in the preview to toggle fold.
func (sp *SplitPane) HandleMouseDoubleClick(mouseX int, totalW, splitRatio int) bool {
	if !sp.Show || mouseX <= sp.ListWidth(totalW, splitRatio) {
		return false
	}
	if sp.Folds == nil || len(sp.Folds.BlockStarts) == 0 {
		return false
	}
	// Toggle fold on current block cursor
	bc := sp.Folds.BlockCursor
	if sp.Folds.Collapsed[bc] {
		delete(sp.Folds.Collapsed, bc)
	} else {
		sp.Folds.Collapsed[bc] = true
	}
	return true
}

// SetPreviewContent sets the preview viewport content and resets to top.
func (sp *SplitPane) SetPreviewContent(content string, totalW, totalH, splitRatio int) {
	previewW := sp.PreviewWidth(totalW, splitRatio)
	contentH := ContentHeight(totalH)
	sp.Preview = viewport.New(previewW, contentH)
	sp.Preview.SetContent(content)
}

// RefreshFoldPreview re-renders fold-aware preview content.
// It clamps the existing YOffset to the new content bounds and scrolls
// to keep the block cursor visible.  Callers that need proportional
// scroll preservation (e.g. resize) should do so explicitly afterwards.
func (sp *SplitPane) RefreshFoldPreview(totalW, splitRatio int) {
	if sp.Folds == nil || len(sp.Folds.Entry.Content) == 0 {
		return
	}
	previewW := sp.PreviewWidth(totalW, splitRatio)
	oldOffset := sp.Preview.YOffset

	cursor := sp.Folds.BlockCursor
	ro := renderOpts{visible: sp.Folds.BlockVisible, hideHooks: sp.Folds.HideHooks, selected: sp.Folds.Selected}
	rp := renderFullMessageWithCursor(sp.Folds.Entry, previewW, sp.Folds.Collapsed, sp.Folds.Formatted, cursor, ro)
	sp.Folds.BlockStarts = rp.blockStarts
	sp.cachedRP = &rp
	sp.cachedFolds = foldHash(sp.Folds.Collapsed, sp.Folds.Formatted)

	if sp.Preview.Width != previewW {
		sp.Preview.Width = previewW
	}

	content := rp.content
	padLines := 0
	if sp.BottomAlign && rp.lineCount < sp.Preview.Height {
		padLines = sp.Preview.Height - rp.lineCount
		content = strings.Repeat("\n", padLines) + content
		// Shift block starts to account for padding
		for i := range sp.Folds.BlockStarts {
			sp.Folds.BlockStarts[i] += padLines
		}
	}
	sp.Preview.SetContent(content)

	// Clamp old offset to new content bounds (no proportional restoration)
	maxOffset := max(sp.Preview.TotalLineCount()-sp.Preview.Height, 0)
	if oldOffset > maxOffset {
		oldOffset = maxOffset
	}
	sp.Preview.YOffset = oldOffset

	// Ensure block cursor is visible after content update
	sp.ScrollToBlock()
}

// RefreshFoldCursor re-renders only the block cursor markers without re-computing
// wrapped text. Falls back to full RefreshFoldPreview if fold state changed.
func (sp *SplitPane) RefreshFoldCursor(totalW, splitRatio int) {
	if sp.Folds == nil || len(sp.Folds.Entry.Content) == 0 {
		return
	}
	// If fold state changed or no cache, do full re-render
	h := foldHash(sp.Folds.Collapsed, sp.Folds.Formatted)
	if sp.cachedRP == nil || h != sp.cachedFolds || sp.PreviewWidth(totalW, splitRatio) != sp.Preview.Width {
		sp.RefreshFoldPreview(totalW, splitRatio)
		return
	}

	// Fold state unchanged — re-render with new cursor position only
	previewW := sp.PreviewWidth(totalW, splitRatio)
	cursor := sp.Folds.BlockCursor
	ro := renderOpts{visible: sp.Folds.BlockVisible, hideHooks: sp.Folds.HideHooks, selected: sp.Folds.Selected}
	rp := renderFullMessageWithCursor(sp.Folds.Entry, previewW, sp.Folds.Collapsed, sp.Folds.Formatted, cursor, ro)
	sp.Folds.BlockStarts = rp.blockStarts
	sp.cachedRP = &rp

	oldOffset := sp.Preview.YOffset

	content := rp.content
	padLines := 0
	if sp.BottomAlign && rp.lineCount < sp.Preview.Height {
		padLines = sp.Preview.Height - rp.lineCount
		content = strings.Repeat("\n", padLines) + content
		for i := range sp.Folds.BlockStarts {
			sp.Folds.BlockStarts[i] += padLines
		}
	}
	sp.Preview.SetContent(content)

	totalLines := sp.Preview.TotalLineCount()
	maxOffset := max(totalLines-sp.Preview.Height, 0)
	if oldOffset > maxOffset {
		oldOffset = maxOffset
	}
	sp.Preview.YOffset = oldOffset

	// Ensure block cursor is visible after content update
	sp.ScrollToBlock()
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

// foldHash computes a simple hash of collapsed+formatted state for cache invalidation.
func foldHash(collapsed, formatted foldSet) uint64 {
	var h uint64
	for k := range collapsed {
		h ^= uint64(k)*2654435761 + 1
	}
	for k := range formatted {
		h ^= uint64(k)*2654435761 + 0x9e3779b9
	}
	return h
}

// SyncTypePrefs merges fold/format preferences from the current FoldState
// into the SplitPane's persistent TypeFoldPrefs/TypeFmtPrefs maps.
// Only updates prefs for block types present in the current entry,
// preserving prefs for types not in this entry (e.g., tool_use prefs
// survive while viewing a text-only user message).
//
// syncFmt controls whether format (Formatted) preferences are updated.
// Pass false for "left" key actions — left progressively collapses
// (unformat → fold → switch-to-list), and the intermediate unformat
// step should NOT clear the persistent format preference. Pass true
// for "right" (explicit format toggle) and "f"/"F" (reset all folds).
func (sp *SplitPane) SyncTypePrefs(syncFmt bool) {
	if sp.Folds == nil || len(sp.Folds.Entry.Content) == 0 {
		return
	}
	debugLog.Printf("SyncTypePrefs: BEFORE foldPrefs=%v fmtPrefs=%v collapsed=%v formatted=%v",
		sp.TypeFoldPrefs, sp.TypeFmtPrefs, sp.Folds.Collapsed, sp.Folds.Formatted)
	if sp.TypeFoldPrefs == nil {
		sp.TypeFoldPrefs = make(map[string]bool)
	}
	// Collect per-type fold state from current entry (any-unfolded wins)
	localFold := make(map[string]bool)
	for i, block := range sp.Folds.Entry.Content {
		bt := block.Type
		if prev, seen := localFold[bt]; !seen {
			localFold[bt] = sp.Folds.Collapsed[i]
		} else if prev && !sp.Folds.Collapsed[i] {
			localFold[bt] = false
		}
	}
	// Merge into persistent prefs (only types present in current entry)
	for bt, collapsed := range localFold {
		sp.TypeFoldPrefs[bt] = collapsed
	}

	// Collect per-type format state from current entry (only when syncFmt is true)
	if syncFmt {
		if sp.TypeFmtPrefs == nil {
			sp.TypeFmtPrefs = make(map[string]bool)
		}
		localFmt := make(map[string]bool)
		seenTypes := make(map[string]bool)
		for i, block := range sp.Folds.Entry.Content {
			seenTypes[block.Type] = true
			if sp.Folds.Formatted != nil && sp.Folds.Formatted[i] {
				localFmt[block.Type] = true
			}
		}
		// Merge: update only types present in current entry
		for bt := range seenTypes {
			if localFmt[bt] {
				sp.TypeFmtPrefs[bt] = true
			} else {
				delete(sp.TypeFmtPrefs, bt)
			}
		}
	}
	debugLog.Printf("SyncTypePrefs: AFTER foldPrefs=%v fmtPrefs=%v", sp.TypeFoldPrefs, sp.TypeFmtPrefs)
}

// --- FoldState ---

type foldResult int

const (
	foldUnhandled    foldResult = iota
	foldHandled                // key was consumed, content changed (fold/unfold/format)
	foldCursorMoved            // key was consumed, only cursor position changed
	foldSwitchToList           // left on already-folded block
	foldBoundaryDown           // down at last visible block
	foldBoundaryUp             // up at first visible block
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
		next := fs.prevVisibleBlock(fs.BlockCursor)
		if next >= 0 {
			fs.BlockCursor = next
			return foldCursorMoved
		}
		return foldBoundaryUp
	case "down":
		next := fs.nextVisibleBlock(fs.BlockCursor)
		if next >= 0 {
			fs.BlockCursor = next
			return foldCursorMoved
		}
		return foldBoundaryDown
	case "left":
		// Remove formatting first, then fold, then switch to list
		if fs.Formatted != nil && fs.Formatted[fs.BlockCursor] {
			delete(fs.Formatted, fs.BlockCursor)
			return foldHandled
		}
		if !fs.Collapsed[fs.BlockCursor] {
			block := fs.Entry.Content[fs.BlockCursor]
			if block.Type == "tool_use" || block.Type == "tool_result" || block.Type == "thinking" {
				fs.Collapsed[fs.BlockCursor] = true
				return foldHandled
			}
		}
		return foldSwitchToList
	case "right":
		// Unfold current block, or toggle formatting if already expanded
		if fs.Collapsed[fs.BlockCursor] {
			delete(fs.Collapsed, fs.BlockCursor)
			return foldHandled
		}
		block := fs.Entry.Content[fs.BlockCursor]
		if block.Type == "tool_use" || block.Type == "tool_result" || block.Type == "thinking" || block.Type == "text" {
			if fs.Formatted == nil {
				fs.Formatted = make(foldSet)
			}
			if !fs.Formatted[fs.BlockCursor] {
				fs.Formatted[fs.BlockCursor] = true
				return foldHandled
			}
			delete(fs.Formatted, fs.BlockCursor)
			return foldHandled
		}
		return foldHandled
	case "pgdown", "pgup":
		// Fall through to viewport scroll — lets user read long blocks page by page
		return foldUnhandled
	case "home":
		first := fs.firstVisibleBlock()
		if first >= 0 && fs.BlockCursor != first {
			fs.BlockCursor = first
			return foldCursorMoved
		}
		return foldUnhandled
	case "end":
		last := fs.lastVisibleBlock()
		if last >= 0 && fs.BlockCursor != last {
			fs.BlockCursor = last
			return foldCursorMoved
		}
		return foldUnhandled
	case " ":
		// Toggle block selection for copy
		if fs.Selected == nil {
			fs.Selected = make(foldSet)
		}
		if fs.Selected[fs.BlockCursor] {
			delete(fs.Selected, fs.BlockCursor)
		} else {
			fs.Selected[fs.BlockCursor] = true
		}
		// Auto-advance to next block
		if next := fs.nextVisibleBlock(fs.BlockCursor); next >= 0 {
			fs.BlockCursor = next
		}
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

// isBlockVisible returns whether block i is visible under the current filter.
func (fs *FoldState) isBlockVisible(i int) bool {
	if fs.BlockVisible == nil {
		return true
	}
	if i < 0 || i >= len(fs.BlockVisible) {
		return false
	}
	return fs.BlockVisible[i]
}

// nextVisibleBlock returns the next visible block index after current, or -1.
func (fs *FoldState) nextVisibleBlock(current int) int {
	for i := current + 1; i < len(fs.Entry.Content); i++ {
		if fs.isBlockVisible(i) {
			return i
		}
	}
	return -1
}

// prevVisibleBlock returns the previous visible block index before current, or -1.
func (fs *FoldState) prevVisibleBlock(current int) int {
	for i := current - 1; i >= 0; i-- {
		if fs.isBlockVisible(i) {
			return i
		}
	}
	return -1
}

// firstVisibleBlock returns the first visible block index, or -1.
func (fs *FoldState) firstVisibleBlock() int {
	for i := 0; i < len(fs.Entry.Content); i++ {
		if fs.isBlockVisible(i) {
			return i
		}
	}
	return -1
}

// lastVisibleBlock returns the last visible block index, or -1.
func (fs *FoldState) lastVisibleBlock() int {
	for i := len(fs.Entry.Content) - 1; i >= 0; i-- {
		if fs.isBlockVisible(i) {
			return i
		}
	}
	return -1
}

// SelectBlockAtLine moves the block cursor to the block containing the given line.
func (fs *FoldState) SelectBlockAtLine(line int) {
	if len(fs.BlockStarts) == 0 {
		return
	}
	// Find the last block whose start line is <= the clicked line
	best := 0
	for i, start := range fs.BlockStarts {
		if start <= line {
			best = i
		} else {
			break
		}
	}
	fs.BlockCursor = best
}

// Reset initializes fold state for a new entry (cold start, no preferences).
func (fs *FoldState) Reset(entry session.Entry) {
	fs.Entry = entry
	fs.Collapsed = defaultFolds(entry)
	fs.Formatted = nil
	fs.BlockCursor = 0
	fs.Selected = nil
}

// ResetWithPrefs initializes fold state for a new entry using persistent
// per-type preferences from the SplitPane. If prefs are nil, falls back to defaults.
func (fs *FoldState) ResetWithPrefs(entry session.Entry, foldPrefs map[string]bool, fmtPrefs map[string]bool) {
	fs.Entry = entry
	fs.Collapsed = make(foldSet)
	fs.BlockCursor = 0

	// Log block types for debugging
	types := make([]string, len(entry.Content))
	for i, b := range entry.Content {
		types[i] = b.Type
	}
	debugLog.Printf("  ResetWithPrefs: blockTypes=%v foldPrefs=%v fmtPrefs=%v", types, foldPrefs, fmtPrefs)

	for i, block := range entry.Content {
		if foldPrefs != nil {
			if pref, ok := foldPrefs[block.Type]; ok {
				if pref {
					fs.Collapsed[i] = true
				}
				continue
			}
		}
		// No preference — use default
		if block.Type == "tool_use" || block.Type == "tool_result" || block.Type == "thinking" {
			fs.Collapsed[i] = true
		}
	}

	// Apply formatted preferences
	if fmtPrefs != nil && len(fmtPrefs) > 0 {
		fs.Formatted = make(foldSet)
		for i, block := range entry.Content {
			if fmtPrefs[block.Type] {
				fs.Formatted[i] = true
			}
		}
		if len(fs.Formatted) == 0 {
			fs.Formatted = nil
		}
	} else {
		fs.Formatted = nil
	}
}

// GrowBlocks extends fold defaults for newly-appended blocks (live tail).
// Uses persistent foldPrefs/fmtPrefs if available, otherwise defaults.
func (fs *FoldState) GrowBlocks(entry session.Entry, oldBlockCount int, foldPrefs map[string]bool, fmtPrefs map[string]bool) {
	fs.Entry = entry
	for i := oldBlockCount; i < len(entry.Content); i++ {
		block := entry.Content[i]
		collapsed := false
		if foldPrefs != nil {
			if pref, ok := foldPrefs[block.Type]; ok {
				collapsed = pref
			} else if block.Type == "tool_use" || block.Type == "tool_result" || block.Type == "thinking" {
				collapsed = true
			}
		} else if block.Type == "tool_use" || block.Type == "tool_result" || block.Type == "thinking" {
			collapsed = true
		}
		if collapsed {
			fs.Collapsed[i] = true
		}
		// Apply format prefs to unfolded blocks
		if !collapsed && fmtPrefs != nil && fmtPrefs[block.Type] {
			if fs.Formatted == nil {
				fs.Formatted = make(foldSet)
			}
			fs.Formatted[i] = true
		}
	}
	if fs.BlockCursor >= len(entry.Content) {
		fs.BlockCursor = max(len(entry.Content)-1, 0)
	}
}
