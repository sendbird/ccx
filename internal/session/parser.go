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

// StripXMLTags removes XML-like tags such as <command-name>, </local-command-stdout>, etc.
func StripXMLTags(s string) string {
	return xmlTagRegex.ReplaceAllString(s, "")
}

type rawEntry struct {
	Type      string          `json:"type"`
	Timestamp string          `json:"timestamp"`
	IsMeta    bool            `json:"isMeta"`
	Message   json.RawMessage `json:"message"`
	UUID      string          `json:"uuid"`
	ParentID  string          `json:"parentUuid"`
	AgentID   string          `json:"agentId"`
	CWD       string          `json:"cwd"`
	GitBranch string          `json:"gitBranch"`
}

type rawMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
	Model   string          `json:"model"`
}

type rawContentBlock struct {
	Type    string          `json:"type"`
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

	return entry, nil
}

func parseContentBlocks(raw json.RawMessage) []ContentBlock {
	if raw == nil {
		return nil
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s != "" {
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
			cb.Text = b.Text
		case "tool_use":
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
