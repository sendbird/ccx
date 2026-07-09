package extract

import (
	"strings"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

// imageExts lists file extensions that belong in the Images page, not Files.
var imageExts = []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".bmp", ".ico"}

// FilePathTools maps tool names to their JSON field containing the file path.
// Includes all tools that reference specific files (Read, Write, Edit, LSP, NotebookEdit).
// Used by buildStandardEntry to show file artifacts in conversation preview.
var FilePathTools = map[string]string{
	"Read":         "file_path",
	"Write":        "file_path",
	"Edit":         "file_path",
	"LSP":          "filePath",
	"NotebookEdit": "notebook_path",
}

// modifyTools is the subset of FilePathTools that actually modify files.
// Used by BlockModifiedFiles for the Files page browser.
var modifyTools = map[string]string{
	"Write":        "file_path",
	"Edit":         "file_path",
	"NotebookEdit": "notebook_path",
}

// BlockFilePaths extracts unique file paths from tool_use blocks.
// Includes all file-referencing tools (Read, Write, Edit, etc.).
func BlockFilePaths(blocks []session.ContentBlock) []Item {
	return extractFilePaths(blocks, FilePathTools, false)
}

// BlockModifiedFiles extracts unique file paths from write/edit tool_use blocks.
// Excludes Read (context-only), image files, and search tools (Glob/Grep).
func BlockModifiedFiles(blocks []session.ContentBlock) []Item {
	return extractFilePaths(blocks, modifyTools, true)
}

func extractFilePaths(blocks []session.ContentBlock, tools map[string]string, skipImages bool) []Item {
	seen := make(map[string]int)
	var items []Item
	extractFilePathsInto(blocks, time.Time{}, tools, skipImages, seen, &items)
	return items
}

// extractFilePathsInto appends unique file paths from blocks to items, stamping
// each with ts (keep latest occurrence). seen maps a path to its index in items.
func extractFilePathsInto(blocks []session.ContentBlock, ts time.Time, tools map[string]string, skipImages bool, seen map[string]int, items *[]Item) {
	for _, block := range blocks {
		if block.Type != "tool_use" || block.ToolInput == "" {
			continue
		}
		field, ok := tools[block.ToolName]
		if !ok {
			continue
		}
		path := JSONField(block.ToolInput, field)
		if path == "" {
			continue
		}
		if idx, ok := seen[path]; ok {
			if !ts.IsZero() {
				(*items)[idx].Timestamp = ts // keep latest occurrence
			}
			continue
		}
		if skipImages {
			lower := strings.ToLower(path)
			isImage := false
			for _, ext := range imageExts {
				if strings.HasSuffix(lower, ext) {
					isImage = true
					break
				}
			}
			if isImage {
				continue
			}
		}
		seen[path] = len(*items)
		*items = append(*items, Item{
			URL:       path,
			Label:     ShortenPath(path),
			Category:  block.ToolName,
			Timestamp: ts,
		})
	}
}

// SessionFilePaths loads messages and extracts file paths, stamping each with
// the timestamp of the entry it last appeared in and ordering most-recent first.
func SessionFilePaths(filePath string) []Item {
	entries, err := session.LoadMessages(filePath)
	if err != nil {
		return nil
	}
	seen := make(map[string]int)
	var items []Item
	for _, entry := range entries {
		extractFilePathsInto(entry.Content, entry.Timestamp, FilePathTools, false, seen, &items)
	}
	sortItemsByTime(items)
	return items
}
