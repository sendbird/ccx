package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sendbird/ccx/internal/extract"
	"github.com/sendbird/ccx/internal/session"
)

// ItemRef is a single reference (context) where an item was found.
type ItemRef struct {
	EntryUUID string
	Timestamp time.Time
	Role      string // user|assistant
	Preview   string // 3-5 lines of conversation context
}

// PickerItem is a deduplicated item with all its references.
type PickerItem struct {
	Item      extract.Item
	SessionID string
	Refs      []ItemRef // all places this item was referenced
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
