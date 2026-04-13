package extract

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

type ChangeItem struct {
	Item        Item
	ToolNames   []string
	ToolInputs  []string
	Summary     string
	ChangeCount int
	Timestamp   time.Time
}

type editLikeInput struct {
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

type writeLikeInput struct {
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

func BlockChanges(blocks []session.ContentBlock) []ChangeItem {
	return blockChangesWithTimestamp(blocks, time.Time{})
}

func blockChangesWithTimestamp(blocks []session.ContentBlock, ts time.Time) []ChangeItem {
	seen := make(map[string]int)
	var items []ChangeItem
	for _, block := range blocks {
		if block.Type != "tool_use" || block.ToolInput == "" {
			continue
		}
		filePath, summary := changePathAndSummary(block.ToolName, block.ToolInput)
		if filePath == "" {
			continue
		}
		if idx, ok := seen[filePath]; ok {
			items[idx].ChangeCount++
			items[idx].ToolNames = append(items[idx].ToolNames, block.ToolName)
			items[idx].ToolInputs = append(items[idx].ToolInputs, block.ToolInput)
			items[idx].Summary = summarizeCount(items[idx].Summary, items[idx].ChangeCount)
			if !ts.IsZero() {
				items[idx].Timestamp = ts // keep latest
			}
			continue
		}
		seen[filePath] = len(items)
		items = append(items, ChangeItem{
			Item: Item{
				URL:      filePath,
				Label:    ShortenPath(filePath),
				Category: block.ToolName,
			},
			ToolNames:   []string{block.ToolName},
			ToolInputs:  []string{block.ToolInput},
			Summary:     summary,
			ChangeCount: 1,
			Timestamp:   ts,
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Item.URL < items[j].Item.URL })
	return items
}

// EntryChanges extracts change items from entries, preserving timestamps.
func EntryChanges(entries []session.Entry) []ChangeItem {
	seen := make(map[string]int)
	var items []ChangeItem
	for _, entry := range entries {
		for _, block := range entry.Content {
			if block.Type != "tool_use" || block.ToolInput == "" {
				continue
			}
			filePath, summary := changePathAndSummary(block.ToolName, block.ToolInput)
			if filePath == "" {
				continue
			}
			if idx, ok := seen[filePath]; ok {
				items[idx].ChangeCount++
				items[idx].ToolNames = append(items[idx].ToolNames, block.ToolName)
				items[idx].ToolInputs = append(items[idx].ToolInputs, block.ToolInput)
				items[idx].Summary = summarizeCount(items[idx].Summary, items[idx].ChangeCount)
				if !entry.Timestamp.IsZero() {
					items[idx].Timestamp = entry.Timestamp
				}
				continue
			}
			seen[filePath] = len(items)
			items = append(items, ChangeItem{
				Item: Item{
					URL:      filePath,
					Label:    ShortenPath(filePath),
					Category: block.ToolName,
				},
				ToolNames:   []string{block.ToolName},
				ToolInputs:  []string{block.ToolInput},
				Summary:     summary,
				ChangeCount: 1,
				Timestamp:   entry.Timestamp,
			})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Item.URL < items[j].Item.URL })
	return items
}

func SessionChanges(filePath string) []ChangeItem {
	entries, err := session.LoadMessages(filePath)
	if err != nil {
		return nil
	}
	return EntryChanges(entries)
}

func changePathAndSummary(toolName, toolInput string) (string, string) {
	switch toolName {
	case "Edit", "MultiEdit":
		var in editLikeInput
		if json.Unmarshal([]byte(toolInput), &in) != nil || in.FilePath == "" {
			return "", ""
		}
		oldCount := len(splitLinesLocal(in.OldString))
		newCount := len(splitLinesLocal(in.NewString))
		summary := fmt.Sprintf("%s: -%d/+%d", toolName, oldCount, newCount)
		if in.ReplaceAll {
			summary += " (all)"
		}
		return in.FilePath, summary
	case "Write":
		var in writeLikeInput
		if json.Unmarshal([]byte(toolInput), &in) != nil || in.FilePath == "" {
			return "", ""
		}
		return in.FilePath, fmt.Sprintf("Write: +%d lines", len(splitLinesLocal(in.Content)))
	default:
		return "", ""
	}
}

func summarizeCount(summary string, count int) string {
	if count <= 1 {
		return summary
	}
	base := summary
	if idx := strings.Index(base, " ×"); idx >= 0 {
		base = base[:idx]
	}
	return fmt.Sprintf("%s ×%d", base, count)
}

func SummarizeChangeCount(summary string, count int) string {
	return summarizeCount(summary, count)
}

func splitLinesLocal(s string) []string {
	if s == "" {
		return []string{""}
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 1 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
