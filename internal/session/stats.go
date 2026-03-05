package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"time"
)

// SessionStats holds aggregated statistics extracted from a session JSONL file.
type SessionStats struct {
	// Token usage
	TotalInputTokens         int64
	TotalOutputTokens        int64
	TotalCacheReadTokens     int64
	TotalCacheCreationTokens int64

	// Per-assistant-message output token counts (chronological, for sparkline)
	OutputTokenSeries []int

	// Tool usage: tool name -> call count
	ToolCounts map[string]int

	// Code activity
	WriteCount   int
	EditCount    int
	ReadCount    int
	BashCount    int
	FilesTouched map[string]bool

	// Errors
	ToolResultCount int
	ToolErrorCount  int
	ToolErrors      map[string]int // tool name -> error count
	SkillErrors     map[string]int // skill name -> error count
	CommandErrors   map[string]int // command name -> error count

	// MCP tools: name -> count (subset of ToolCounts for mcp__ prefixed tools)
	MCPToolCounts map[string]int

	// Commands: slash command name -> count (e.g. "/commit" -> 2)
	CommandCounts map[string]int

	// Skills: skill name -> count (from Skill tool_use)
	SkillCounts map[string]int

	// Timeline
	FirstTimestamp time.Time
	LastTimestamp  time.Time
	MessageCount   int
	UserMsgCount   int
	AsstMsgCount   int

	// Compaction
	CompactionCount int // number of context compactions

	// Turns per user request (assistant messages between consecutive fresh user messages)
	TurnsPerRequest []int

	// Per-model token usage (for cost estimation)
	ModelTokens map[string]*ModelUsage

	// Model switching: how many times the model changed between consecutive assistant messages
	ModelSwitches int

	// Message timing
	AvgMsgGap time.Duration
	MaxMsgGap time.Duration

	// Per-message timestamps for timeline visualization
	MsgTimestamps []time.Time

	// Models used
	Models map[string]int
}

var (
	bUsage     = []byte(`"usage":{`)
	bUsageS    = []byte(`"usage": {`)
	bToolUse   = []byte(`"type":"tool_use"`)
	bToolUseS  = []byte(`"type": "tool_use"`)
	bIsErrorT  = []byte(`"is_error":true`)
	bIsErrorTS = []byte(`"is_error": true`)
	bToolRes   = []byte(`"type":"tool_result"`)
	bToolResS  = []byte(`"type": "tool_result"`)
	bNameQ     = []byte(`"name":"`)
	bNameQS    = []byte(`"name": "`)
	bFilePathQ = []byte(`"file_path":"`)
	bFilePathS = []byte(`"file_path": "`)
	bModelQ    = []byte(`"model":"`)
	bModelQS   = []byte(`"model": "`)
	bRoleAsst  = []byte(`"role":"assistant"`)
	bRoleAsstS = []byte(`"role": "assistant"`)
	bSkillQ   = []byte(`"skill":"`)
	bSkillQS  = []byte(`"skill": "`)
	bCmdTag    = []byte(`<command-name>`)
	bCmdTagEnd = []byte(`</command-name>`)
	bIDCol     = []byte(`"id":"`)
	bIDColS    = []byte(`"id": "`)
	bTUIDCol   = []byte(`"tool_use_id":"`)
	bTUIDColS  = []byte(`"tool_use_id": "`)
)

// ModelUsage tracks token counts per model for cost estimation.
type ModelUsage struct {
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
}

type rawUsage struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CacheReadTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
}

// ScanSessionStats scans a session JSONL file and computes aggregate statistics
// using byte-level pre-filtering for performance.
func ScanSessionStats(path string) (SessionStats, error) {
	f, err := os.Open(path)
	if err != nil {
		return SessionStats{}, err
	}
	defer f.Close()

	stats := SessionStats{
		ToolCounts:    make(map[string]int),
		MCPToolCounts: make(map[string]int),
		CommandCounts: make(map[string]int),
		SkillCounts:   make(map[string]int),
		FilesTouched:  make(map[string]bool),
		Models:        make(map[string]int),
		ModelTokens:   make(map[string]*ModelUsage),
		ToolErrors:    make(map[string]int),
		SkillErrors:   make(map[string]int),
		CommandErrors: make(map[string]int),
	}

	// Context tracking for error attribution
	var toolIDMap map[string]string // tool_use_id -> tool_name (from last assistant msg)
	var currentSkill, currentCommand string

	// Turns-per-request tracking
	asstTurnCount := 0  // assistant messages since last fresh user message
	seenFirstUser := false

	// Model switching tracking
	var lastModel string

	// Message gap tracking
	var lastMsgTime time.Time
	var totalGap time.Duration
	var gapCount int

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 256*1024), 10*1024*1024)

	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}

		// Count compactions (meta entries with isCompactSummary)
		isMeta := bytes.Contains(line, bIsMeta) || bytes.Contains(line, bIsMetaSpaced)
		if isMeta {
			if bytes.Contains(line, bIsCompactSummary) || bytes.Contains(line, bIsCompactSummaryS) {
				stats.CompactionCount++
			}
			continue
		}

		// Slash commands: stored as <command-name> tags on user or system lines
		if bytes.Contains(line, bCmdTag) {
			extractCommands(line, &stats)
		}

		hasUser := bytes.Contains(line, bRoleUser) || bytes.Contains(line, bRoleUserS)
		hasAsst := bytes.Contains(line, bRoleAsst) || bytes.Contains(line, bRoleAsstS)
		if !hasUser && !hasAsst {
			continue
		}

		// Resolve ambiguity: assistant messages have "model" field, user messages don't
		hasModel := bytes.Contains(line, bModelQ) || bytes.Contains(line, bModelQS)
		isAsst := hasAsst && hasModel
		isUser := !isAsst && hasUser

		stats.MessageCount++
		if isUser {
			stats.UserMsgCount++
		}
		if isAsst {
			stats.AsstMsgCount++
		}

		// Timestamp
		ts := extractTimestamp(line)
		if !ts.IsZero() {
			if stats.FirstTimestamp.IsZero() || ts.Before(stats.FirstTimestamp) {
				stats.FirstTimestamp = ts
			}
			if ts.After(stats.LastTimestamp) {
				stats.LastTimestamp = ts
			}
			// Message gap tracking
			if !lastMsgTime.IsZero() {
				gap := ts.Sub(lastMsgTime)
				if gap > 0 {
					totalGap += gap
					gapCount++
					if gap > stats.MaxMsgGap {
						stats.MaxMsgGap = gap
					}
				}
			}
			lastMsgTime = ts
			stats.MsgTimestamps = append(stats.MsgTimestamps, ts)
		}

		// Model (assistant messages)
		if isAsst {
			if model := extractStringField(line, bModelQ, bModelQS); model != "" {
				stats.Models[model]++
				// Model switching
				if lastModel != "" && model != lastModel {
					stats.ModelSwitches++
				}
				lastModel = model
			}
		}

		// Token usage (assistant messages)
		if isAsst {
			if usage := extractUsage(line); usage != nil {
				stats.TotalInputTokens += usage.InputTokens
				stats.TotalOutputTokens += usage.OutputTokens
				stats.TotalCacheReadTokens += usage.CacheReadTokens
				stats.TotalCacheCreationTokens += usage.CacheCreationTokens
				stats.OutputTokenSeries = append(stats.OutputTokenSeries, int(usage.OutputTokens))
				// Per-model token usage
				model := extractStringField(line, bModelQ, bModelQS)
				if model != "" {
					mu := stats.ModelTokens[model]
					if mu == nil {
						mu = &ModelUsage{}
						stats.ModelTokens[model] = mu
					}
					mu.InputTokens += usage.InputTokens
					mu.OutputTokens += usage.OutputTokens
					mu.CacheReadTokens += usage.CacheReadTokens
					mu.CacheCreationTokens += usage.CacheCreationTokens
				}
			}
		}

		// Context tracking: detect new user turn (no tool_result = fresh prompt)
		if isUser {
			hasToolResult := bytes.Contains(line, bToolRes) || bytes.Contains(line, bToolResS)
			if !hasToolResult {
				// Fresh user request — record turns from previous request
				if seenFirstUser && asstTurnCount > 0 {
					stats.TurnsPerRequest = append(stats.TurnsPerRequest, asstTurnCount)
				}
				asstTurnCount = 0
				seenFirstUser = true
				currentSkill = ""
				currentCommand = ""
				if bytes.Contains(line, bCmdTag) {
					currentCommand = extractFirstCommand(line)
				}
			}
		}

		// Count assistant turns for turns-per-request
		if isAsst {
			asstTurnCount++
		}

		// Tool use (assistant messages have tool_use blocks)
		if isAsst && (bytes.Contains(line, bToolUse) || bytes.Contains(line, bToolUseS)) {
			extractToolUses(line, &stats)
			toolIDMap = buildToolIDMap(line)
			if skill := extractFirstSkill(line); skill != "" {
				currentSkill = skill
			}
		}

		// Tool results with errors (user messages contain tool_result blocks)
		if isUser {
			if bytes.Contains(line, bToolRes) || bytes.Contains(line, bToolResS) {
				stats.ToolResultCount += countOccurrences(line, bToolRes) + countOccurrences(line, bToolResS)
			}
			if bytes.Contains(line, bIsErrorT) || bytes.Contains(line, bIsErrorTS) {
				errCount := countOccurrences(line, bIsErrorT) + countOccurrences(line, bIsErrorTS)
				stats.ToolErrorCount += errCount
				// Attribute errors to specific tools via tool_use_id matching
				for _, name := range extractErrorToolNames(line, toolIDMap) {
					stats.ToolErrors[name]++
				}
				// Attribute to active skill/command context
				if currentSkill != "" {
					stats.SkillErrors[currentSkill] += errCount
				}
				if currentCommand != "" {
					stats.CommandErrors[currentCommand] += errCount
				}
			}
		}
	}

	// Flush last request's turns
	if seenFirstUser && asstTurnCount > 0 {
		stats.TurnsPerRequest = append(stats.TurnsPerRequest, asstTurnCount)
	}

	// Average message gap
	if gapCount > 0 {
		stats.AvgMsgGap = totalGap / time.Duration(gapCount)
	}

	return stats, sc.Err()
}

// extractUsage finds the "usage":{...} object in a line and unmarshals just that portion.
func extractUsage(line []byte) *rawUsage {
	marker := bUsage
	idx := bytes.Index(line, marker)
	if idx < 0 {
		marker = bUsageS
		idx = bytes.Index(line, marker)
	}
	if idx < 0 {
		return nil
	}

	// Find the opening brace
	braceStart := idx + len(marker) - 1 // points to '{'
	depth := 0
	for i := braceStart; i < len(line); i++ {
		if line[i] == '{' {
			depth++
		} else if line[i] == '}' {
			depth--
			if depth == 0 {
				var u rawUsage
				if json.Unmarshal(line[braceStart:i+1], &u) == nil {
					return &u
				}
				return nil
			}
		}
	}
	return nil
}

// extractToolUses finds all tool_use blocks in a line and records tool names and file paths.
func extractToolUses(line []byte, stats *SessionStats) {
	markers := [][]byte{bToolUse, bToolUseS}
	for _, marker := range markers {
		offset := 0
		for {
			idx := bytes.Index(line[offset:], marker)
			if idx < 0 {
				break
			}
			pos := offset + idx + len(marker)

			// Find tool name: search forward for "name":"
			name := extractStringField(line[pos:min(pos+200, len(line))], bNameQ, bNameQS)
			if name != "" {
				stats.ToolCounts[name]++

				// Categorize
				if len(name) > 5 && name[:5] == "mcp__" {
					stats.MCPToolCounts[name]++
				}
				switch name {
				case "Write":
					stats.WriteCount++
				case "Edit":
					stats.EditCount++
				case "Read":
					stats.ReadCount++
				case "Bash":
					stats.BashCount++
				}

				// Extract skill name for Skill tool calls
				if name == "Skill" {
					if skill := extractSkillName(line, pos); skill != "" {
						stats.SkillCounts[skill]++
					}
				}

				// Extract file_path for code activity
				if name == "Write" || name == "Edit" || name == "Read" {
					searchEnd := min(pos+2000, len(line))
					fp := extractStringField(line[pos:searchEnd], bFilePathQ, bFilePathS)
					if fp != "" {
						stats.FilesTouched[fp] = true
					}
				}
			}

			offset = pos
		}
	}
}

// extractStringField extracts a JSON string value using two marker variants (with/without space).
func extractStringField(line []byte, marker1, marker2 []byte) string {
	idx := bytes.Index(line, marker1)
	markerLen := len(marker1)
	if idx < 0 {
		idx = bytes.Index(line, marker2)
		markerLen = len(marker2)
	}
	if idx < 0 {
		return ""
	}
	start := idx + markerLen
	return extractJSONString(line[start:])
}

// extractCommands finds slash commands stored as <command-name>/cmd</command-name> tags.
func extractCommands(line []byte, stats *SessionStats) {
	offset := 0
	for {
		idx := bytes.Index(line[offset:], bCmdTag)
		if idx < 0 {
			return
		}
		start := offset + idx + len(bCmdTag)
		end := bytes.Index(line[start:], bCmdTagEnd)
		if end < 0 || end > 50 {
			offset = start
			continue
		}
		cmd := string(line[start : start+end])
		if len(cmd) > 1 && cmd[0] == '/' && isValidCommand(cmd) {
			stats.CommandCounts[cmd]++
		}
		offset = start + end + len(bCmdTagEnd)
	}
}

// isValidCommand checks that a command name contains only letters, digits, and hyphens.
func isValidCommand(cmd string) bool {
	for i := 1; i < len(cmd); i++ {
		c := cmd[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	return true
}

// extractSkillName finds the "skill":"name" field near a Skill tool_use block.
func extractSkillName(line []byte, pos int) string {
	searchEnd := min(pos+500, len(line))
	return extractStringField(line[pos:searchEnd], bSkillQ, bSkillQS)
}

// extractFirstCommand returns the first slash command from a line, or "".
func extractFirstCommand(line []byte) string {
	idx := bytes.Index(line, bCmdTag)
	if idx < 0 {
		return ""
	}
	start := idx + len(bCmdTag)
	end := bytes.Index(line[start:], bCmdTagEnd)
	if end < 0 || end > 50 {
		return ""
	}
	cmd := string(line[start : start+end])
	if len(cmd) > 1 && cmd[0] == '/' && isValidCommand(cmd) {
		return cmd
	}
	return ""
}

// extractFirstSkill finds the first Skill tool_use in a line and returns the skill name.
func extractFirstSkill(line []byte) string {
	for _, marker := range [][]byte{bToolUse, bToolUseS} {
		offset := 0
		for {
			idx := bytes.Index(line[offset:], marker)
			if idx < 0 {
				break
			}
			pos := offset + idx + len(marker)
			name := extractStringField(line[pos:min(pos+200, len(line))], bNameQ, bNameQS)
			if name == "Skill" {
				if skill := extractSkillName(line, pos); skill != "" {
					return skill
				}
			}
			offset = pos
		}
	}
	return ""
}

// buildToolIDMap extracts tool_use id→name mappings from an assistant message line.
func buildToolIDMap(line []byte) map[string]string {
	idMap := make(map[string]string)
	for _, marker := range [][]byte{bToolUse, bToolUseS} {
		offset := 0
		for {
			idx := bytes.Index(line[offset:], marker)
			if idx < 0 {
				break
			}
			pos := offset + idx + len(marker)
			windowEnd := min(pos+500, len(line))
			window := line[pos:windowEnd]

			name := extractStringField(window, bNameQ, bNameQS)
			id := extractStringField(window, bIDCol, bIDColS)

			if name != "" && id != "" {
				idMap[id] = name
			}
			offset = pos
		}
	}
	return idMap
}

// extractErrorToolNames finds tool names for each is_error:true in a user message
// by matching tool_use_id back to the tool ID map from the preceding assistant message.
func extractErrorToolNames(line []byte, idMap map[string]string) []string {
	if len(idMap) == 0 {
		return nil
	}
	var names []string
	for _, errMarker := range [][]byte{bIsErrorT, bIsErrorTS} {
		offset := 0
		for {
			idx := bytes.Index(line[offset:], errMarker)
			if idx < 0 {
				break
			}
			pos := offset + idx
			// Search backward up to 500 bytes for tool_use_id
			searchStart := max(pos-500, 0)
			segment := line[searchStart:pos]
			id := lastStringField(segment, bTUIDCol, bTUIDColS)
			if id != "" {
				if name, ok := idMap[id]; ok {
					names = append(names, name)
				}
			}
			offset = pos + len(errMarker)
		}
	}
	return names
}

// lastStringField finds the LAST occurrence of a marker and extracts the JSON string value.
func lastStringField(line []byte, marker1, marker2 []byte) string {
	idx := bytes.LastIndex(line, marker1)
	markerLen := len(marker1)
	if idx2 := bytes.LastIndex(line, marker2); idx2 > idx {
		idx = idx2
		markerLen = len(marker2)
	}
	if idx < 0 {
		return ""
	}
	start := idx + markerLen
	return extractJSONString(line[start:])
}

// GlobalStats holds aggregated statistics across all sessions.
type GlobalStats struct {
	SessionCount  int
	TotalMessages int
	TotalUserMsgs int
	TotalAsstMsgs int
	TotalDuration time.Duration
	AvgDuration   time.Duration

	TotalInputTokens         int64
	TotalOutputTokens        int64
	TotalCacheReadTokens     int64
	TotalCacheCreationTokens int64

	ToolCounts    map[string]int
	MCPToolCounts map[string]int
	SkillCounts   map[string]int
	CommandCounts map[string]int
	Models        map[string]int

	TotalWrites, TotalEdits, TotalFiles int
	TotalToolResults, TotalToolErrors   int
	TotalCompactions                    int
	SessionsWithCompaction              int

	// Cost estimation
	TotalCostUSD   float64
	ModelTokens    map[string]*ModelUsage

	// Model switching
	TotalModelSwitches    int
	SessionsWithSwitches  int

	ToolErrors    map[string]int // tool name -> error count
	SkillErrors   map[string]int // skill name -> error count
	CommandErrors map[string]int // command name -> error count

	// Turns per request (aggregated across all sessions)
	AllTurnsPerRequest []int // every request's turn count for distribution

	SessionDurations []time.Duration // per-session durations for sparkline
	SessionTokens    []int64         // output tokens per session for sparkline
	SessionStarts    []time.Time     // session start times for daily activity
}

// AggregateStats scans all session files and aggregates their statistics.
func AggregateStats(sessions []Session) GlobalStats {
	g := GlobalStats{
		ToolCounts:    make(map[string]int),
		MCPToolCounts: make(map[string]int),
		SkillCounts:   make(map[string]int),
		CommandCounts: make(map[string]int),
		Models:        make(map[string]int),
		ModelTokens:   make(map[string]*ModelUsage),
		ToolErrors:    make(map[string]int),
		SkillErrors:   make(map[string]int),
		CommandErrors: make(map[string]int),
	}

	allFiles := make(map[string]bool)

	for _, sess := range sessions {
		stats, err := ScanSessionStats(sess.FilePath)
		if err != nil {
			continue
		}

		g.SessionCount++
		g.TotalMessages += stats.MessageCount
		g.TotalUserMsgs += stats.UserMsgCount
		g.TotalAsstMsgs += stats.AsstMsgCount

		g.TotalInputTokens += stats.TotalInputTokens
		g.TotalOutputTokens += stats.TotalOutputTokens
		g.TotalCacheReadTokens += stats.TotalCacheReadTokens
		g.TotalCacheCreationTokens += stats.TotalCacheCreationTokens

		g.TotalWrites += stats.WriteCount
		g.TotalEdits += stats.EditCount
		g.TotalToolResults += stats.ToolResultCount
		g.TotalToolErrors += stats.ToolErrorCount
		g.TotalCompactions += stats.CompactionCount
		if stats.CompactionCount > 0 {
			g.SessionsWithCompaction++
		}

		// Cost and model tokens
		sessCost := EstimateCost(stats.ModelTokens)
		g.TotalCostUSD += sessCost
		for model, mu := range stats.ModelTokens {
			gmu := g.ModelTokens[model]
			if gmu == nil {
				gmu = &ModelUsage{}
				g.ModelTokens[model] = gmu
			}
			gmu.InputTokens += mu.InputTokens
			gmu.OutputTokens += mu.OutputTokens
			gmu.CacheReadTokens += mu.CacheReadTokens
			gmu.CacheCreationTokens += mu.CacheCreationTokens
		}

		// Model switches
		g.TotalModelSwitches += stats.ModelSwitches
		if stats.ModelSwitches > 0 {
			g.SessionsWithSwitches++
		}

		for k, v := range stats.ToolCounts {
			g.ToolCounts[k] += v
		}
		for k, v := range stats.MCPToolCounts {
			g.MCPToolCounts[k] += v
		}
		for k, v := range stats.SkillCounts {
			g.SkillCounts[k] += v
		}
		for k, v := range stats.CommandCounts {
			g.CommandCounts[k] += v
		}
		for k, v := range stats.Models {
			g.Models[k] += v
		}
		for k, v := range stats.ToolErrors {
			g.ToolErrors[k] += v
		}
		for k, v := range stats.SkillErrors {
			g.SkillErrors[k] += v
		}
		for k, v := range stats.CommandErrors {
			g.CommandErrors[k] += v
		}
		for f := range stats.FilesTouched {
			allFiles[f] = true
		}

		dur := stats.LastTimestamp.Sub(stats.FirstTimestamp)
		if dur > 0 {
			g.TotalDuration += dur
			g.SessionDurations = append(g.SessionDurations, dur)
		}
		g.SessionTokens = append(g.SessionTokens, stats.TotalOutputTokens)
		if !stats.FirstTimestamp.IsZero() {
			g.SessionStarts = append(g.SessionStarts, stats.FirstTimestamp)
		}
		g.AllTurnsPerRequest = append(g.AllTurnsPerRequest, stats.TurnsPerRequest...)
	}

	g.TotalFiles = len(allFiles)
	if g.SessionCount > 0 {
		g.AvgDuration = g.TotalDuration / time.Duration(g.SessionCount)
	}

	return g
}

// modelPricing holds per-million-token pricing for Claude models.
type modelPricing struct {
	input, output, cacheRead, cacheWrite float64
}

var claudePricing = map[string]modelPricing{
	"opus":   {input: 15.0, output: 75.0, cacheRead: 1.50, cacheWrite: 18.75},
	"sonnet": {input: 3.0, output: 15.0, cacheRead: 0.30, cacheWrite: 3.75},
	"haiku":  {input: 1.0, output: 5.0, cacheRead: 0.10, cacheWrite: 1.25},
}

// EstimateCost computes approximate USD cost from per-model token usage.
func EstimateCost(modelTokens map[string]*ModelUsage) float64 {
	total := 0.0
	for model, mu := range modelTokens {
		p := matchPricing(model)
		total += float64(mu.InputTokens) * p.input / 1_000_000
		total += float64(mu.OutputTokens) * p.output / 1_000_000
		total += float64(mu.CacheReadTokens) * p.cacheRead / 1_000_000
		total += float64(mu.CacheCreationTokens) * p.cacheWrite / 1_000_000
	}
	return total
}

func matchPricing(model string) modelPricing {
	for key, p := range claudePricing {
		if len(model) >= len(key) && contains(model, key) {
			return p
		}
	}
	// Default to sonnet pricing
	return claudePricing["sonnet"]
}

func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func countOccurrences(data, pattern []byte) int {
	count := 0
	offset := 0
	for {
		idx := bytes.Index(data[offset:], pattern)
		if idx < 0 {
			break
		}
		count++
		offset += idx + len(pattern)
	}
	return count
}
