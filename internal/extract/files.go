package extract

import (
	"github.com/sendbird/ccx/internal/session"
)

// FilePathTools maps tool names to their JSON field containing the file path.
var FilePathTools = map[string]string{
	"Read":         "file_path",
	"Write":        "file_path",
	"Edit":         "file_path",
	"Glob":         "path",
	"Grep":         "path",
	"LSP":          "filePath",
	"NotebookEdit": "notebook_path",
}

// BlockFilePaths extracts unique file paths from tool_use blocks.
func BlockFilePaths(blocks []session.ContentBlock) []Item {
	seen := make(map[string]bool)
	var items []Item

	for _, block := range blocks {
		if block.Type != "tool_use" || block.ToolInput == "" {
			continue
		}
		field, ok := FilePathTools[block.ToolName]
		if !ok {
			continue
		}
		path := JSONField(block.ToolInput, field)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		items = append(items, Item{
			URL:      path,
			Label:    ShortenPath(path),
			Category: block.ToolName,
		})
	}
	return items
}

// SessionFilePaths loads messages and extracts file paths.
func SessionFilePaths(filePath string) []Item {
	entries, err := session.LoadMessages(filePath)
	if err != nil {
		return nil
	}
	var blocks []session.ContentBlock
	for _, entry := range entries {
		blocks = append(blocks, entry.Content...)
	}
	return BlockFilePaths(blocks)
}
