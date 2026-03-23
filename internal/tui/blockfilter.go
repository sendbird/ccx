package tui

import (
	"strings"

	"github.com/sendbird/ccx/internal/session"
)

// applyBlockFilter computes per-block visibility for the given filter expression.
// Returns nil if filter is empty (all visible). Otherwise returns a bool slice
// where true = visible. Supported syntax:
//
//	is:hook      — tool_use blocks with hooks attached
//	is:tool      — tool_use blocks
//	is:result    — tool_result blocks
//	is:error     — tool_result blocks with IsError
//	is:text      — text blocks
//	is:thinking  — thinking blocks
//	is:skill     — Skill tool_use blocks
//	is:image     — image blocks
//	tool:Name    — tool_use blocks where ToolName matches (case-insensitive)
//	<free text>  — blocks containing the text (case-insensitive)
//
// Multiple terms are ANDed. Prefix ! negates a term.
func applyBlockFilter(filter string, entry session.Entry) []bool {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return nil
	}

	terms := strings.Fields(filter)
	vis := make([]bool, len(entry.Content))

	for i, block := range entry.Content {
		vis[i] = blockMatchesAll(block, terms)
	}
	return vis
}

func blockMatchesAll(block session.ContentBlock, terms []string) bool {
	for _, term := range terms {
		negate := false
		t := term
		if strings.HasPrefix(t, "!") {
			negate = true
			t = t[1:]
		}
		match := blockMatchesTerm(block, t)
		if negate {
			match = !match
		}
		if !match {
			return false
		}
	}
	return true
}

func blockMatchesTerm(block session.ContentBlock, term string) bool {
	lower := strings.ToLower(term)

	// Structured filters
	if strings.HasPrefix(lower, "is:") {
		tag := lower[3:]
		switch tag {
		case "hook":
			return block.Type == "tool_use" && len(block.Hooks) > 0
		case "tool":
			return block.Type == "tool_use"
		case "result":
			return block.Type == "tool_result"
		case "error":
			return block.IsError
		case "text":
			return block.Type == "text"
		case "thinking":
			return block.Type == "thinking"
		case "skill":
			return block.Type == "tool_use" && block.ToolName == "Skill"
		case "image":
			return block.Type == "image"
		}
		return false
	}

	if strings.HasPrefix(lower, "tool:") {
		name := lower[5:]
		return block.Type == "tool_use" && strings.EqualFold(block.ToolName, name)
	}

	// Free text search — match against block content
	searchIn := strings.ToLower(blockSearchText(block))
	return strings.Contains(searchIn, lower)
}

// blockSearchText returns a searchable text representation of a block.
func blockSearchText(block session.ContentBlock) string {
	switch block.Type {
	case "text":
		return block.Text
	case "tool_use":
		return block.ToolName + " " + block.ToolInput
	case "tool_result":
		return block.Text
	case "thinking":
		return block.Text
	default:
		return block.Text
	}
}

// countVisibleBlocks returns the number of true values in the visibility slice.
func countVisibleBlocks(vis []bool) int {
	if vis == nil {
		return -1 // no filter
	}
	n := 0
	for _, v := range vis {
		if v {
			n++
		}
	}
	return n
}
