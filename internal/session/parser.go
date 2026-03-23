package session

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

var ansiRegex = regexp.MustCompile(`(?:\x1b|\\u001b)\[[0-9;]*m`)
var xmlTagRegex = regexp.MustCompile(`</?[a-zA-Z][a-zA-Z0-9_-]*>`)

// xmlOpenTagRegex matches opening XML-like tags: <tag-name>
var xmlOpenTagRegex = regexp.MustCompile(`<([a-zA-Z][a-zA-Z0-9_-]*)>`)

// systemTagNames lists XML tag names whose content should be treated as
// foldable system blocks rather than user-visible text.
var systemTagNames = map[string]bool{
	"system-reminder":          true,
	"task-notification":        true,
	"analysis":                 true,
	"command-name":             true,
	"command-message":          true,
	"command-args":             true,
	"local-command-caveat":     true,
	"local-command-stdout":     true,
	"available-deferred-tools": true,
}

// StripXMLTags removes XML-like tags such as <command-name>, </local-command-stdout>, etc.
func StripXMLTags(s string) string {
	return xmlTagRegex.ReplaceAllString(s, "")
}

// splitSystemTags splits a text block containing system XML tags into separate
// ContentBlocks. Tagged sections become type="system_tag" with TagName set;
// remaining text stays type="text". Returns nil if no splitting occurred.
func splitSystemTags(text string) []ContentBlock {
	// Find all opening tags, then locate matching closing tags
	type tagMatch struct {
		name       string
		outerStart int // start of <tag>
		innerStart int // start of content after <tag>
		innerEnd   int // end of content before </tag>
		outerEnd   int // end of </tag>
	}

	openMatches := xmlOpenTagRegex.FindAllStringSubmatchIndex(text, -1)
	if len(openMatches) == 0 {
		return nil
	}

	var matches []tagMatch
	for _, m := range openMatches {
		tagName := text[m[2]:m[3]]
		if !systemTagNames[tagName] {
			continue
		}
		closeTag := "</" + tagName + ">"
		closeIdx := strings.Index(text[m[1]:], closeTag)
		if closeIdx < 0 {
			continue
		}
		matches = append(matches, tagMatch{
			name:       tagName,
			outerStart: m[0],
			innerStart: m[1],
			innerEnd:   m[1] + closeIdx,
			outerEnd:   m[1] + closeIdx + len(closeTag),
		})
	}

	if len(matches) == 0 {
		return nil
	}

	var blocks []ContentBlock
	cursor := 0
	for _, m := range matches {
		if m.outerStart < cursor {
			continue // overlapping, skip
		}
		before := strings.TrimSpace(text[cursor:m.outerStart])
		if before != "" {
			blocks = append(blocks, ContentBlock{Type: "text", Text: before})
		}
		inner := text[m.innerStart:m.innerEnd]
		blocks = append(blocks, ContentBlock{Type: "system_tag", Text: inner, TagName: m.name})
		cursor = m.outerEnd
	}
	after := strings.TrimSpace(text[cursor:])
	if after != "" {
		blocks = append(blocks, ContentBlock{Type: "text", Text: after})
	}

	if len(blocks) == 0 {
		return nil
	}
	return blocks
}

type rawEntry struct {
	Type          string          `json:"type"`
	Timestamp     string          `json:"timestamp"`
	IsMeta        bool            `json:"isMeta"`
	Message       json.RawMessage `json:"message"`
	UUID          string          `json:"uuid"`
	ParentID      string          `json:"parentUuid"`
	AgentID       string          `json:"agentId"`
	CWD           string          `json:"cwd"`
	GitBranch     string          `json:"gitBranch"`
	ImagePasteIDs []int           `json:"imagePasteIds"`
}

type rawMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Model   string          `json:"model"`
}

type rawContentBlock struct {
	Type    string          `json:"type"`
	ID      string          `json:"id"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Content json.RawMessage `json:"content"`
	Input   any             `json:"input"`
	IsError bool            `json:"is_error"`
	Source  *imageSource    `json:"source"`
}

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
}

func ParseEntry(line string) (Entry, error) {
	var raw rawEntry
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return Entry{}, fmt.Errorf("unmarshal entry: %w", err)
	}

	entry := Entry{
		Type:     raw.Type,
		IsMeta:   raw.IsMeta,
		UUID:     raw.UUID,
		ParentID: raw.ParentID,
		AgentID:  raw.AgentID,
		RawJSON:  line,
	}

	if raw.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339Nano, raw.Timestamp); err == nil {
			entry.Timestamp = t
		} else if t, err := time.Parse(time.RFC3339, raw.Timestamp); err == nil {
			entry.Timestamp = t
		}
	}

	if raw.Message != nil {
		var msg rawMessage
		if err := json.Unmarshal(raw.Message, &msg); err == nil {
			entry.Role = msg.Role
			entry.Model = msg.Model
			entry.Content = parseContentBlocks(msg.Content)
		}
	}

	// Assign image paste IDs to image content blocks
	if len(raw.ImagePasteIDs) > 0 {
		imgIdx := 0
		for i := range entry.Content {
			if entry.Content[i].Type == "image" && imgIdx < len(raw.ImagePasteIDs) {
				entry.Content[i].ImagePasteID = raw.ImagePasteIDs[imgIdx]
				imgIdx++
			}
		}
	}

	return entry, nil
}

func parseContentBlocks(raw json.RawMessage) []ContentBlock {
	if raw == nil {
		return nil
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s != "" {
		if split := splitSystemTags(s); split != nil {
			return split
		}
		return []ContentBlock{{Type: "text", Text: s}}
	}

	var blocks []rawContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil
	}

	result := make([]ContentBlock, 0, len(blocks))
	for _, b := range blocks {
		cb := ContentBlock{Type: b.Type, IsError: b.IsError}
		switch b.Type {
		case "text":
			// Split text blocks containing system XML tags
			if split := splitSystemTags(b.Text); split != nil {
				result = append(result, split...)
				continue
			}
			cb.Text = b.Text
		case "tool_use":
			cb.ID = b.ID
			cb.ToolName = b.Name
			if b.Input != nil {
				inputBytes, _ := json.Marshal(b.Input)
				cb.ToolInput = string(inputBytes)
			}
		case "tool_result":
			cb.Text = parseToolResultContent(b.Content)
		case "thinking":
			cb.Text = b.Text
		case "image":
			media := "image"
			if b.Source != nil && b.Source.MediaType != "" {
				media = b.Source.MediaType
			}
			cb.Text = fmt.Sprintf("[Image: %s]", media)
		default:
			cb.Text = b.Text
		}
		result = append(result, cb)
	}
	return result
}

// parseToolResultContent handles tool_result content which can be a string or
// an array of content blocks like [{"type":"text","text":"..."}].
func parseToolResultContent(raw json.RawMessage) string {
	if raw == nil {
		return ""
	}
	// Try string first
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Try array of blocks
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return string(raw)
}

// rawHookProgress represents a hook_progress entry from the JSONL.
type rawHookProgress struct {
	Type      string `json:"type"` // "progress"
	ToolUseID string `json:"toolUseID"`
	Data      struct {
		Type      string `json:"type"` // "hook_progress"
		HookEvent string `json:"hookEvent"`
		HookName  string `json:"hookName"`
		Command   string `json:"command"`
	} `json:"data"`
}

// parseHookProgress extracts hook info from a progress line.
// Returns the toolUseID and HookInfo, or empty if not a hook_progress.
// Skips "callback" hooks (internal permission checks) as they're not informative.
func parseHookProgress(line []byte) (string, HookInfo, bool) {
	var raw rawHookProgress
	if err := json.Unmarshal(line, &raw); err != nil {
		return "", HookInfo{}, false
	}
	if raw.Data.Type != "hook_progress" || raw.ToolUseID == "" {
		return "", HookInfo{}, false
	}
	// Skip built-in callback hooks — they fire on every tool use and carry no useful info
	if raw.Data.Command == "" || raw.Data.Command == "callback" {
		return "", HookInfo{}, false
	}
	return raw.ToolUseID, HookInfo{
		Event:   raw.Data.HookEvent,
		Name:    raw.Data.HookName,
		Command: raw.Data.Command,
	}, true
}

func ExtractMetadata(line string) (cwd, gitBranch string) {
	var raw rawEntry
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return "", ""
	}
	return raw.CWD, raw.GitBranch
}

func EntryPreview(e Entry) string {
	for _, block := range e.Content {
		if block.Type == "text" {
			text := strings.TrimSpace(block.Text)
			if text == "" {
				continue
			}
			text = strings.ReplaceAll(text, "\n", " ")
			text = ansiRegex.ReplaceAllString(text, "")
			text = xmlTagRegex.ReplaceAllString(text, "")
			if len(text) > 100 {
				return text[:97] + "..."
			}
			return text
		}
	}

	var tools []string
	var images int
	for _, block := range e.Content {
		switch block.Type {
		case "tool_use":
			tools = append(tools, block.ToolName)
		case "image":
			images++
		}
	}
	if len(tools) > 0 {
		return "[" + strings.Join(tools, ", ") + "]"
	}
	if images > 0 {
		return fmt.Sprintf("[%d image(s)]", images)
	}
	return "(no content)"
}

func ToolSummary(e Entry) string {
	var tools []string
	for _, block := range e.Content {
		if block.Type == "tool_use" {
			tools = append(tools, block.ToolName)
		}
	}
	if len(tools) == 0 {
		return ""
	}
	return "[" + strings.Join(tools, ", ") + "]"
}
