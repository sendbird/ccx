package tui

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
)

// navFrame stores state for navigating back from an agent drill-down.
type navFrame struct {
	sess     session.Session
	messages []session.Entry
	merged   []mergedMsg
	agents   []session.Subagent
	items    []convItem
	listIdx  int // cursor position to restore
	agent    session.Subagent
	task     session.TaskItem
	fromView viewState // which view pushed this frame
}

// openMsgFullForEntry opens viewMessageFull for a specific merged message.
func (a *App) openMsgFullForEntry(m mergedMsg) (tea.Model, tea.Cmd) {
	a.msgFull.sess = a.currentSess
	a.msgFull.agent = session.Subagent{}
	a.msgFull.messages = a.conv.messages
	a.msgFull.merged = a.conv.merged
	a.msgFull.agents = a.conv.agents

	// Find the index of this merged message
	idx := 0
	for i, mm := range a.msgFull.merged {
		if mm.startIdx == m.startIdx {
			idx = i
			break
		}
	}
	a.msgFull.idx = idx
	a.navToMsgFull(idx)
	a.state = viewMessageFull
	return a, nil
}

// navToMsgFull sets up the viewport and fold state for message at index.
func (a *App) navToMsgFull(idx int) {
	if idx < 0 || idx >= len(a.msgFull.merged) {
		return
	}
	a.msgFull.idx = idx
	entry := a.msgFull.merged[idx].entry

	a.msgFull.folds = FoldState{}
	a.msgFull.folds.Reset(entry)

	contentH := ContentHeight(a.height)
	content := renderFullMessage(entry, a.width)
	a.msgFull.content = content

	a.msgFull.vp = viewport.New(a.width, contentH)

	// Render with block cursor
	rp := renderFullMessageWithCursor(entry, a.width, a.msgFull.folds.Collapsed, a.msgFull.folds.Formatted, a.msgFull.folds.BlockCursor)
	a.msgFull.folds.BlockStarts = rp.blockStarts
	a.msgFull.vp.SetContent(rp.content)
}

// handleMessageFullKeys handles keyboard input for viewMessageFull.
func (a *App) handleMessageFullKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Copy mode intercepts all keys
	if a.copyModeActive {
		return a.handleCopyModeKeys(msg)
	}

	// Block filter input intercepts all keys
	if a.msgFull.blockFiltering {
		return a.handleMsgFullBlockFilterInput(msg)
	}

	// Search input mode intercepts all keys
	if a.msgFull.searching {
		return a.handleMsgFullSearchInput(msg)
	}

	key := msg.String()

	// Translate navigation aliases (vim hjkl, etc.)
	if nav, navMsg := a.keymap.TranslateNav(key, msg); nav != "" {
		key = nav
		msg = navMsg
	}

	// Actions menu
	if a.convActionsMenu {
		return a.handleConvActionsMenu(key)
	}

	switch key {
	case "q":
		return a, tea.Quit
	case "esc":
		// Clear block selection first
		if len(a.msgFull.folds.Selected) > 0 {
			a.msgFull.folds.Selected = nil
			a.refreshMsgFullPreview()
			return a, nil
		}
		// Clear block filter
		if a.msgFull.folds.BlockFilter != "" {
			a.clearMsgFullBlockFilter()
			return a, nil
		}
		if a.msgFull.searchTerm != "" {
			a.clearMsgFullSearch()
			return a, nil
		}
		return a.popNavFrame()
	case "v":
		a.enterMsgFullCopyMode()
		return a, nil
	case "y":
		a.copyMsgFullBlocks()
		return a, nil
	case "L":
		return a.toggleLiveTail()
	case "/":
		if a.msgFull.allMessages {
			a.startMsgFullSearch()
		} else {
			a.startMsgFullBlockFilter()
		}
		return a, nil
	case "x":
		a.convActionsMenu = true
		return a, nil
	}

	// Search navigation (when search term is active)
	if a.msgFull.searchTerm != "" {
		switch key {
		case "n":
			a.nextMsgFullSearchMatch()
			return a, nil
		case "N":
			a.prevMsgFullSearchMatch()
			return a, nil
		}
	}

	// allMessages mode: only scroll, no blocks/folds/navigation
	if a.msgFull.allMessages {
		switch key {
		case "up", "down", "pgup", "pgdown", "home", "end":
			scrollPreview(&a.msgFull.vp, key)
		}
		return a, nil
	}

	switch key {
	case "n", "]":
		// Next message
		if a.msgFull.idx < len(a.msgFull.merged)-1 {
			a.navToMsgFull(a.msgFull.idx + 1)
		}
		return a, nil
	case "N", "[":
		// Previous message
		if a.msgFull.idx > 0 {
			a.navToMsgFull(a.msgFull.idx - 1)
		}
		return a, nil
	case "enter":
		// On actionable blocks: image open, agent drill-down
		fs := &a.msgFull.folds
		if fs.BlockCursor >= 0 && fs.BlockCursor < len(fs.Entry.Content) {
			block := fs.Entry.Content[fs.BlockCursor]
			if block.Type == "image" && block.ImagePasteID > 0 {
				return a.openCachedImage(block.ImagePasteID)
			}
			if block.Type == "tool_use" && block.ToolName == "Task" {
				if agent, found := a.findAgentInMsgFull(fs.Entry); found {
					a.pushMsgFullFrame()
					return a.openAgentConversation(agent)
				}
			}
		}
		return a, nil
	case "i":
		return a.openMessageImage()
	}

	// Fold navigation
	fs := &a.msgFull.folds
	fr := fs.HandleKey(key)
	if fr == foldCursorMoved || fr == foldHandled {
		a.refreshMsgFullPreview()
		return a, nil
	}

	// Scroll viewport
	switch key {
	case "up", "down", "pgup", "pgdown", "home", "end":
		scrollPreview(&a.msgFull.vp, key)
		return a, nil
	}

	return a, nil
}

// handleLiveTailMsgFull refreshes the message detail view during live tail.
// Re-loads messages from disk; if the current message (typically the last one)
// has new content blocks, grow the fold state and scroll to the bottom.
func (a *App) handleLiveTailMsgFull() {
	oldMergedLen := len(a.msgFull.merged)
	oldIdx := a.msgFull.idx
	entries, err := session.LoadMessages(a.msgFull.sess.FilePath)
	if err != nil {
		return
	}
	a.msgFull.messages = entries
	a.msgFull.merged = filterConversation(mergeConversationTurns(entries))

	if len(a.msgFull.merged) == 0 {
		return
	}

	if a.msgFull.allMessages {
		a.msgFull.content = renderAllMessages(a.msgFull.merged, a.width)
		content := a.msgFull.content
		if a.msgFull.searchTerm != "" {
			a.buildMsgFullSearchMatches()
			content = highlightSearchMatches(content, a.msgFull.searchTerm, a.msgFullCurrentMatchLine())
		}
		a.msgFull.vp.SetContent(content)
		a.msgFull.vp.YOffset = max(a.msgFull.vp.TotalLineCount()-a.msgFull.vp.Height, 0)
		return
	}

	// If new messages appeared and we were on the last one, follow to new last
	wasLast := oldMergedLen == 0 || oldIdx >= oldMergedLen-1
	if wasLast {
		a.msgFull.idx = len(a.msgFull.merged) - 1
	}
	// Clamp idx
	if a.msgFull.idx >= len(a.msgFull.merged) {
		a.msgFull.idx = len(a.msgFull.merged) - 1
	}

	newEntry := a.msgFull.merged[a.msgFull.idx].entry
	fs := &a.msgFull.folds
	oldEntry := fs.Entry
	oldBlockCount := len(oldEntry.Content)
	newBlockCount := len(newEntry.Content)

	if newEntry.Role == oldEntry.Role && reflect.DeepEqual(newEntry.Content, oldEntry.Content) {
		// No change — nothing to update
		return
	}

	if oldBlockCount > 0 && newBlockCount > oldBlockCount {
		// Grow: preserve existing fold state, add defaults for new blocks
		fs.GrowBlocks(newEntry, oldBlockCount, nil, nil)
	} else {
		// Reset (new message or shrunk): full re-init
		fs.Reset(newEntry)
	}

	// Re-render and scroll to bottom
	ro := renderOpts{visible: fs.BlockVisible, hideHooks: fs.HideHooks, selected: fs.Selected}
	rp := renderFullMessageWithCursor(fs.Entry, a.width, fs.Collapsed, fs.Formatted, fs.BlockCursor, ro)
	fs.BlockStarts = rp.blockStarts
	a.msgFull.content = rp.content
	a.msgFull.vp.SetContent(rp.content)

	// Move block cursor to last block and scroll to bottom
	if len(fs.Entry.Content) > 0 {
		fs.BlockCursor = len(fs.Entry.Content) - 1
	}
	total := a.msgFull.vp.TotalLineCount()
	maxOffset := max(total-a.msgFull.vp.Height, 0)
	a.msgFull.vp.YOffset = maxOffset
}

// refreshMsgFullPreview re-renders the message full viewport.
func (a *App) refreshMsgFullPreview() {
	fs := &a.msgFull.folds
	ro := renderOpts{visible: fs.BlockVisible, hideHooks: fs.HideHooks, selected: fs.Selected}
	rp := renderFullMessageWithCursor(fs.Entry, a.width, fs.Collapsed, fs.Formatted, fs.BlockCursor, ro)
	fs.BlockStarts = rp.blockStarts

	oldOffset := a.msgFull.vp.YOffset
	content := rp.content
	if a.msgFull.searchTerm != "" {
		content = highlightSearchMatches(content, a.msgFull.searchTerm, a.msgFullCurrentMatchLine())
	}
	a.msgFull.vp.SetContent(content)

	maxOffset := max(a.msgFull.vp.TotalLineCount()-a.msgFull.vp.Height, 0)
	if oldOffset > maxOffset {
		oldOffset = maxOffset
	}
	a.msgFull.vp.YOffset = oldOffset

	// Scroll to keep block cursor visible
	if fs.BlockCursor >= 0 && fs.BlockCursor < len(fs.BlockStarts) {
		blockLine := fs.BlockStarts[fs.BlockCursor]
		if blockLine < a.msgFull.vp.YOffset {
			a.msgFull.vp.YOffset = max(blockLine-1, 0)
		} else if blockLine >= a.msgFull.vp.YOffset+a.msgFull.vp.Height {
			a.msgFull.vp.YOffset = min(blockLine-a.msgFull.vp.Height/2, maxOffset)
		}
	}
}

// renderMessageFull renders the full-screen message detail view.
func (a *App) renderMessageFull() string {
	// Clamp YOffset to prevent out-of-bounds panic after content change
	maxOffset := max(a.msgFull.vp.TotalLineCount()-a.msgFull.vp.Height, 0)
	if a.msgFull.vp.YOffset > maxOffset {
		a.msgFull.vp.YOffset = maxOffset
	}
	return a.msgFull.vp.View()
}

// --- Block filter for full-screen message view ---

func (a *App) startMsgFullBlockFilter() {
	ti := textinput.New()
	ti.Prompt = "Filter: "
	ti.Placeholder = "is:hook is:tool tool:Grep is:error ..."
	ti.CharLimit = 200
	ti.Width = a.width - 20
	if a.msgFull.folds.BlockFilter != "" {
		ti.SetValue(a.msgFull.folds.BlockFilter)
	}
	ti.Focus()
	a.msgFull.blockFilterTI = ti
	a.msgFull.blockFiltering = true
}

func (a *App) handleMsgFullBlockFilterInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "enter":
		a.commitMsgFullBlockFilter()
		return a, nil
	case "esc":
		a.msgFull.blockFiltering = false
		return a, nil
	}
	var cmd tea.Cmd
	a.msgFull.blockFilterTI, cmd = a.msgFull.blockFilterTI.Update(msg)
	return a, cmd
}

func (a *App) commitMsgFullBlockFilter() {
	a.msgFull.blockFiltering = false
	fs := &a.msgFull.folds
	filter := a.msgFull.blockFilterTI.Value()
	fs.BlockFilter = filter
	fs.BlockVisible = applyBlockFilter(filter, fs.Entry)
	if first := fs.firstVisibleBlock(); first >= 0 {
		fs.BlockCursor = first
	}
	a.refreshMsgFullPreview()
	a.msgFull.vp.YOffset = 0
}

func (a *App) clearMsgFullBlockFilter() {
	fs := &a.msgFull.folds
	fs.BlockFilter = ""
	fs.BlockVisible = nil
	a.refreshMsgFullPreview()
}

// renderMsgFullSearchHintBox renders a floating hint box for the full-screen message search.
func renderMsgFullSearchHintBox() string {
	h := lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8"))
	d := dimStyle

	lines := []string{
		d.Render("text search across rendered content"),
		h.Render("n/N") + d.Render(": next/prev match after search"),
	}

	body := strings.Join(lines, "\n")
	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorDim).
		Padding(0, 1)
	return boxStyle.Render(body)
}

// pushNavFrame saves current conversation state onto the nav stack.
func (a *App) pushNavFrame() {
	frame := navFrame{
		sess:     a.conv.sess,
		messages: a.conv.messages,
		merged:   a.conv.merged,
		agents:   a.conv.agents,
		items:    a.conv.items,
		listIdx:  a.convList.Index(),
		agent:    a.conv.agent,
		task:     a.conv.task,
		fromView: a.state,
	}
	a.navStack = append(a.navStack, frame)
}

// pushMsgFullFrame saves current msgFull state for recursive agent drill-down.
func (a *App) pushMsgFullFrame() {
	// Store the current msgFull context as a nav frame
	frame := navFrame{
		sess:     a.msgFull.sess,
		messages: a.msgFull.messages,
		merged:   a.msgFull.merged,
		agents:   a.msgFull.agents,
		listIdx:  a.msgFull.idx,
		fromView: viewMessageFull,
	}
	a.navStack = append(a.navStack, frame)
}

// popNavFrame restores the previous view from the nav stack.
func (a *App) popNavFrame() (tea.Model, tea.Cmd) {
	if len(a.navStack) == 0 {
		// No stack: go back to conversation or sessions
		if a.state == viewMessageFull {
			a.msgFull.allMessages = false
			a.state = viewConversation
			a.updateConvPreview()
			return a, nil
		}
		// viewConversation with agent → back to sessions
		a.conv.agent = session.Subagent{}
		a.state = viewSessions
		return a, nil
	}

	frame := a.navStack[len(a.navStack)-1]
	a.navStack = a.navStack[:len(a.navStack)-1]

	// Restore to conversation view
	if frame.fromView == viewConversation {
		a.conv.sess = frame.sess
		a.conv.messages = frame.messages
		a.conv.merged = frame.merged
		a.conv.agents = frame.agents
		a.conv.items = frame.items
		a.conv.agent = frame.agent
		a.conv.task = frame.task
		a.msgFull.allMessages = false

		contentH := ContentHeight(a.height)
		a.convList = newConvList(a.conv.items, a.conv.split.ListWidth(a.width, a.splitRatio), contentH)
		a.conv.split.List = &a.convList

		if frame.listIdx < len(a.conv.items) {
			a.convList.Select(frame.listIdx)
		}
		a.conv.split.CacheKey = ""
		a.state = viewConversation
		a.updateConvPreview()
		return a, nil
	}

	// Pop back to parent msgFull (recursive agent)
	a.msgFull.sess = frame.sess
	a.msgFull.messages = frame.messages
	a.msgFull.merged = frame.merged
	a.msgFull.agents = frame.agents
	a.msgFull.allMessages = false
	a.navToMsgFull(frame.listIdx)
	a.state = viewMessageFull
	return a, nil
}

// findAgentInMsgFull finds the agent matching a Task tool_use in the current msgFull context.
func (a *App) findAgentInMsgFull(entry session.Entry) (session.Subagent, bool) {
	agents := a.msgFull.agents
	if len(agents) == 0 {
		return session.Subagent{}, false
	}
	if entry.Timestamp.IsZero() {
		return session.Subagent{}, false
	}

	var best session.Subagent
	bestDiff := float64(1e18)
	for _, ag := range agents {
		if ag.Timestamp.IsZero() {
			continue
		}
		diff := ag.Timestamp.Sub(entry.Timestamp).Seconds()
		if diff >= -5 && diff < 60 {
			absDiff := diff
			if absDiff < 0 {
				absDiff = -absDiff
			}
			if absDiff < bestDiff {
				bestDiff = absDiff
				best = ag
			}
		}
	}
	if bestDiff < 1e18 {
		return best, true
	}
	return session.Subagent{}, false
}

// copyMsgFullBlocks copies selected blocks (if any) or the entire message content.
func (a *App) copyMsgFullBlocks() {
	fs := &a.msgFull.folds
	if len(fs.Selected) > 0 {
		// Copy only selected blocks' raw text
		var parts []string
		for i, block := range fs.Entry.Content {
			if !fs.Selected[i] {
				continue
			}
			text := blockPlainText(block)
			if text != "" {
				parts = append(parts, text)
			}
		}
		joined := strings.Join(parts, "\n\n")
		copyToClipboard(joined)
		n := len(fs.Selected)
		a.copiedMsg = fmt.Sprintf("Copied %d block", n)
		if n != 1 {
			a.copiedMsg += "s"
		}
		a.copiedMsg += "!"
		// Clear selection after copy
		fs.Selected = nil
		a.refreshMsgFullPreview()
		return
	}
	// No selection: copy all
	copyToClipboard(stripANSI(a.msgFull.content))
	a.copiedMsg = "Copied!"
}

// blockPlainText extracts the plain text content of a single block.
func blockPlainText(b session.ContentBlock) string {
	switch b.Type {
	case "text":
		return strings.TrimSpace(session.StripXMLTags(b.Text))
	case "tool_use":
		header := "Tool: " + b.ToolName
		if b.ToolInput != "" {
			header += "  " + b.ToolInput
		}
		return header
	case "tool_result":
		return strings.TrimSpace(b.Text)
	case "thinking":
		return strings.TrimSpace(b.Text)
	default:
		return strings.TrimSpace(b.Text)
	}
}

// enterMsgFullCopyMode enters copy mode for the message full view.
func (a *App) enterMsgFullCopyMode() {
	a.copyLines = strings.Split(stripANSI(a.msgFull.content), "\n")
	a.copyModeActive = true

	// Start cursor at current block position (if available), fall back to viewport offset
	cursor := a.msgFull.vp.YOffset
	fs := &a.msgFull.folds
	if fs.BlockCursor >= 0 && fs.BlockCursor < len(fs.BlockStarts) {
		cursor = fs.BlockStarts[fs.BlockCursor]
	}
	a.copyCursor = cursor

	a.copyAnchor = -1
	a.renderMsgFullCopyMode()
}

// renderMsgFullCopyMode renders copy mode overlay on msgFull viewport.
func (a *App) renderMsgFullCopyMode() {
	offset := a.msgFull.vp.YOffset
	selStart, selEnd := a.copySelRange()

	var sb strings.Builder
	for i, line := range a.copyLines {
		if i > 0 {
			sb.WriteByte('\n')
		}
		padded := line + strings.Repeat(" ", max(a.width-lipgloss.Width(line), 0))
		if a.copyAnchor != -1 && i >= selStart && i <= selEnd {
			sb.WriteString(selectBg.Render(padded))
		} else if i == a.copyCursor {
			sb.WriteString(cursorBg.Render(padded))
		} else {
			sb.WriteString(line)
		}
	}

	a.msgFull.vp.SetContent(sb.String())
	a.msgFull.vp.YOffset = offset
}

// msgFullBreadcrumb returns the breadcrumb suffix for the message full view.
func (a *App) msgFullBreadcrumb() string {
	if a.msgFull.agent.ShortID != "" {
		return fmt.Sprintf(" > agent:%s > #%d/%d",
			a.msgFull.agent.ShortID,
			a.msgFull.idx+1,
			len(a.msgFull.merged))
	}
	m := a.msgFull.merged[a.msgFull.idx]
	return fmt.Sprintf(" > #%d %s", m.startIdx+1, strings.ToUpper(m.entry.Role))
}

// handleMsgFullSearchInput handles key events while the search input is active.
func (a *App) handleMsgFullSearchInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "enter":
		a.commitMsgFullSearch()
		return a, nil
	case "esc":
		a.cancelMsgFullSearch()
		return a, nil
	}
	var cmd tea.Cmd
	a.msgFull.searchInput, cmd = a.msgFull.searchInput.Update(msg)
	return a, cmd
}

// startMsgFullSearch activates the search input in msgFull view.
func (a *App) startMsgFullSearch() {
	ti := textinput.New()
	ti.Prompt = "/"
	ti.Placeholder = ""
	ti.CharLimit = 200
	ti.Width = a.width - 20
	ti.Focus()
	a.msgFull.searchInput = ti
	a.msgFull.searching = true
}

// commitMsgFullSearch commits the current search input and finds matches.
func (a *App) commitMsgFullSearch() {
	term := a.msgFull.searchInput.Value()
	a.msgFull.searching = false
	if term == "" {
		a.msgFull.searchTerm = ""
		a.msgFull.searchLines = nil
		return
	}
	a.msgFull.searchTerm = term
	a.buildMsgFullSearchMatches()

	// Apply search highlighting
	if a.msgFull.allMessages {
		content := highlightSearchMatches(a.msgFull.content, term, a.msgFullCurrentMatchLine())
		a.msgFull.vp.SetContent(content)
	} else {
		a.refreshMsgFullPreview()
	}

	// Jump to first match at or after current viewport
	a.jumpMsgFullSearchForward()
}

// cancelMsgFullSearch closes search without changing the term.
func (a *App) cancelMsgFullSearch() {
	a.msgFull.searching = false
}

// clearMsgFullSearch clears search state entirely.
func (a *App) clearMsgFullSearch() {
	a.msgFull.searching = false
	a.msgFull.searchTerm = ""
	a.msgFull.searchLines = nil
	a.msgFull.searchIdx = 0

	// Re-render without highlights
	if a.msgFull.allMessages {
		a.msgFull.vp.SetContent(a.msgFull.content)
	} else {
		a.refreshMsgFullPreview()
	}
}

// buildMsgFullSearchMatches finds all lines matching the search term.
// It scans the given content (the same content set on the viewport,
// before highlight wrapping) so line numbers match what's displayed.
func (a *App) buildMsgFullSearchMatches() {
	term := strings.ToLower(a.msgFull.searchTerm)
	// For single-message view, get the current rendered content
	// (with cursor/folds) to match actual viewport line numbers.
	var source string
	if a.msgFull.allMessages {
		source = a.msgFull.content
	} else {
		fs := &a.msgFull.folds
		rp := renderFullMessageWithCursor(fs.Entry, a.width, fs.Collapsed, fs.Formatted, fs.BlockCursor)
		source = rp.content
	}
	fullPlain := stripANSI(source)
	lines := strings.Split(fullPlain, "\n")
	a.msgFull.searchLines = nil
	for i, line := range lines {
		if strings.Contains(strings.ToLower(line), term) {
			a.msgFull.searchLines = append(a.msgFull.searchLines, i)
		}
	}
	a.msgFull.searchIdx = 0
}

// jumpMsgFullSearchForward jumps to the next match at or after the viewport.
func (a *App) jumpMsgFullSearchForward() {
	if len(a.msgFull.searchLines) == 0 {
		return
	}
	offset := a.msgFull.vp.YOffset
	// Find the first match at or after current offset
	for i, line := range a.msgFull.searchLines {
		if line >= offset {
			a.msgFull.searchIdx = i
			a.scrollToSearchMatch()
			return
		}
	}
	// Wrap around
	a.msgFull.searchIdx = 0
	a.scrollToSearchMatch()
}

// jumpMsgFullSearchBackward jumps to the previous match.
func (a *App) jumpMsgFullSearchBackward() {
	if len(a.msgFull.searchLines) == 0 {
		return
	}
	offset := a.msgFull.vp.YOffset
	// Find the last match before current offset
	for i := len(a.msgFull.searchLines) - 1; i >= 0; i-- {
		if a.msgFull.searchLines[i] < offset {
			a.msgFull.searchIdx = i
			a.scrollToSearchMatch()
			return
		}
	}
	// Wrap around
	a.msgFull.searchIdx = len(a.msgFull.searchLines) - 1
	a.scrollToSearchMatch()
}

// nextMsgFullSearchMatch moves to the next match.
func (a *App) nextMsgFullSearchMatch() {
	if len(a.msgFull.searchLines) == 0 {
		return
	}
	a.msgFull.searchIdx = (a.msgFull.searchIdx + 1) % len(a.msgFull.searchLines)
	a.scrollToSearchMatch()
}

// prevMsgFullSearchMatch moves to the previous match.
func (a *App) prevMsgFullSearchMatch() {
	if len(a.msgFull.searchLines) == 0 {
		return
	}
	a.msgFull.searchIdx--
	if a.msgFull.searchIdx < 0 {
		a.msgFull.searchIdx = len(a.msgFull.searchLines) - 1
	}
	a.scrollToSearchMatch()
}

// scrollToSearchMatch re-renders highlights and scrolls viewport to show the current search match.
func (a *App) scrollToSearchMatch() {
	if a.msgFull.searchIdx < 0 || a.msgFull.searchIdx >= len(a.msgFull.searchLines) {
		return
	}

	// Re-render content with updated current-match highlight
	currentLine := a.msgFullCurrentMatchLine()
	if a.msgFull.allMessages {
		content := highlightSearchMatches(a.msgFull.content, a.msgFull.searchTerm, currentLine)
		a.msgFull.vp.SetContent(content)
	} else {
		fs := &a.msgFull.folds
		rp := renderFullMessageWithCursor(fs.Entry, a.width, fs.Collapsed, fs.Formatted, fs.BlockCursor)
		fs.BlockStarts = rp.blockStarts
		content := highlightSearchMatches(rp.content, a.msgFull.searchTerm, currentLine)
		a.msgFull.vp.SetContent(content)
	}

	line := a.msgFull.searchLines[a.msgFull.searchIdx]
	target := max(line-a.msgFull.vp.Height/3, 0)
	a.msgFull.vp.SetYOffset(target)
}

// msgFullCurrentMatchLine returns the line number of the current search match, or -1.
func (a *App) msgFullCurrentMatchLine() int {
	if len(a.msgFull.searchLines) == 0 || a.msgFull.searchIdx < 0 || a.msgFull.searchIdx >= len(a.msgFull.searchLines) {
		return -1
	}
	return a.msgFull.searchLines[a.msgFull.searchIdx]
}

// highlightSearchMatches wraps occurrences of the search term with a
// highlight background in the rendered viewport content.
// currentLine is the line number of the active match (-1 for no active highlight).
func highlightSearchMatches(content, term string, currentLine int) string {
	if term == "" {
		return content
	}
	lowerTerm := strings.ToLower(term)
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		plain := stripANSI(line)
		if !strings.Contains(strings.ToLower(plain), lowerTerm) {
			continue
		}
		lines[i] = highlightLine(line, term, i == currentLine)
	}
	return strings.Join(lines, "\n")
}

// highlightLine inserts ANSI highlight escapes around case-insensitive matches
// in a line that may contain existing ANSI sequences.
// If isCurrent is true, uses a brighter style for the active match line.
func highlightLine(line, term string, isCurrent bool) string {
	hlStart := "\x1b[43;30m" // yellow bg, black fg
	if isCurrent {
		hlStart = "\x1b[46;30m" // cyan bg, black fg (current match)
	}
	const hlEnd = "\x1b[0m"

	lowerTerm := strings.ToLower(term)
	termLen := len(lowerTerm)

	// Walk the line tracking visible character position vs ANSI escapes.
	// Build a map from visible-char index to byte positions in the original line.
	type charPos struct {
		byteStart int
		byteEnd   int
	}
	var visChars []charPos
	i := 0
	for i < len(line) {
		if line[i] == '\x1b' && i+1 < len(line) && line[i+1] == '[' {
			// Skip ANSI escape sequence
			j := i + 2
			for j < len(line) && line[j] != 'm' {
				j++
			}
			if j < len(line) {
				j++ // skip 'm'
			}
			i = j
			continue
		}
		visChars = append(visChars, charPos{i, i + 1})
		i++
	}

	if len(visChars) == 0 {
		return line
	}

	// Find matches in visible text
	visText := make([]byte, len(visChars))
	for idx, cp := range visChars {
		visText[idx] = line[cp.byteStart]
	}
	lowerVis := strings.ToLower(string(visText))

	type matchRange struct{ start, end int } // visible char indices
	var matches []matchRange
	pos := 0
	for {
		idx := strings.Index(lowerVis[pos:], lowerTerm)
		if idx < 0 {
			break
		}
		mStart := pos + idx
		matches = append(matches, matchRange{mStart, mStart + termLen})
		pos = mStart + termLen
	}

	if len(matches) == 0 {
		return line
	}

	// Rebuild the line, inserting highlight codes around matched visible chars.
	// Track which visible chars are highlighted.
	hlSet := make([]bool, len(visChars))
	for _, m := range matches {
		for j := m.start; j < m.end && j < len(visChars); j++ {
			hlSet[j] = true
		}
	}

	var sb strings.Builder
	visIdx := 0
	inHL := false
	i = 0
	for i < len(line) {
		if line[i] == '\x1b' && i+1 < len(line) && line[i+1] == '[' {
			// Copy ANSI escape through
			j := i + 2
			for j < len(line) && line[j] != 'm' {
				j++
			}
			if j < len(line) {
				j++
			}
			sb.WriteString(line[i:j])
			i = j
			continue
		}
		// Visible character
		if visIdx < len(hlSet) {
			if hlSet[visIdx] && !inHL {
				sb.WriteString(hlStart)
				inHL = true
			} else if !hlSet[visIdx] && inHL {
				sb.WriteString(hlEnd)
				inHL = false
			}
		}
		sb.WriteByte(line[i])
		visIdx++
		i++
	}
	if inHL {
		sb.WriteString(hlEnd)
	}
	return sb.String()
}
