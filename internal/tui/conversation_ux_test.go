package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
)

// --- Test helpers ---

func makeTextEntry(role string, ts time.Time, texts ...string) session.Entry {
	blocks := make([]session.ContentBlock, len(texts))
	for i, t := range texts {
		blocks[i] = session.ContentBlock{Type: "text", Text: t}
	}
	return session.Entry{Role: role, Timestamp: ts, Content: blocks}
}

func makeToolEntry(ts time.Time, toolName, input, result string) session.Entry {
	return session.Entry{
		Role:      "assistant",
		Timestamp: ts,
		Content: []session.ContentBlock{
			{Type: "text", Text: "Using tool..."},
			{Type: "tool_use", ToolName: toolName, ToolInput: input},
			{Type: "tool_result", Text: result},
		},
	}
}

func makeGrowingEntry(ts time.Time, blockCount int) session.Entry {
	blocks := make([]session.ContentBlock, blockCount)
	for i := range blockCount {
		blocks[i] = session.ContentBlock{
			Type: "text",
			Text: fmt.Sprintf("Block %d: %s", i, strings.Repeat("content line\n", 5)),
		}
	}
	return session.Entry{Role: "assistant", Timestamp: ts, Content: blocks}
}

func writeSessionJSONL(t *testing.T, entries []session.Entry) string {
	t.Helper()

	type rawBlock map[string]any
	type rawMessage struct {
		Role    string     `json:"role"`
		Content []rawBlock `json:"content"`
	}
	type rawEntry struct {
		Type      string     `json:"type"`
		Timestamp string     `json:"timestamp"`
		Message   rawMessage `json:"message"`
	}

	var lines []string
	for _, entry := range entries {
		blocks := make([]rawBlock, 0, len(entry.Content))
		for _, block := range entry.Content {
			switch block.Type {
			case "text":
				blocks = append(blocks, rawBlock{"type": "text", "text": block.Text})
			case "thinking":
				blocks = append(blocks, rawBlock{"type": "thinking", "text": block.Text})
			case "tool_use":
				var input any
				if block.ToolInput != "" {
					_ = json.Unmarshal([]byte(block.ToolInput), &input)
				}
				blocks = append(blocks, rawBlock{
					"type":  "tool_use",
					"id":    block.ID,
					"name":  block.ToolName,
					"input": input,
				})
			case "tool_result":
				blocks = append(blocks, rawBlock{"type": "tool_result", "content": block.Text})
			}
		}
		line, err := json.Marshal(rawEntry{
			Type:      entry.Role,
			Timestamp: entry.Timestamp.Format(time.RFC3339Nano),
			Message: rawMessage{
				Role:    entry.Role,
				Content: blocks,
			},
		})
		if err != nil {
			t.Fatalf("marshal session entry: %v", err)
		}
		lines = append(lines, string(line))
	}

	path := t.TempDir() + "/session.jsonl"
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write session jsonl: %v", err)
	}
	return path
}

// setupConvApp creates an App with a conversation loaded from entries.
func setupConvApp(t *testing.T, entries []session.Entry, width, height int) *App {
	t.Helper()
	sess := session.Session{
		ID: "test-sess", ShortID: "test", ProjectPath: "/tmp/test",
		ProjectName: "test", MsgCount: len(entries),
	}
	app := NewApp([]session.Session{sess}, Config{})
	m, _ := app.Update(tea.WindowSizeMsg{Width: width, Height: height})
	app = m.(*App)

	// Manually populate conversation state (no file I/O)
	app.currentSess = sess
	app.conv.sess = sess
	app.conv.messages = entries
	app.conv.merged = filterConversation(mergeConversationTurns(entries))
	app.conv.items = buildConvItems(app.conv.sess, app.conv.merged, nil, nil, nil)

	contentH := ContentHeight(height)
	app.conv.split.Focus = false
	app.conv.split.CacheKey = ""
	app.convList = newConvList(app.conv.items, app.conv.split.ListWidth(width, app.splitRatio), contentH)
	app.conv.split.List = &app.convList
	app.state = viewConversation

	// Open preview
	app.conv.split.Show = true
	app.updateConvPreview()
	return app
}

func setupTreeConvApp(t *testing.T, entries []session.Entry, tasks []session.TaskItem, agents []session.Subagent, width, height int) *App {
	t.Helper()
	app := setupConvApp(t, entries, width, height)
	app.currentSess.Tasks = tasks
	app.conv.sess.Tasks = tasks
	app.conv.agents = agents
	app.conv.items = buildConvItems(app.conv.sess, app.conv.merged, agents, tasks, nil)
	app.conv.leftPaneMode = convPaneTree
	app.rebuildConversationList(0)
	app.updateConvPreview()
	return app
}

func selectConvItemBy(t *testing.T, app *App, match func(convItem) bool) {
	t.Helper()
	for i, item := range app.convList.Items() {
		ci, ok := item.(convItem)
		if ok && match(ci) {
			app.convList.Select(i)
			return
		}
	}
	t.Fatal("matching conversation item not found")
}

func pressKey(app *App, key string) *App {
	var msg tea.KeyMsg
	switch key {
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	case "left":
		msg = tea.KeyMsg{Type: tea.KeyLeft}
	case "right":
		msg = tea.KeyMsg{Type: tea.KeyRight}
	case "tab":
		msg = tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		msg = tea.KeyMsg{Type: tea.KeyShiftTab}
	case "pgup":
		msg = tea.KeyMsg{Type: tea.KeyPgUp}
	case "pgdown":
		msg = tea.KeyMsg{Type: tea.KeyPgDown}
	case "home":
		msg = tea.KeyMsg{Type: tea.KeyHome}
	case "end":
		msg = tea.KeyMsg{Type: tea.KeyEnd}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEscape}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	m, _ := app.Update(msg)
	a := m.(*App)
	// Flush any pending preview debounce so tests see immediate results
	if a.previewDebounceID > 0 {
		m, _ = a.Update(previewDebounceMsg{id: a.previewDebounceID})
		a = m.(*App)
	}
	return a
}

func sendResize(app *App, w, h int) *App {
	m, _ := app.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m.(*App)
}

func testEntries() []session.Entry {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	return []session.Entry{
		makeTextEntry("user", base, "Hello, please help me with this task."),
		makeTextEntry("assistant", base.Add(time.Second), strings.Repeat("This is a long response.\n", 20)),
		makeTextEntry("user", base.Add(2*time.Second), "Thanks, now do something else."),
		makeToolEntry(base.Add(3*time.Second), "Bash", `{"command":"ls"}`, "file1.go\nfile2.go"),
		makeTextEntry("assistant", base.Add(4*time.Second), strings.Repeat("Final response with lots of content.\n", 30)),
	}
}

func TestBuildConvItemsAddsSessionMetaRows(t *testing.T) {
	entries := testEntries()
	sess := session.Session{
		ID:        "test-sess",
		ShortID:   "test",
		ProjectPath: "/tmp/test",
		HasMemory: true,
		HasPlan:   true,
		Todos:     []session.TodoItem{{Content: "remember this", Status: "pending"}},
	}
	merged := filterConversation(mergeConversationTurns(entries))
	items := buildConvItems(sess, merged, nil, nil, nil)
	if len(items) < 2 {
		t.Fatalf("expected session meta rows, got %d items", len(items))
	}
	if items[0].kind != convSessionMeta || items[0].sessionMeta != "memory" {
		t.Fatalf("first item = %#v, want memory session meta", items[0])
	}
	if items[1].kind != convSessionMeta || items[1].sessionMeta != "tasksplan" {
		t.Fatalf("second item = %#v, want tasksplan session meta", items[1])
	}
	if fv := items[0].FilterValue(); !strings.Contains(fv, "is:memory") {
		t.Fatalf("memory filter tokens missing: %q", fv)
	}
	if fv := items[1].FilterValue(); !strings.Contains(fv, "is:tasksplan") || !strings.Contains(fv, "is:plan") {
		t.Fatalf("tasksplan filter tokens missing: %q", fv)
	}
}

func TestConvPreviewSessionMetaUsesSessionRenderers(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 50)
	app.currentSess.HasMemory = true
	app.currentSess.HasPlan = true
	app.currentSess.Todos = []session.TodoItem{{Content: "saved todo", Status: "pending"}}
	app.conv.sess = app.currentSess
	app.conv.items = buildConvItems(app.conv.sess, app.conv.merged, nil, nil, nil)
	contentH := ContentHeight(app.height)
	app.convList = newConvList(app.conv.items, app.conv.split.ListWidth(app.width, app.splitRatio), contentH)
	app.conv.split.List = &app.convList

	selectConvItemBy(t, app, func(ci convItem) bool { return ci.kind == convSessionMeta && ci.sessionMeta == "memory" })
	app.updateConvPreview()
	if got := app.conv.split.Preview.View(); !strings.Contains(got, "saved todo") {
		t.Fatalf("memory preview did not use session memory renderer: %q", got)
	}
}


func TestConvPreviewUpdatesOnCursorMove(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 50)

	first := app.conv.split.CacheKey
	if first == "" {
		t.Fatal("CacheKey should be set after initial preview")
	}

	// Move down
	app = pressKey(app, "down")
	second := app.conv.split.CacheKey
	if second == first {
		t.Error("CacheKey should change when moving to a different item")
	}
}

func TestConvPreviewCacheHitOnSameItem(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 50)

	// Select last item
	items := app.convList.Items()
	app.convList.Select(len(items) - 1)
	app.updateConvPreview()
	key1 := app.conv.split.CacheKey

	// Press down at boundary — should not change CacheKey
	app = pressKey(app, "down")
	key2 := app.conv.split.CacheKey
	if key2 != key1 {
		t.Errorf("CacheKey should not change at list boundary: %q != %q", key1, key2)
	}
}

func TestConvPreviewResetOnNewEntry(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 50)

	// Scroll preview down
	app.conv.split.Preview.YOffset = 5

	// Move to next item
	app = pressKey(app, "down")

	// New entry should reset YOffset
	if app.conv.split.Preview.YOffset != 0 {
		t.Errorf("YOffset should reset to 0 on new entry, got %d", app.conv.split.Preview.YOffset)
	}
}

func TestConvPreviewGrowBlocksOnSameEntry(t *testing.T) {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	entries := []session.Entry{
		makeTextEntry("user", base, "Hello"),
		makeGrowingEntry(base.Add(time.Second), 3),
	}
	app := setupConvApp(t, entries, 160, 50)

	// Select the assistant entry
	app.convList.Select(1)
	app.updateConvPreview()

	// Manually unfold block 1
	if app.conv.split.Folds != nil && app.conv.split.Folds.Collapsed[1] {
		delete(app.conv.split.Folds.Collapsed, 1)
	}
	oldCollapsed := make(foldSet)
	if app.conv.split.Folds != nil {
		for k, v := range app.conv.split.Folds.Collapsed {
			oldCollapsed[k] = v
		}
	}

	// Simulate growing: add more blocks to the same entry
	grown := makeGrowingEntry(base.Add(time.Second), 6)
	app.conv.merged[1] = mergedMsg{entry: grown, startIdx: 1, endIdx: 1}
	app.conv.items = buildConvItems(app.conv.sess, app.conv.merged, nil, nil, nil)

	// Update preview — should use GrowBlocks, preserving existing folds
	app.conv.split.CacheKey = fmt.Sprintf("%d:%d", 1, 3) // old block count
	app.updateConvPreview()

	// Verify existing fold state for block 1 is preserved
	if app.conv.split.Folds != nil {
		if app.conv.split.Folds.Collapsed[1] != oldCollapsed[1] {
			t.Error("GrowBlocks should preserve existing fold state for block 1")
		}
	}
}

// --- Group 2: Live Tail Behavior ---

func TestLiveTailScrollsToBottom(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 50)
	app.liveTail = true
	app.conv.split.BottomAlign = true

	// Select last item with long content
	items := app.convList.Items()
	app.convList.Select(len(items) - 1)
	app.updateConvPreview()
	app.scrollConvPreviewToTail()

	// Block cursor should be at the last block (Bug A fix)
	if app.conv.split.Folds != nil && len(app.conv.split.Folds.Entry.Content) > 0 {
		lastBlock := len(app.conv.split.Folds.Entry.Content) - 1
		if app.conv.split.Folds.BlockCursor != lastBlock {
			t.Errorf("BlockCursor should be at last block (%d), got %d",
				lastBlock, app.conv.split.Folds.BlockCursor)
		}
	}

	// YOffset should be at the bottom
	total := app.conv.split.Preview.TotalLineCount()
	height := app.conv.split.Preview.Height
	expected := max(total-height, 0)
	if app.conv.split.Preview.YOffset != expected {
		t.Errorf("YOffset should be at bottom (%d), got %d", expected, app.conv.split.Preview.YOffset)
	}
}

func TestLiveTailTracksNewMessages(t *testing.T) {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	entries := []session.Entry{
		makeTextEntry("user", base, "Hello"),
		makeTextEntry("assistant", base.Add(time.Second), "Response"),
	}
	app := setupConvApp(t, entries, 160, 50)
	app.liveTail = true
	app.conv.split.BottomAlign = true

	oldCount := len(app.convList.Items())

	// Simulate new message arriving
	newEntry := makeTextEntry("user", base.Add(2*time.Second), "Follow-up question")
	entries = append(entries, newEntry)
	app.conv.messages = entries
	app.conv.merged = filterConversation(mergeConversationTurns(entries))
	app.conv.items = buildConvItems(app.conv.sess, app.conv.merged, nil, nil, nil)

	contentH := ContentHeight(app.height)
	app.convList = newConvList(app.conv.items, app.conv.split.ListWidth(app.width, app.splitRatio), contentH)
	app.conv.split.List = &app.convList

	newCount := len(app.convList.Items())
	if newCount <= oldCount {
		t.Fatal("new message should increase item count")
	}

	// Select last and update preview (simulating handleLiveTail behavior)
	app.convList.Select(newCount - 1)
	app.updateConvPreview()
	app.scrollConvPreviewToTail()

	if app.convList.Index() != newCount-1 {
		t.Errorf("cursor should be at last item (%d), got %d", newCount-1, app.convList.Index())
	}
}

func TestLiveTailGrowingContent(t *testing.T) {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	entries := []session.Entry{
		makeTextEntry("user", base, "Hello"),
		makeGrowingEntry(base.Add(time.Second), 2),
	}
	app := setupConvApp(t, entries, 160, 30)
	app.liveTail = true
	app.conv.split.BottomAlign = true

	app.convList.Select(len(app.convList.Items()) - 1)
	app.updateConvPreview()
	app.scrollConvPreviewToTail()

	// Grow the entry
	grown := makeGrowingEntry(base.Add(time.Second), 8)
	app.conv.merged[len(app.conv.merged)-1] = mergedMsg{entry: grown, startIdx: 1, endIdx: 1}
	app.conv.items = buildConvItems(app.conv.sess, app.conv.merged, nil, nil, nil)
	app.conv.split.CacheKey = fmt.Sprintf("%d:%d", 1, 2) // old count

	app.updateConvPreview()
	app.scrollConvPreviewToTail()

	total := app.conv.split.Preview.TotalLineCount()
	height := app.conv.split.Preview.Height
	if total > height {
		expected := total - height
		if app.conv.split.Preview.YOffset != expected {
			t.Errorf("after grow, YOffset should be at bottom (%d), got %d", expected, app.conv.split.Preview.YOffset)
		}
	}
}

func TestLiveTailPausesOnManualPreviewUp(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 50)
	app.liveTail = true
	app.conv.split.BottomAlign = true
	app.conv.split.Focus = true

	items := app.convList.Items()
	app.convList.Select(len(items) - 1)
	app.updateConvPreview()
	app.scrollConvPreviewToTail()

	selectedBefore := app.convList.Index()
	app = pressKey(app, "up")

	if app.liveTail {
		t.Fatal("live tail should pause after manual preview up navigation")
	}
	if app.conv.split.BottomAlign {
		t.Fatal("bottom align should be cleared when live tail pauses")
	}
	if app.convList.Index() != selectedBefore {
		t.Fatalf("manual preview navigation should not change list selection: got %d want %d", app.convList.Index(), selectedBefore)
	}
}

func TestLiveTailPausesOnPreviewPageUp(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 50)
	app.liveTail = true
	app.conv.split.BottomAlign = true
	app.conv.split.Focus = true

	items := app.convList.Items()
	app.convList.Select(len(items) - 1)
	app.updateConvPreview()
	app.scrollConvPreviewToTail()

	app = pressKey(app, "pgup")

	if app.liveTail {
		t.Fatal("live tail should pause after manual preview pgup")
	}
}

func TestLiveTailPausedDoesNotJumpBackOnTick(t *testing.T) {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	entries := []session.Entry{
		makeTextEntry("user", base, "Hello"),
		makeTextEntry("assistant", base.Add(time.Second), strings.Repeat("Long reply line.\n", 30)),
		makeTextEntry("user", base.Add(2*time.Second), "Inspect older content"),
		makeTextEntry("assistant", base.Add(3*time.Second), strings.Repeat("Newest line.\n", 30)),
	}
	app := setupConvApp(t, entries, 160, 30)
	app.liveTail = true
	app.conv.split.BottomAlign = true
	app.conv.split.Focus = true

	items := app.convList.Items()
	app.convList.Select(len(items) - 1)
	app.updateConvPreview()
	app.scrollConvPreviewToTail()

	app = pressKey(app, "up")
	selectedBefore := app.convList.Index()
	offsetBefore := app.conv.split.Preview.YOffset

	m, cmd := app.Update(liveTickMsg{})
	app = m.(*App)

	if cmd != nil {
		t.Fatal("paused live tail should not schedule another live tick")
	}
	if app.convList.Index() != selectedBefore {
		t.Fatalf("selection should stay put when live tail is paused: got %d want %d", app.convList.Index(), selectedBefore)
	}
	if app.conv.split.Preview.YOffset != offsetBefore {
		t.Fatalf("preview offset should stay put when live tail is paused: got %d want %d", app.conv.split.Preview.YOffset, offsetBefore)
	}
}

func TestLiveTailAlwaysSelectsLastItem(t *testing.T) {
	entries := testEntries()
	app := setupConvApp(t, entries, 160, 50)
	app.liveTail = true
	app.conv.split.BottomAlign = true

	// User scrolled up in list (not at last item)
	app.convList.Select(0)
	app.updateConvPreview()

	// Simulate handleLiveTail inline (refreshConversation needs file I/O,
	// so rebuild manually)
	app.conv.items = buildConvItems(app.conv.sess, app.conv.merged, nil, nil, nil)
	contentH := ContentHeight(app.height)
	app.convList = newConvList(app.conv.items, app.conv.split.ListWidth(app.width, app.splitRatio), contentH)
	app.conv.split.List = &app.convList
	visItems := app.convList.Items()
	if len(visItems) > 0 {
		app.convList.Select(len(visItems) - 1)
		app.updateConvPreview()
		app.scrollConvPreviewToTail()
	}

	// Live tail should always snap to the last item
	if app.convList.Index() != len(visItems)-1 {
		t.Errorf("live tail should always select last item, got index %d, want %d", app.convList.Index(), len(visItems)-1)
	}
}

func TestLiveTailScrollsBottomEvenWhenFocused(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 50)
	app.liveTail = true
	app.conv.split.BottomAlign = true
	app.conv.split.Focus = true // Bug A scenario: preview is focused

	// Select last item with long content
	items := app.convList.Items()
	app.convList.Select(len(items) - 1)
	app.updateConvPreview()

	// This is the key assertion: scrollConvPreviewToTail should work even when focused
	app.scrollConvPreviewToTail()

	total := app.conv.split.Preview.TotalLineCount()
	height := app.conv.split.Preview.Height
	if total > height {
		expected := total - height
		if app.conv.split.Preview.YOffset != expected {
			t.Errorf("Bug A: YOffset should be at bottom (%d) even when focused, got %d",
				expected, app.conv.split.Preview.YOffset)
		}
	}
}

// TestLiveTailRefreshNoCachePoisoning verifies that refreshConversation
// during live tail does NOT consume the CacheKey update, allowing
// handleLiveTail's updateConvPreview to process the change and scroll to bottom.
func TestLiveTailRefreshNoCachePoisoning(t *testing.T) {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	entries := []session.Entry{
		makeTextEntry("user", base, "Hello"),
		makeGrowingEntry(base.Add(time.Second), 3),
	}
	app := setupConvApp(t, entries, 160, 30)
	app.liveTail = true
	app.conv.split.BottomAlign = true

	// Select last, update preview — simulates initial state
	app.convList.Select(len(app.convList.Items()) - 1)
	app.updateConvPreview()
	app.scrollConvPreviewToTail()
	cacheKeyAfterInit := app.conv.split.CacheKey

	// Simulate content growing (streaming)
	grown := makeGrowingEntry(base.Add(time.Second), 8)
	app.conv.messages = []session.Entry{entries[0], grown}
	app.conv.merged = filterConversation(mergeConversationTurns(app.conv.messages))
	app.conv.items = buildConvItems(app.conv.sess, app.conv.merged, nil, nil, nil)

	// Simulate what refreshConversation does (minus LoadMessages I/O)
	oldIdx := app.convList.Index()
	contentH := ContentHeight(app.height)
	app.convList = newConvList(app.conv.items, app.conv.split.ListWidth(app.width, app.splitRatio), contentH)
	app.conv.split.List = &app.convList
	if oldIdx < len(app.convList.Items()) {
		app.convList.Select(oldIdx)
	}
	// During live tail, refreshConversation skips updateConvPreview.
	// So CacheKey should still be the old value.
	if app.conv.split.CacheKey != cacheKeyAfterInit {
		t.Fatalf("CacheKey should not change during refreshConversation in live tail mode")
	}

	// Now simulate what handleLiveTail does after refreshConversation
	visItems := app.convList.Items()
	app.convList.Select(len(visItems) - 1)
	app.updateConvPreview()
	app.scrollConvPreviewToTail()

	// CacheKey or preview content should now reflect the grown entry (not stay stale)
	if app.conv.split.CacheKey == cacheKeyAfterInit && len(app.conv.split.Folds.Entry.Content) == 3 {
		t.Error("handleLiveTail's updateConvPreview should have refreshed the preview state")
	}

	// YOffset should be at the bottom
	total := app.conv.split.Preview.TotalLineCount()
	height := app.conv.split.Preview.Height
	if total > height {
		expected := total - height
		if app.conv.split.Preview.YOffset != expected {
			t.Errorf("YOffset should be at bottom (%d), got %d", expected, app.conv.split.Preview.YOffset)
		}
	}
}

// --- Group 3: Resize Preservation ---

func TestResizePreservesFoldState(t *testing.T) {
	entries := []session.Entry{
		makeTextEntry("user", time.Now(), "Hello"),
		makeToolEntry(time.Now().Add(time.Second), "Bash", `{"command":"ls"}`, "output"),
	}
	app := setupConvApp(t, entries, 160, 50)

	// Select the tool entry
	app.convList.Select(1)
	app.updateConvPreview()

	// Manually unfold a block
	if app.conv.split.Folds != nil && app.conv.split.Folds.Collapsed[1] {
		delete(app.conv.split.Folds.Collapsed, 1)
	}
	foldsBefore := make(foldSet)
	if app.conv.split.Folds != nil {
		for k, v := range app.conv.split.Folds.Collapsed {
			foldsBefore[k] = v
		}
	}

	// Resize
	app = sendResize(app, 120, 40)

	// Verify folds preserved
	if app.conv.split.Folds != nil {
		for k, v := range foldsBefore {
			if app.conv.split.Folds.Collapsed[k] != v {
				t.Errorf("fold state for block %d changed after resize", k)
			}
		}
		// Also check block 1 is still unfolded
		if app.conv.split.Folds.Collapsed[1] {
			t.Error("block 1 should remain unfolded after resize")
		}
	}
}

func TestResizePreservesScrollPosition(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 50)

	// Select last item (long content)
	items := app.convList.Items()
	app.convList.Select(len(items) - 1)
	app.updateConvPreview()

	// Scroll down
	app.conv.split.Preview.YOffset = 10
	offsetBefore := app.conv.split.Preview.YOffset

	// Resize (slightly smaller)
	app = sendResize(app, 140, 45)

	// Offset should be clamped, not reset to 0
	offsetAfter := app.conv.split.Preview.YOffset
	if offsetAfter == 0 && offsetBefore > 0 {
		t.Errorf("Bug B: YOffset should be preserved/clamped after resize, not reset to 0 (was %d)", offsetBefore)
	}
}

func TestResizePreservesListCursor(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 50)

	// Select item 2
	app.convList.Select(2)
	idxBefore := app.convList.Index()

	app = sendResize(app, 120, 40)

	idxAfter := app.convList.Index()
	if idxAfter != idxBefore {
		t.Errorf("list cursor should be preserved: was %d, got %d", idxBefore, idxAfter)
	}
}

func TestResizePreservesCacheKey(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 50)

	keyBefore := app.conv.split.CacheKey
	if keyBefore == "" {
		t.Fatal("CacheKey should be set before resize")
	}

	app = sendResize(app, 120, 40)

	keyAfter := app.conv.split.CacheKey
	if keyAfter == "" {
		t.Error("Bug B: CacheKey should NOT be cleared on resize")
	}
	if keyAfter != keyBefore {
		t.Errorf("CacheKey should be preserved: was %q, got %q", keyBefore, keyAfter)
	}
}

// --- Group 4: Focus and Split Pane ---

func TestRightKeyOpensSplitPane(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 50)
	app.conv.split.Show = false
	app.conv.split.Focus = false

	app = pressKey(app, "right")

	if !app.conv.split.Show {
		t.Error("right key should open split pane")
	}
	if !app.conv.split.Focus {
		t.Error("right key should focus preview")
	}
}

func TestLeftKeyUnfocuses(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 50)
	app.conv.split.Focus = true

	// Need to set up fold state for left to work through fold handler
	// The left key from focused preview should unfocus
	app = pressKey(app, "left")

	// After pressing left from focused state, it should either unfocus
	// or close the preview entirely
	if app.conv.split.Focus {
		t.Error("left key from focused preview should unfocus")
	}
}

func TestTabOpensPreviewWithoutFocus(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 50)
	app.conv.split.Show = false
	app.conv.split.Focus = false

	app = pressKey(app, "tab")

	if !app.conv.split.Show {
		t.Error("tab should open preview")
	}
	if app.conv.split.Focus {
		t.Error("tab should not focus preview (list stays focused)")
	}
}

func TestLeftPaneTabTogglesTreeWithoutChangingRightMode(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 50)
	app.conv.leftPaneMode = convPaneFlat
	app.conv.rightPaneMode = previewHook
	app.conv.split.Focus = false

	app = pressKey(app, "tab")

	if app.conv.leftPaneMode != convPaneTree {
		t.Fatalf("left pane tab should switch to tree mode, got %d", app.conv.leftPaneMode)
	}
	if app.conv.rightPaneMode != previewHook {
		t.Fatalf("left pane tab should not change right pane mode, got %d", app.conv.rightPaneMode)
	}
}

func TestRightPaneTabCyclesDetailWithoutChangingLeftMode(t *testing.T) {
	tasks := []session.TaskItem{{ID: "42", Subject: "Refactor preview", Status: "in_progress"}}
	app := setupTreeConvApp(t, testEntries(), tasks, nil, 160, 50)
	app.conv.split.Focus = true
	app.conv.rightPaneMode = previewText

	app = pressKey(app, "tab")

	if app.conv.rightPaneMode != previewTool {
		t.Fatalf("right pane tab should cycle to standard mode, got %d", app.conv.rightPaneMode)
	}
	if app.conv.leftPaneMode != convPaneTree {
		t.Fatalf("right pane tab should not change left pane mode, got %d", app.conv.leftPaneMode)
	}
}

func TestCompactPreviewBuildsSelectableFoldState(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 50)
	app.conv.split.Focus = true
	app.conv.rightPaneMode = previewText
	selectConvItemBy(t, app, func(ci convItem) bool {
		return ci.kind == convMsg && ci.merged.entry.Role == "assistant"
	})
	app.updateConvPreview()

	if app.conv.split.Folds == nil {
		t.Fatal("compact preview should keep fold state")
	}
	if len(app.conv.split.Folds.Entry.Content) == 0 {
		t.Fatal("compact preview should build selectable content blocks")
	}
	if app.conv.split.Folds.BlockCursor < 0 || app.conv.split.Folds.BlockCursor >= len(app.conv.split.Folds.Entry.Content) {
		t.Fatalf("compact preview block cursor out of range: %d", app.conv.split.Folds.BlockCursor)
	}
}

func TestCompactPreviewArrowKeysMoveBlockSelection(t *testing.T) {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	entries := []session.Entry{
		makeTextEntry("user", base, "hello"),
		{
			Role:      "assistant",
			Timestamp: base.Add(time.Second),
			Content: []session.ContentBlock{
				{Type: "text", Text: "First compact block"},
			},
		},
		{
			Role:      "assistant",
			Timestamp: base.Add(2 * time.Second),
			Content: []session.ContentBlock{
				{Type: "text", Text: "Second compact block"},
			},
		},
	}
	app := setupConvApp(t, entries, 160, 50)
	app.conv.split.Focus = true
	app.conv.rightPaneMode = previewText
	selectConvItemBy(t, app, func(ci convItem) bool {
		return ci.kind == convMsg && ci.merged.entry.Role == "assistant"
	})
	app.updateConvPreview()

	start := app.conv.split.Folds.BlockCursor
	app = pressKey(app, "down")
	if app.conv.split.Folds.BlockCursor <= start {
		t.Fatalf("expected compact preview selection to move down, start=%d now=%d", start, app.conv.split.Folds.BlockCursor)
	}
}

func TestModeSwitchPreservesNearestSelection(t *testing.T) {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	entries := []session.Entry{
		makeTextEntry("user", base, "hello"),
		{
			Role:      "assistant",
			Timestamp: base.Add(time.Second),
			Content: []session.ContentBlock{
				{Type: "text", Text: "Intro"},
				{Type: "tool_use", ToolName: "Read", ToolInput: `{"file_path":"/tmp/x.go"}`},
				{Type: "text", Text: "Conclusion"},
			},
		},
	}
	app := setupConvApp(t, entries, 160, 40)
	app.conv.split.Focus = true
	app.conv.rightPaneMode = previewTool
	selectConvItemBy(t, app, func(ci convItem) bool {
		return ci.kind == convMsg && ci.merged.entry.Role == "assistant"
	})
	app.updateConvPreview()

	if app.conv.split.Folds == nil {
		t.Fatal("expected fold state in standard preview")
	}
	artifactIdx := -1
	for i, b := range app.conv.split.Folds.Entry.Content {
		if b.Type == "text" && strings.Contains(b.Text, "[file] /tmp/x.go") {
			artifactIdx = i
			break
		}
	}
	if artifactIdx < 0 {
		t.Fatal("expected file artifact block in standard preview")
	}
	app.conv.split.Folds.BlockCursor = artifactIdx
	app.conv.split.RefreshFoldPreview(app.width, app.splitRatio)

	app.setConvDetailLevel(previewText)
	if app.conv.split.Folds == nil || len(app.conv.split.Folds.Entry.Content) == 0 {
		t.Fatal("compact preview should preserve fold state after mode switch")
	}
	compactCursor := app.conv.split.Folds.BlockCursor

	app.setConvDetailLevel(previewTool)
	if app.conv.split.Folds == nil || len(app.conv.split.Folds.Entry.Content) == 0 {
		t.Fatal("standard preview should rebuild fold state after mode switch")
	}
	if app.conv.split.Folds.BlockCursor < 0 || app.conv.split.Folds.BlockCursor >= len(app.conv.split.Folds.Entry.Content) {
		t.Fatalf("restored block cursor out of range: %d", app.conv.split.Folds.BlockCursor)
	}
	if compactCursor < 0 || compactCursor >= len(app.conv.split.Folds.Entry.Content) {
		t.Fatalf("compact cursor out of range after restore: %d", compactCursor)
	}
}

func TestBuildEntityTreeUsesCompactLabels(t *testing.T) {
	merged := []mergedMsg{{
		entry: session.Entry{
			Role: "assistant",
			Content: []session.ContentBlock{
				{Type: "tool_use", ID: "bash-1", ToolName: "Bash", ToolInput: `{"command":"npm test --watch --runInBand --color=always"}`},
				{Type: "tool_result", ID: "bash-1", Text: "Command running in background with ID: bg-1."},
			},
		},
	}}
	agents := []session.Subagent{{
		ID:          "agent-1",
		ShortID:     "agent-1",
		FirstPrompt: "This is a very long agent prompt that should not appear in the compact tree label",
	}}
	tasks := []session.TaskItem{{
		ID:      "42",
		Subject: "This is a very long task title that should be compacted in the tree",
		Status:  "in_progress",
	}}

	items := buildEntityTree(session.Session{}, merged, agents, tasks, nil, map[string]string{"agent-1": "running"})

	var agentLabel, bgLabel, taskLabel string
	for _, item := range items {
		switch {
		case item.kind == convAgent:
			agentLabel = item.label
		case item.bgTaskID != "":
			bgLabel = item.label
		case item.kind == convTask && item.task.ID == "42":
			taskLabel = item.label
		}
	}

	if !strings.HasPrefix(agentLabel, "Agent: ") {
		t.Fatalf("agent tree label = %q, want compact Agent prefix", agentLabel)
	}
	if strings.Contains(agentLabel, "very long agent prompt") {
		t.Fatalf("agent tree label should not include full prompt: %q", agentLabel)
	}
	if !strings.HasPrefix(bgLabel, "BG: ") {
		t.Fatalf("background job tree label = %q, want compact BG prefix", bgLabel)
	}
	if !strings.HasPrefix(taskLabel, "Task: ") {
		t.Fatalf("task tree label = %q, want compact Task prefix", taskLabel)
	}
}

func TestTreeAgentPreviewShowsConversationAndToolCalls(t *testing.T) {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	agentPath := writeSessionJSONL(t, []session.Entry{
		makeTextEntry("user", base, "Investigate the failure"),
		{
			Role:      "assistant",
			Timestamp: base.Add(time.Second),
			Content: []session.ContentBlock{
				{Type: "tool_use", ID: "read-1", ToolName: "Read", ToolInput: `{"path":"main.go"}`},
				{Type: "tool_result", ID: "read-1", Text: "package main"},
				{Type: "text", Text: "Found the issue in main.go"},
			},
		},
	})
	agents := []session.Subagent{{
		ID:          "agent-1",
		ShortID:     "agent-1",
		FilePath:    agentPath,
		AgentType:   "planner",
		FirstPrompt: "Investigate the failure",
	}}

	app := setupTreeConvApp(t, []session.Entry{makeTextEntry("user", base, "parent")}, nil, agents, 160, 50)
	app.conv.rightPaneMode = previewTool
	selectConvItemBy(t, app, func(ci convItem) bool { return ci.kind == convAgent })
	app.updateConvPreview()

	if got := len(app.conv.split.Folds.Entry.Content); got < 3 {
		t.Fatalf("agent tree preview should include rich content blocks, got %d", got)
	}
	if !strings.Contains(entryFullText(app.conv.split.Folds.Entry), "Investigate the failure") {
		t.Fatalf("agent tree preview should include conversation text, got %q", entryFullText(app.conv.split.Folds.Entry))
	}
}

func TestTreeBgJobPreviewShowsCommandAndOutput(t *testing.T) {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	entries := []session.Entry{
		{
			Role:      "assistant",
			Timestamp: base,
			Content: []session.ContentBlock{
				{Type: "text", Text: "Running tests in the background"},
				{Type: "tool_use", ID: "bash-1", ToolName: "Bash", ToolInput: `{"command":"npm test --watch --runInBand"}`},
				{Type: "tool_result", ID: "bash-1", Text: "Command running in background with ID: bg-1."},
			},
		},
		{
			Role:      "assistant",
			Timestamp: base.Add(time.Second),
			Content: []session.ContentBlock{
				{Type: "tool_use", ID: "taskout-1", ToolName: "TaskOutput", ToolInput: `{"task_id":"bg-1"}`},
				{Type: "tool_result", ID: "taskout-1", Text: "<status>completed</status>\n<output>all tests passed</output>"},
			},
		},
	}

	app := setupTreeConvApp(t, entries, nil, nil, 160, 50)
	app.conv.rightPaneMode = previewTool
	selectConvItemBy(t, app, func(ci convItem) bool { return ci.bgTaskID == "bg-1" })
	app.updateConvPreview()

	if got := len(app.conv.split.Folds.Entry.Content); got < 3 {
		t.Fatalf("background job tree preview should include command and output blocks, got %d", got)
	}
	text := entryFullText(app.conv.split.Folds.Entry)
	if !strings.Contains(text, "Command: npm test --watch --runInBand") {
		t.Fatalf("background job preview should include command text, got %q", text)
	}
}

func TestTreeTaskPreviewShowsActivityLog(t *testing.T) {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	entries := []session.Entry{
		{
			Role:      "assistant",
			Timestamp: base,
			Content: []session.ContentBlock{
				{Type: "text", Text: "Starting refactor"},
				{Type: "tool_use", ToolName: "TaskUpdate", ToolInput: `{"taskId":"42","status":"in_progress"}`},
			},
		},
		makeTextEntry("assistant", base.Add(time.Second), "Updated the renderer"),
		{
			Role:      "assistant",
			Timestamp: base.Add(2 * time.Second),
			Content: []session.ContentBlock{
				{Type: "text", Text: "Finished refactor"},
				{Type: "tool_use", ToolName: "TaskUpdate", ToolInput: `{"taskId":"42","status":"completed"}`},
			},
		},
	}
	tasks := []session.TaskItem{{ID: "42", Subject: "Refactor preview", Status: "completed", Description: "Make tree previews richer"}}

	app := setupTreeConvApp(t, entries, tasks, nil, 160, 50)
	app.conv.rightPaneMode = previewTool
	selectConvItemBy(t, app, func(ci convItem) bool { return ci.kind == convTask && ci.task.ID == "42" })
	app.updateConvPreview()

	if got := len(app.conv.split.Folds.Entry.Content); got < 3 {
		t.Fatalf("task tree preview should include activity log blocks, got %d", got)
	}
	text := entryFullText(app.conv.split.Folds.Entry)
	if !strings.Contains(text, "Updated the renderer") {
		t.Fatalf("task tree preview should include activity log text, got %q", text)
	}
}

func TestEscClosesPreview(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 50)
	app.conv.split.Show = true

	app = pressKey(app, "esc")

	if app.conv.split.Show {
		t.Error("esc should close preview when open")
	}
}

// --- Group 5: Fold State ---

func TestFoldResetOnNewEntry(t *testing.T) {
	entries := []session.Entry{
		makeTextEntry("user", time.Now(), "Hello"),
		makeToolEntry(time.Now().Add(time.Second), "Bash", `{"cmd":"ls"}`, "out"),
		makeTextEntry("assistant", time.Now().Add(2*time.Second), "Done"),
	}
	app := setupConvApp(t, entries, 160, 50)

	// Select first item
	app.convList.Select(0)
	app.updateConvPreview()

	// Move to tool entry — should reset folds
	app.convList.Select(1)
	app.conv.split.CacheKey = "" // force new entry detection
	app.updateConvPreview()

	if app.conv.split.Folds != nil {
		if app.conv.split.Folds.BlockCursor != 0 {
			t.Errorf("BlockCursor should reset to 0 on new entry, got %d", app.conv.split.Folds.BlockCursor)
		}
	}
}

func TestFoldGrowBlocksPreservesExisting(t *testing.T) {
	base := time.Now()
	entry := session.Entry{
		Role:      "assistant",
		Timestamp: base,
		Content: []session.ContentBlock{
			{Type: "text", Text: "Hello"},
			{Type: "tool_use", ToolName: "Bash", ToolInput: `{"cmd":"ls"}`},
			{Type: "tool_result", Text: "output"},
		},
	}

	fs := &FoldState{}
	fs.Reset(entry)

	// Unfold block 1
	delete(fs.Collapsed, 1)

	// Grow: add more blocks
	grown := session.Entry{
		Role:      "assistant",
		Timestamp: base,
		Content: append(entry.Content,
			session.ContentBlock{Type: "text", Text: "More text"},
			session.ContentBlock{Type: "tool_use", ToolName: "Read", ToolInput: `{"path":"x"}`},
		),
	}
	fs.GrowBlocks(grown, len(entry.Content), nil, nil)

	// Block 1 should still be unfolded
	if fs.Collapsed[1] {
		t.Error("GrowBlocks should preserve existing unfold for block 1")
	}
	// New tool_use block (index 4) should be folded by default
	if !fs.Collapsed[4] {
		t.Error("GrowBlocks should fold new tool_use blocks")
	}
}

func TestFoldToggle(t *testing.T) {
	entry := session.Entry{
		Role:      "assistant",
		Timestamp: time.Now(),
		Content: []session.ContentBlock{
			{Type: "text", Text: "Hello"},
			{Type: "tool_use", ToolName: "Bash", ToolInput: `{"cmd":"ls"}`},
		},
	}

	fs := &FoldState{}
	fs.Reset(entry)

	// Block 1 (tool_use) should start folded
	if !fs.Collapsed[1] {
		t.Fatal("tool_use block should start folded")
	}

	// Right unfolds
	fs.BlockCursor = 1
	result := fs.HandleKey("right")
	if result != foldHandled {
		t.Error("right on folded block should return foldHandled")
	}
	if fs.Collapsed[1] {
		t.Error("right should unfold block 1")
	}

	// Left re-folds
	result = fs.HandleKey("left")
	if result != foldHandled {
		t.Error("left on unfolded tool block should return foldHandled")
	}
	if !fs.Collapsed[1] {
		t.Error("left should re-fold block 1")
	}
}

func TestDefaultFoldsCollapseTools(t *testing.T) {
	entry := session.Entry{
		Role:      "assistant",
		Timestamp: time.Now(),
		Content: []session.ContentBlock{
			{Type: "text", Text: "Hello"},
			{Type: "tool_use", ToolName: "Bash", ToolInput: `{}`},
			{Type: "tool_result", Text: "output"},
			{Type: "thinking", Text: "thinking..."},
			{Type: "text", Text: "Final answer"},
		},
	}

	folds := defaultFolds(entry)

	// text blocks should NOT be folded
	if folds[0] {
		t.Error("text block 0 should not be folded")
	}
	if folds[4] {
		t.Error("text block 4 should not be folded")
	}

	// tool_use, tool_result, thinking should be folded
	if !folds[1] {
		t.Error("tool_use block should be folded by default")
	}
	if !folds[2] {
		t.Error("tool_result block should be folded by default")
	}
	if !folds[3] {
		t.Error("thinking block should be folded by default")
	}
}

// TestLiveTickMsgReachesHandleLiveTailInConvView verifies that liveTickMsg
// dispatches to handleLiveTail (not refreshLivePreview) when app.state == viewConversation,
// even if sessPreviewLive and livePreviewSessID are set from a prior session view.
func TestConversationPageMenuOpensWithP(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 40)
	app = pressKey(app, "p")
	if !app.convPageMenu {
		t.Fatal("expected conversation page menu to open")
	}
}

func TestConversationPageMenuConsumesSecondKey(t *testing.T) {
	app := setupConvApp(t, testEntries(), 160, 40)
	app.convPageMenu = true
	app = pressKey(app, "o")
	if app.convPageMenu {
		t.Fatal("expected conversation page menu to close after selection")
	}
}

func TestConversationPageMenuImagesPage(t *testing.T) {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	entries := []session.Entry{{
		Role:      "assistant",
		Timestamp: base,
		Content: []session.ContentBlock{{
			Type:         "image",
			Text:         "[Image: image/png]",
			ImagePasteID: 42,
		}},
	}}
	app := setupConvApp(t, entries, 160, 40)
	app.conv.merged = filterConversation(mergeConversationTurns(entries))
	m, _ := app.openConvImagesPage()
	app = m.(*App)
	if app.convPage != convPageImages {
		t.Fatal("expected images page to open")
	}
	if len(app.convPageItems) != 1 {
		t.Fatalf("expected 1 image artifact item, got %d", len(app.convPageItems))
	}
}

func TestConversationPageBrowserUsesXPrefixedActions(t *testing.T) {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	entries := []session.Entry{{
		Role:      "assistant",
		Timestamp: base,
		Content: []session.ContentBlock{{
			Type:      "tool_use",
			ToolName:  "Write",
			ToolInput: `{"file_path":"/tmp/example.txt","content":"hello"}`,
		}},
	}}
	app := setupConvApp(t, entries, 120, 20)
	m, _ := app.openConvFilesPage()
	app = m.(*App)
	if len(app.convPageItems) == 0 {
		t.Fatal("expected file page items")
	}

	app = pressKey(app, "e")
	if app.convPageActionsMenu {
		t.Fatal("direct e should not open actions in conversation page browser")
	}

	app = pressKey(app, "y")
	if app.convPageActionsMenu {
		t.Fatal("direct y should not open actions in conversation page browser")
	}

	app = pressKey(app, "x")
	if !app.convPageActionsMenu {
		t.Fatal("x should open conversation page actions menu")
	}

	app = pressKey(app, "e")
	if app.convPageActionsMenu {
		t.Fatal("xe should consume and close the actions menu")
	}
}

func TestConversationPageBrowserNavigationKeys(t *testing.T) {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	entries := []session.Entry{{
		Role:      "assistant",
		Timestamp: base,
		Content: []session.ContentBlock{{
			Type:     "tool_use",
			ToolName: "Bash",
			ToolInput: strings.Join([]string{
				"https://a.example.com/path-a",
				"https://b.example.com/path-b",
				"https://c.example.com/path-c",
				"https://d.example.com/path-d",
				"https://e.example.com/path-e",
			}, "\n"),
		}},
	}}
	app := setupConvApp(t, entries, 120, 20)
	m, _ := app.openConvURLsPage()
	app = m.(*App)
	if len(app.convPageItems) < 5 {
		t.Fatalf("expected multiple URL items, got %d", len(app.convPageItems))
	}

	app = pressKey(app, "G")
	if got, want := app.convPageCursor, len(app.convPageItems)-1; got != want {
		t.Fatalf("G should jump to last item: got %d want %d", got, want)
	}

	app = pressKey(app, "g")
	if app.convPageCursor != 0 {
		t.Fatalf("g should jump to first item: got %d", app.convPageCursor)
	}

	app = pressKey(app, "pgdown")
	if app.convPageCursor <= 0 {
		t.Fatalf("pgdown should move cursor down by a page: got %d", app.convPageCursor)
	}

	app = pressKey(app, "pgup")
	if app.convPageCursor != 0 {
		t.Fatalf("pgup should move cursor back toward top: got %d", app.convPageCursor)
	}
}

func TestConversationPageBrowserSplitStaysSeparated(t *testing.T) {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	entries := []session.Entry{{
		Role:      "assistant",
		Timestamp: base,
		Content: []session.ContentBlock{{
			Type:      "tool_use",
			ToolName:  "Write",
			ToolInput: `{"file_path":"/tmp/really/long/path/that/should/not/break/the/layout/file.txt","content":"` + strings.Repeat("very long content without natural wrapping ", 30) + `"}`,
		}},
	}}
	app := setupConvApp(t, entries, 100, 24)
	m, _ := app.openConvChangesPage()
	app = m.(*App)

	view := app.renderConvPageBrowser()
	lines := strings.Split(view, "\n")
	if len(lines) == 0 {
		t.Fatal("expected non-empty browser view")
	}
	for i, line := range lines {
		if lipgloss.Width(line) > app.width {
			t.Fatalf("line %d exceeds width: got %d want <= %d\n%q", i, lipgloss.Width(line), app.width, line)
		}
	}

	app = sendResize(app, 80, 24)
	view = app.renderConvPageBrowser()
	lines = strings.Split(view, "\n")
	for i, line := range lines {
		if lipgloss.Width(line) > app.width {
			t.Fatalf("after resize line %d exceeds width: got %d want <= %d\n%q", i, lipgloss.Width(line), app.width, line)
		}
	}
}

func TestBuildStandardEntryPlacesArtifactsNearRelatedText(t *testing.T) {
	entry := session.Entry{
		Role: "assistant",
		Content: []session.ContentBlock{
			{Type: "text", Text: "Here is the result"},
			{Type: "tool_use", ToolName: "Read", ToolInput: `{"file_path":"/tmp/x.go"}`},
			{Type: "text", Text: "And here is the summary"},
		},
	}
	preview := buildStandardEntry(entry)
	textIdx := -1
	fileIdx := -1
	artifactsHeaderIdx := -1
	for i, b := range preview.Content {
		if b.Type == "text" && strings.Contains(b.Text, "Here is the result") {
			textIdx = i
		}
		if b.Type == "text" && strings.Contains(b.Text, "[file] /tmp/x.go") {
			fileIdx = i
		}
		if b.Type == "text" && b.Text == "Artifacts" {
			artifactsHeaderIdx = i
		}
	}
	if textIdx < 0 || fileIdx < 0 {
		t.Fatalf("expected text and file artifact blocks, got %#v", preview.Content)
	}
	if artifactsHeaderIdx >= 0 {
		t.Fatalf("standard preview should no longer use a detached Artifacts header")
	}
	if fileIdx <= textIdx {
		t.Fatalf("expected file artifact after related text, text=%d file=%d", textIdx, fileIdx)
	}
}

func TestRenderStandardPreviewShowsArtifactNearRelatedTurn(t *testing.T) {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	entries := []session.Entry{
		makeTextEntry("user", base, "hello"),
		{
			Role:      "assistant",
			Timestamp: base.Add(time.Second),
			Content: []session.ContentBlock{
				{Type: "text", Text: "Here is the result"},
				{Type: "tool_use", ToolName: "Read", ToolInput: `{"file_path":"/tmp/x.go"}`},
			},
		},
	}
	app := setupConvApp(t, entries, 160, 40)
	app.conv.rightPaneMode = previewTool
	app.conv.split.CacheKey = ""
	selectConvItemBy(t, app, func(ci convItem) bool {
		return ci.kind == convMsg && ci.merged.entry.Role == "assistant"
	})
	app.updateConvPreview()
	if app.conv.split.Folds == nil || len(app.conv.split.Folds.Entry.Content) == 0 {
		t.Fatal("expected fold-aware standard preview entry")
	}
	foundText := false
	foundFile := false
	artifactsHeaderFound := false
	for _, b := range app.conv.split.Folds.Entry.Content {
		if b.Type == "text" && strings.Contains(b.Text, "Here is the result") {
			foundText = true
		}
		if b.Type == "text" && strings.Contains(b.Text, "[file] /tmp/x.go") {
			foundFile = true
		}
		if b.Type == "text" && b.Text == "Artifacts" {
			artifactsHeaderFound = true
		}
	}
	if !foundText {
		t.Fatalf("standard preview should include the related text block")
	}
	if !foundFile {
		t.Fatalf("standard preview should include file artifact block")
	}
	if artifactsHeaderFound {
		t.Fatalf("standard preview should not include a detached Artifacts header")
	}
}

func TestStandardPreviewKeepsImageBlocksForKittyPreview(t *testing.T) {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	entries := []session.Entry{
		makeTextEntry("user", base, "show image"),
		{
			Role:      "assistant",
			Timestamp: base.Add(time.Second),
			Content: []session.ContentBlock{
				{Type: "text", Text: "Here is an image"},
				{Type: "image", Text: "[Image: image/png]", ImagePasteID: 42},
			},
		},
	}
	app := setupConvApp(t, entries, 160, 40)
	app.conv.rightPaneMode = previewTool
	app.conv.split.Focus = true
	selectConvItemBy(t, app, func(ci convItem) bool {
		return ci.kind == convMsg && ci.merged.entry.Role == "assistant"
	})
	app.updateConvPreview()

	foundImage := false
	for _, b := range app.conv.split.Folds.Entry.Content {
		if b.Type == "image" && b.ImagePasteID == 42 {
			foundImage = true
			break
		}
	}
	if !foundImage {
		t.Fatal("standard preview should retain image blocks for kitty preview")
	}
}

func TestKittyImagePathReturnsEmptyWithoutCachedImage(t *testing.T) {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	entries := []session.Entry{
		makeTextEntry("user", base, "show image"),
		{
			Role:      "assistant",
			Timestamp: base.Add(time.Second),
			Content: []session.ContentBlock{
				{Type: "text", Text: "Here is an image"},
				{Type: "image", Text: "[Image: image/png]", ImagePasteID: 42},
			},
		},
	}
	app := setupConvApp(t, entries, 160, 40)
	app.state = viewConversation
	app.termFocused = true
	app.conv.rightPaneMode = previewTool
	app.conv.split.Focus = true
	selectConvItemBy(t, app, func(ci convItem) bool {
		return ci.kind == convMsg && ci.merged.entry.Role == "assistant"
	})
	app.updateConvPreview()

	imageIdx := -1
	for i, b := range app.conv.split.Folds.Entry.Content {
		if b.Type == "image" && b.ImagePasteID == 42 {
			imageIdx = i
			break
		}
	}
	if imageIdx < 0 {
		t.Fatal("expected image block in preview")
	}
	app.conv.split.Folds.BlockCursor = imageIdx
	if path := app.kittyImagePath(); path != "" {
		t.Fatalf("expected no kitty image path without cached image, got %q", path)
	}
}
func TestFocusedArtifactTooltipForChangeBlock(t *testing.T) {
	sp := &SplitPane{}
	sp.Folds = &FoldState{
		Entry: session.Entry{Content: []session.ContentBlock{{
			Type:      "tool_use",
			ToolName:  "Edit",
			ToolInput: `{"file_path":"/tmp/x.go","old_string":"a","new_string":"b"}`,
		}}},
		BlockCursor: 0,
	}
	app := &App{currentSess: session.Session{ID: "test-sess"}}
	tooltip := app.focusedArtifactTooltip(sp, 120)
	if !strings.Contains(tooltip, "/tmp/x.go") {
		t.Fatalf("expected change tooltip to include file path, got %q", tooltip)
	}
}

func TestLiveTickMsgReachesHandleLiveTailInConvView(t *testing.T) {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	entries := []session.Entry{
		makeTextEntry("user", base, "Hello"),
		makeTextEntry("assistant", base.Add(time.Second), "Hi there"),
		makeTextEntry("user", base.Add(2*time.Second), "Another message"),
		makeTextEntry("assistant", base.Add(3*time.Second), strings.Repeat("Long reply line.\n", 30)),
	}
	app := setupConvApp(t, entries, 160, 30)

	// Simulate stale session-level live preview state (as if user came from session view)
	app.sessPreviewMode = sessPreviewLive
	app.paneProxy = &paneProxyState{sessID: "some-old-session-id"}

	// Enable live tail for conversation
	app.liveTail = true
	app.conv.split.BottomAlign = true

	// Select item 0 (not the last)
	app.convList.Select(0)
	app.updateConvPreview()

	// Send liveTickMsg — this should dispatch to handleLiveTail, NOT refreshLivePreview
	m, cmd := app.Update(liveTickMsg{})
	app = m.(*App)

	// handleLiveTail should have selected the last item (wasAtEnd was false,
	// but the key check is that liveTickMsg reached handleLiveTail at all)
	if cmd == nil {
		t.Fatal("liveTickMsg should return a command (liveTickCmd) when liveTail is true")
	}

	// After handleLiveTail, if we were at item 0 (not at end), it preserves position.
	// The important thing is that we got here at all (not trapped in sessPreviewLive path).
	// Verify by checking state is still viewConversation (refreshLivePreview would not change it).
	if app.state != viewConversation {
		t.Errorf("state should be viewConversation, got %d", app.state)
	}
}

// TestLiveTailSelectsLastMessageNotAgentOrTask verifies that handleLiveTail
// selects the last convMsg item, skipping trailing agent/task sub-items.
func TestLiveTailSelectsLastMessageNotAgentOrTask(t *testing.T) {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	entries := []session.Entry{
		makeTextEntry("user", base, "Hello"),
		makeTextEntry("assistant", base.Add(time.Second), "Let me help you."),
	}
	app := setupConvApp(t, entries, 160, 50)
	app.liveTail = true
	app.conv.split.BottomAlign = true

	// Manually add agent and task items after the last message
	// to simulate buildConvItems interleaving
	app.conv.items = append(app.conv.items, convItem{
		kind:   convAgent,
		agent:  session.Subagent{ShortID: "test-agent", AgentType: "general"},
		indent: 1,
	})
	app.conv.items = append(app.conv.items, convItem{
		kind:     convTask,
		groupTag: "tasks",
		count:    1,
		folded:   true,
		indent:   1,
		task:     session.TaskItem{Subject: "Some task", Status: "in_progress"},
	})

	contentH := ContentHeight(app.height)
	app.convList = newConvList(app.conv.items, app.conv.split.ListWidth(app.width, app.splitRatio), contentH)
	app.conv.split.List = &app.convList

	// Simulate handleLiveTail's selection logic
	visItems := app.convList.Items()
	lastMsg := len(visItems) - 1
	for i := len(visItems) - 1; i >= 0; i-- {
		if ci, ok := visItems[i].(convItem); ok && ci.kind == convMsg {
			lastMsg = i
			break
		}
	}
	app.convList.Select(lastMsg)

	// The selected item should be the last convMsg, not the agent or task
	sel, ok := app.convList.SelectedItem().(convItem)
	if !ok {
		t.Fatal("selected item should be a convItem")
	}
	if sel.kind != convMsg {
		t.Errorf("live tail should select last convMsg, got kind=%d", sel.kind)
	}
	if lastMsg >= len(visItems)-1 {
		t.Errorf("lastMsg index (%d) should be before trailing items (total %d)", lastMsg, len(visItems))
	}
}

func TestHandleLiveTailMsgFullFollowsNewLastMessage(t *testing.T) {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	initial := []session.Entry{
		makeTextEntry("user", base, "Hello"),
		makeTextEntry("assistant", base.Add(time.Second), "Reply 1"),
	}
	path := writeSessionJSONL(t, initial)

	app := setupConvApp(t, initial, 120, 30)
	app.currentSess.FilePath = path
	app.conv.sess.FilePath = path
	app.state = viewMessageFull
	app.msgFull.sess = app.currentSess
	app.msgFull.messages = app.conv.messages
	app.msgFull.merged = app.conv.merged
	app.msgFull.agents = app.conv.agents
	app.navToMsgFull(len(app.msgFull.merged) - 1)
	app.liveTail = true

	updated := append(append([]session.Entry{}, initial...), makeTextEntry("user", base.Add(2*time.Second), "Follow-up"))
	path = writeSessionJSONL(t, updated)
	app.msgFull.sess.FilePath = path

	app.handleLiveTailMsgFull()

	if got, want := app.msgFull.idx, len(app.msgFull.merged)-1; got != want {
		t.Fatalf("msgFull idx = %d, want %d", got, want)
	}
	if got := app.msgFull.merged[app.msgFull.idx].entry.Content[0].Text; got != "Follow-up" {
		t.Fatalf("live tail should follow new last message, got %q", got)
	}
}

func TestHandleLiveTailMsgFullRefreshesAllMessagesView(t *testing.T) {
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	initial := []session.Entry{
		makeTextEntry("user", base, "Hello"),
		makeTextEntry("assistant", base.Add(time.Second), "Reply 1"),
	}
	path := writeSessionJSONL(t, initial)

	app := setupConvApp(t, initial, 120, 30)
	app.currentSess.FilePath = path
	app.conv.sess.FilePath = path
	app.state = viewMessageFull
	app.msgFull.sess = app.currentSess
	app.msgFull.messages = app.conv.messages
	app.msgFull.merged = app.conv.merged
	app.msgFull.agents = app.conv.agents
	app.msgFull.allMessages = true
	app.msgFull.vp = viewport.New(app.width, ContentHeight(app.height))
	app.msgFull.content = renderAllMessages(app.msgFull.merged, app.width)
	app.msgFull.vp.SetContent(app.msgFull.content)

	updated := append(append([]session.Entry{}, initial...), makeTextEntry("user", base.Add(2*time.Second), "Newest tail line"))
	path = writeSessionJSONL(t, updated)
	app.msgFull.sess.FilePath = path

	app.handleLiveTailMsgFull()

	if !strings.Contains(app.msgFull.content, "Newest tail line") {
		t.Fatalf("allMessages content did not refresh with latest message")
	}
	if app.msgFull.vp.YOffset != max(app.msgFull.vp.TotalLineCount()-app.msgFull.vp.Height, 0) {
		t.Fatalf("allMessages live tail should scroll to bottom, got YOffset=%d", app.msgFull.vp.YOffset)
	}
}
