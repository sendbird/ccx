package tui

import (
	"fmt"
	"strings"

	"github.com/sendbird/ccx/internal/session"
)

// mergedMsg represents a logical conversation turn, potentially combining
// multiple raw API entries into one.
type mergedMsg struct {
	entry    session.Entry
	startIdx int // first original entry index (0-based)
	endIdx   int // last original entry index (0-based)
}

// mergeConversationTurns groups raw entries into logical conversation turns.
// An assistant turn includes all consecutive assistant messages plus any
// interleaved user messages that contain only tool_result blocks (automated
// tool responses). User messages with actual text content stay standalone.
func mergeConversationTurns(entries []session.Entry) []mergedMsg {
	if len(entries) == 0 {
		return nil
	}

	var result []mergedMsg
	i := 0

	for i < len(entries) {
		e := entries[i]

		// User message with actual text → start a user turn, absorb
		// consecutive user messages (e.g. command output, tool results)
		if e.Role == "user" && hasUserText(e) {
			merged := mergedMsg{
				entry:    cloneEntry(e),
				startIdx: i,
				endIdx:   i,
			}
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

		// Assistant message → start a turn, absorb consecutive messages
		// until the next user message with text
		if e.Role == "assistant" {
			merged := mergedMsg{
				entry:    cloneEntry(e),
				startIdx: i,
				endIdx:   i,
			}
			j := i + 1
			for j < len(entries) {
				next := entries[j]
				if next.Role == "user" && hasUserText(next) {
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

		// Orphan user tool_result (no preceding assistant)
		result = append(result, mergedMsg{
			entry:    e,
			startIdx: i,
			endIdx:   i,
		})
		i++
	}

	return result
}

// hasUserText returns true if the entry is a real user prompt (has text
// blocks but no tool_result blocks). User messages that carry tool_result
// blocks are automated tool responses — any text they contain is
// system-generated (e.g. <system-reminder>) and should not break merging.
func hasUserText(e session.Entry) bool {
	hasText := false
	for _, block := range e.Content {
		if block.Type == "tool_result" {
			return false // automated tool response, not a real user prompt
		}
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			hasText = true
		}
	}
	return hasText
}

// cloneEntry creates a shallow copy of an Entry with its own Content slice.
func cloneEntry(e session.Entry) session.Entry {
	clone := e
	clone.Content = make([]session.ContentBlock, len(e.Content))
	copy(clone.Content, e.Content)
	return clone
}


// filterConversation keeps messages with visible conversation content:
// real text (not system-generated) or tool calls.
// Drops empty entries ("no content") and system-only messages.
func filterConversation(msgs []mergedMsg) []mergedMsg {
	var result []mergedMsg
	for _, m := range msgs {
		if hasVisibleContent(m.entry) {
			result = append(result, m)
		}
	}
	return result
}

// hasVisibleContent returns true if the entry has content meaningful
// in a conversation preview (non-system text or tool calls).
func hasVisibleContent(e session.Entry) bool {
	for _, b := range e.Content {
		if b.Type == "tool_use" {
			return true
		}
		if b.Type == "text" {
			text := strings.TrimSpace(b.Text)
			if text != "" && !isSystemText(text) {
				return true
			}
		}
	}
	return false
}

// isSystemText returns true if text is system-generated content
// (e.g. <system-reminder> tags) or API noise rather than real conversation.
func isSystemText(text string) bool {
	return strings.HasPrefix(text, "<system-reminder>") ||
		strings.HasPrefix(text, "<system>") ||
		text == "Prompt is too long"
}

// isSystemAgent returns true if the agent is an internal system agent
// (e.g. autocompaction summary agents) that should be hidden from the UI.
func isSystemAgent(a session.Subagent) bool {
	return strings.HasPrefix(a.ID, "acompact-")
}

// mergedToolSummary returns a compact tool summary with counts for duplicates.
// e.g. "[Bash, Read×3, Edit]"
func mergedToolSummary(e session.Entry) string {
	seen := make(map[string]int)
	var order []string
	for _, block := range e.Content {
		if block.Type == "tool_use" {
			if seen[block.ToolName] == 0 {
				order = append(order, block.ToolName)
			}
			seen[block.ToolName]++
		}
	}
	if len(order) == 0 {
		return ""
	}
	var parts []string
	for _, name := range order {
		if seen[name] > 1 {
			parts = append(parts, fmt.Sprintf("%s×%d", name, seen[name]))
		} else {
			parts = append(parts, name)
		}
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
