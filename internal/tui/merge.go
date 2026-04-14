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
		if b.Type == "tool_result" && b.IsError {
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

// filterAgentContextEntries removes injected context summaries from subagent entries.
// Subagents (including /btw aside agents) receive the parent's compacted context as
// their first user message. This filters it out so only the agent's own content shows.
func filterAgentContextEntries(entries []session.Entry) []session.Entry {
	if len(entries) == 0 {
		return entries
	}
	// Check first entry: if it's a user message starting with context continuation marker, skip it
	first := entries[0]
	if first.Role == "user" {
		for _, b := range first.Content {
			text := b.Text
			if b.Type == "text" && len(text) == 0 {
				// content might be a string (not blocks) — check raw
				continue
			}
			if strings.HasPrefix(text, "This session is being continued from a previous conversation") {
				return entries[1:]
			}
		}
		// Also check if content is a raw string (not blocks)
		if len(first.Content) == 0 {
			// The raw entry might have string content — already filtered by parser
		}
	}
	return entries
}

// filterSideQuestionContext strips background context from side-question (aside_question)
// agent files. These files contain the entire parent session as injected context,
// with only the last user+assistant pair being the actual side-question exchange.
// Returns a single context-summary entry followed by the real messages.
func filterSideQuestionContext(entries []session.Entry) []session.Entry {
	if len(entries) <= 2 {
		return entries
	}

	// Find the last user message — everything from there onwards is the real exchange.
	lastUserIdx := -1
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Role == "user" {
			lastUserIdx = i
			break
		}
	}
	if lastUserIdx <= 0 {
		return entries
	}

	// Build a summary entry for the collapsed context
	contextCount := lastUserIdx
	summary := session.Entry{
		Role:      "assistant",
		Timestamp: entries[0].Timestamp,
		Content: []session.ContentBlock{{
			Type:    "system_tag",
			TagName: "context",
			Text:    fmt.Sprintf("(%d background context messages from parent session)", contextCount),
		}},
	}

	result := make([]session.Entry, 0, 1+len(entries)-lastUserIdx)
	result = append(result, summary)
	result = append(result, entries[lastUserIdx:]...)
	return result
}

// isSystemAgent returns true if the agent is an internal system agent
// (e.g. autocompaction summary agents) that should be hidden from the UI.
func isSystemAgent(a session.Subagent) bool {
	return strings.HasPrefix(a.ID, "acompact-")
}

// mergedToolSummary returns a compact tool summary with counts for duplicates.
// e.g. "[Bash, Read×3, Edit]"
// Skill tool_use blocks show "/skillname" instead of "Skill".
func mergedToolSummary(e session.Entry) string {
	seen := make(map[string]int)
	var order []string
	for _, block := range e.Content {
		if block.Type == "tool_use" {
			name := block.ToolName
			if name == "Skill" {
				if skill := extractSkillFromInput(block.ToolInput); skill != "" {
					name = "/" + skill
				}
			}
			if seen[name] == 0 {
				order = append(order, name)
			}
			seen[name]++
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
