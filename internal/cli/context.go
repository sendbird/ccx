package cli

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/sendbird/ccx/internal/extract"
	"github.com/sendbird/ccx/internal/session"
)

type conversationMsg struct {
	entry    session.Entry
	startIdx int
	endIdx   int
}

// ItemRef is a single reference (context) where an item was found.
type ItemRef struct {
	EntryUUID string
	Timestamp time.Time
	Role      string // user|assistant
	Preview   string // 3-5 lines of conversation context
	ToolName  string // for changes: Edit, Write, etc.
	ToolInput string // for changes: raw JSON tool input for diff rendering
}

// PickerItem is a deduplicated item with all its references.
type PickerItem struct {
	Item                  extract.Item
	SessionID             string
	Refs                  []ItemRef // all places this item was referenced
	ConversationArtifacts []extract.Item
	ConversationText      string
}

// FilterValue returns searchable text for the picker filter.
// Includes is:<category> tags for structured filtering.
func (p PickerItem) FilterValue() string {
	parts := []string{p.Item.Category, p.Item.Label, p.Item.URL}
	// Add is:<type> tag for structured search
	cat := strings.ToLower(p.Item.Category)
	parts = append(parts, "is:"+cat)
	// Add common aliases
	switch cat {
	case "github":
		parts = append(parts, "is:gh")
	case "read", "write", "edit", "glob", "grep":
		parts = append(parts, "is:tool")
	}
	for _, r := range p.Refs {
		parts = append(parts, r.Role, r.Preview)
		if r.Role != "" {
			parts = append(parts, "role:"+r.Role)
		}
	}
	return strings.Join(parts, " ")
}

// FirstRef returns the most recent reference for display.
func (p PickerItem) FirstRef() ItemRef {
	if len(p.Refs) == 0 {
		return ItemRef{}
	}
	return p.Refs[0]
}

func extractConversationWithContext(entries []session.Entry, sessID string) []PickerItem {
	merged := filterConversationCLI(mergeConversationTurnsCLI(entries))
	items := make([]PickerItem, 0, len(merged))
	homeDir, _ := os.UserHomeDir()
	for i, msg := range merged {
		label := conversationLabel(msg.entry, i)
		ref := ItemRef{
			EntryUUID: msg.entry.UUID,
			Timestamp: msg.entry.Timestamp,
			Role:      msg.entry.Role,
			Preview:   entryContext(msg.entry),
		}
		items = append(items, PickerItem{
			Item: extract.Item{
				URL:      fmt.Sprintf("conversation:%d", i+1),
				Label:    label,
				Category: "conversation",
			},
			SessionID:             sessID,
			Refs:                  []ItemRef{ref},
			ConversationArtifacts: conversationArtifacts(msg.entry, sessID, homeDir),
			ConversationText:      conversationListText(msg.entry),
		})
	}
	return items
}

func mergeConversationTurnsCLI(entries []session.Entry) []conversationMsg {
	if len(entries) == 0 {
		return nil
	}

	var result []conversationMsg
	for i := 0; i < len(entries); {
		e := entries[i]
		if e.Role == "user" && hasUserTextCLI(e) {
			merged := conversationMsg{entry: cloneEntryCLI(e), startIdx: i, endIdx: i}
			j := i + 1
			for j < len(entries) && entries[j].Role == "user" {
				merged.entry.Content = append(merged.entry.Content, entries[j].Content...)
				merged.endIdx = j
				j++
			}
			result = append(result, merged)
			i = j
			continue
		}
		if e.Role == "assistant" {
			merged := conversationMsg{entry: cloneEntryCLI(e), startIdx: i, endIdx: i}
			j := i + 1
			for j < len(entries) {
				next := entries[j]
				if next.Role == "user" && hasUserTextCLI(next) {
					break
				}
				merged.entry.Content = append(merged.entry.Content, next.Content...)
				merged.endIdx = j
				j++
			}
			result = append(result, merged)
			i = j
			continue
		}
		result = append(result, conversationMsg{entry: e, startIdx: i, endIdx: i})
		i++
	}
	return result
}

func filterConversationCLI(msgs []conversationMsg) []conversationMsg {
	var result []conversationMsg
	for _, m := range msgs {
		if hasVisibleContentCLI(m.entry) {
			result = append(result, m)
		}
	}
	return result
}

func hasUserTextCLI(e session.Entry) bool {
	hasText := false
	for _, block := range e.Content {
		if block.Type == "tool_result" {
			return false
		}
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			hasText = true
		}
	}
	return hasText
}

func hasVisibleContentCLI(e session.Entry) bool {
	for _, b := range e.Content {
		if b.Type == "tool_use" {
			return true
		}
		if b.Type == "text" {
			text := strings.TrimSpace(b.Text)
			if text != "" && !isSystemTextCLI(text) {
				return true
			}
		}
	}
	return false
}

func isSystemTextCLI(text string) bool {
	return strings.HasPrefix(text, "<system-reminder>") ||
		strings.HasPrefix(text, "<system>") ||
		text == "Prompt is too long"
}

func cloneEntryCLI(e session.Entry) session.Entry {
	clone := e
	clone.Content = make([]session.ContentBlock, len(e.Content))
	copy(clone.Content, e.Content)
	return clone
}

func conversationLabel(e session.Entry, idx int) string {
	prefix := fmt.Sprintf("#%d", idx+1)
	role := strings.ToUpper(e.Role)
	if role == "" {
		role = "ENTRY"
	}
	preview := session.EntryPreview(e)
	if preview == "" || preview == "(no content)" {
		preview = "(no visible content)"
	}
	preview = strings.TrimSpace(session.StripXMLTags(preview))
	if len(preview) > 80 {
		preview = preview[:77] + "..."
	}
	toolSummary := conversationToolSummary(e)
	if toolSummary != "" {
		if strings.HasPrefix(preview, "[") {
			preview = toolSummary
		} else {
			preview += "  " + toolSummary
		}
	}
	if !e.Timestamp.IsZero() {
		return fmt.Sprintf("%s  %s  %s  %s", prefix, role, e.Timestamp.Format("15:04:05"), preview)
	}
	return fmt.Sprintf("%s  %s  %s", prefix, role, preview)
}

func conversationToolSummary(e session.Entry) string {
	seen := make(map[string]int)
	var order []string
	for _, block := range e.Content {
		if block.Type != "tool_use" {
			continue
		}
		name := block.ToolName
		if name == "" {
			continue
		}
		if seen[name] == 0 {
			order = append(order, name)
		}
		seen[name]++
	}
	if len(order) == 0 {
		return ""
	}
	parts := make([]string, 0, len(order))
	for _, name := range order {
		if seen[name] > 1 {
			parts = append(parts, fmt.Sprintf("%s×%d", name, seen[name]))
		} else {
			parts = append(parts, name)
		}
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func firstConversationLine(e session.Entry) string {
	for _, b := range e.Content {
		switch b.Type {
		case "text":
			text := strings.TrimSpace(session.StripXMLTags(b.Text))
			if text != "" && !isSystemTextCLI(text) {
				line := strings.SplitN(text, "\n", 2)[0]
				return strings.TrimSpace(line)
			}
		case "tool_use":
			if b.ToolName != "" {
				return "Tool: " + b.ToolName
			}
		}
	}
	return ""
}

func conversationArtifacts(e session.Entry, sessID, homeDir string) []extract.Item {
	var items []extract.Item
	for _, item := range extract.BlockURLs(e.Content) {
		item.Category = "url"
		items = append(items, item)
	}
	for _, item := range extract.BlockFilePaths(e.Content) {
		item.Category = "file"
		items = append(items, item)
	}
	for _, ch := range extract.BlockChanges(e.Content) {
		items = append(items, extract.Item{
			URL:      ch.Item.URL,
			Label:    ch.Item.Label + "  " + ch.Summary,
			Category: "change",
		})
	}
	for _, b := range e.Content {
		if b.Type != "image" || b.ImagePasteID <= 0 {
			continue
		}
		p := session.ImageCachePath(homeDir, sessID, b.ImagePasteID)
		if p == "" {
			p, _ = session.ExtractImageToTemp(homeDir, "", sessID, b.ImagePasteID)
		}
		if p == "" {
			continue
		}
		items = append(items, extract.Item{
			URL:      p,
			Label:    fmt.Sprintf("#%d", b.ImagePasteID),
			Category: "image",
		})
	}
	return items
}

func conversationListText(e session.Entry) string {
	var lines []string
	for _, b := range e.Content {
		switch b.Type {
		case "text":
			text := strings.TrimSpace(session.StripXMLTags(b.Text))
			if text == "" || isSystemTextCLI(text) {
				continue
			}
			for _, line := range strings.Split(text, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				lines = append(lines, line)
				if len(lines) >= 3 {
					return strings.Join(lines, "\n")
				}
			}
		case "tool_use":
			if b.ToolName != "" && len(lines) < 3 {
				lines = append(lines, "Tool: "+b.ToolName)
			}
		case "image":
			if len(lines) < 3 {
				lines = append(lines, "[image]")
			}
		}
	}
	if len(lines) == 0 {
		return "(no visible content)"
	}
	return strings.Join(lines, "\n")
}

func extractURLsWithContext(entries []session.Entry, sessID string) []PickerItem {
	index := make(map[string]int) // URL → index in items
	var items []PickerItem
	for _, e := range entries {
		ctx := entryContext(e)
		for _, item := range extract.BlockURLs(e.Content) {
			ref := ItemRef{
				EntryUUID: e.UUID,
				Timestamp: e.Timestamp,
				Role:      e.Role,
				Preview:   ctx,
			}
			if idx, ok := index[item.URL]; ok {
				items[idx].Refs = append(items[idx].Refs, ref)
			} else {
				index[item.URL] = len(items)
				items = append(items, PickerItem{
					Item:      item,
					SessionID: sessID,
					Refs:      []ItemRef{ref},
				})
			}
		}
	}
	return items
}

func extractFilesWithContext(entries []session.Entry, sessID string) []PickerItem {
	index := make(map[string]int)
	var items []PickerItem
	for _, e := range entries {
		ctx := entryContext(e)
		for _, item := range extract.BlockFilePaths(e.Content) {
			ref := ItemRef{
				EntryUUID: e.UUID,
				Timestamp: e.Timestamp,
				Role:      e.Role,
				Preview:   ctx,
			}
			if idx, ok := index[item.URL]; ok {
				items[idx].Refs = append(items[idx].Refs, ref)
			} else {
				index[item.URL] = len(items)
				items = append(items, PickerItem{
					Item:      item,
					SessionID: sessID,
					Refs:      []ItemRef{ref},
				})
			}
		}
	}
	return items
}

func extractChangesWithContext(entries []session.Entry, sessID string) []PickerItem {
	index := make(map[string]int)
	var items []PickerItem
	for _, e := range entries {
		ctx := entryContext(e)
		for _, item := range extract.BlockChanges(e.Content) {
			toolName, toolInput := "", ""
			if len(item.ToolNames) > 0 {
				toolName = item.ToolNames[0]
			}
			if len(item.ToolInputs) > 0 {
				toolInput = item.ToolInputs[0]
			}
			ref := ItemRef{
				EntryUUID: e.UUID,
				Timestamp: e.Timestamp,
				Role:      e.Role,
				Preview:   ctx,
				ToolName:  toolName,
				ToolInput: toolInput,
			}
			tsLabel := ""
			if !e.Timestamp.IsZero() {
				tsLabel = "  " + shortTimeAgo(e.Timestamp)
			}
			if idx, ok := index[item.Item.URL]; ok {
				items[idx].Refs = append(items[idx].Refs, ref)
				items[idx].Item.Category = item.Item.Category
				items[idx].Item.Label = item.Item.Label + "  " + extract.SummarizeChangeCount(item.Summary, len(items[idx].Refs)) + tsLabel
			} else {
				index[item.Item.URL] = len(items)
				items = append(items, PickerItem{
					Item: extract.Item{
						URL:      item.Item.URL,
						Label:    item.Item.Label + "  " + item.Summary + tsLabel,
						Category: "change",
					},
					SessionID: sessID,
					Refs:      []ItemRef{ref},
				})
			}
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if len(items[i].Refs) == 0 || len(items[j].Refs) == 0 {
			return items[i].Item.URL < items[j].Item.URL
		}
		return items[i].Refs[0].Timestamp.After(items[j].Refs[0].Timestamp)
	})
	return items
}

func shortTimeAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("Jan 02")
	}
}

func extractImagesWithContext(entries []session.Entry, sessID, homeDir string) []PickerItem {
	index := make(map[int]int) // pasteID → index
	var items []PickerItem
	for _, e := range entries {
		ctx := entryContext(e)
		for _, b := range e.Content {
			if b.Type != "image" || b.ImagePasteID <= 0 {
				continue
			}
			ref := ItemRef{
				EntryUUID: e.UUID,
				Timestamp: e.Timestamp,
				Role:      e.Role,
				Preview:   ctx,
			}
			if idx, ok := index[b.ImagePasteID]; ok {
				items[idx].Refs = append(items[idx].Refs, ref)
				continue
			}
			p := session.ImageCachePath(homeDir, sessID, b.ImagePasteID)
			if p == "" {
				p, _ = session.ExtractImageToTemp(homeDir, "", sessID, b.ImagePasteID)
			}
			if p == "" {
				continue
			}
			index[b.ImagePasteID] = len(items)
			items = append(items, PickerItem{
				Item: extract.Item{
					URL:      p,
					Label:    fmt.Sprintf("#%d", b.ImagePasteID),
					Category: "image",
				},
				SessionID: sessID,
				Refs:      []ItemRef{ref},
			})
		}
	}
	return items
}

// entryContext extracts a short text snippet from an entry for preview.
func entryContext(e session.Entry) string {
	var lines []string
	role := strings.ToUpper(e.Role)
	if role == "" {
		role = "ENTRY"
	}
	header := role
	if !e.Timestamp.IsZero() {
		header += "  " + e.Timestamp.Format("15:04:05")
	}
	lines = append(lines, header)

	for _, b := range e.Content {
		switch b.Type {
		case "text":
			text := strings.TrimSpace(session.StripXMLTags(b.Text))
			if text != "" {
				for _, l := range strings.Split(text, "\n") {
					l = strings.TrimSpace(l)
					if l != "" {
						lines = append(lines, l)
					}
					if len(lines) >= 6 {
						break
					}
				}
			}
		case "tool_use":
			summary := "Tool: " + b.ToolName
			if b.ToolInput != "" {
				if path := extract.JSONField(b.ToolInput, "file_path"); path != "" {
					summary += "  " + shortenPath(path)
				} else if cmd := extract.JSONField(b.ToolInput, "command"); cmd != "" {
					if len(cmd) > 60 {
						cmd = cmd[:57] + "..."
					}
					summary += "  $ " + cmd
				} else if desc := extract.JSONField(b.ToolInput, "description"); desc != "" {
					if len(desc) > 50 {
						desc = desc[:47] + "..."
					}
					summary += "  " + desc
				}
			}
			lines = append(lines, summary)
		case "tool_result":
			text := strings.TrimSpace(session.StripXMLTags(b.Text))
			if text != "" {
				first := strings.SplitN(text, "\n", 2)[0]
				if len(first) > 60 {
					first = first[:57] + "..."
				}
				lines = append(lines, "Result: "+first)
			}
		}
		if len(lines) >= 6 {
			break
		}
	}

	// Ensure at least one context line beyond the header
	if len(lines) <= 1 {
		lines = append(lines, "(no text content)")
	}

	return strings.Join(lines, "\n")
}

func shortenPath(path string) string {
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}
