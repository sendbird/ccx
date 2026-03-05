package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"time"
)

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
		ToolCounts:          make(map[string]int),
		MCPToolCounts:       make(map[string]int),
		CommandCounts:       make(map[string]int),
		SkillCounts:         make(map[string]int),
		FilesTouched:        make(map[string]bool),
		Models:              make(map[string]int),
		ModelTokens:         make(map[string]*ModelUsage),
		ToolErrors:          make(map[string]int),
		SkillErrors:         make(map[string]int),
		CommandErrors:       make(map[string]int),
		ToolCallTimestamps:  make(map[string][]time.Time),
		ToolErrorTimestamps: make(map[string][]time.Time),
	}

	var toolIDMap map[string]string
	var currentSkill, currentCommand string

	asstTurnCount := 0
	seenFirstUser := false

	var lastModel string

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

		isMeta := bytes.Contains(line, bIsMeta) || bytes.Contains(line, bIsMetaSpaced)
		if isMeta {
			if bytes.Contains(line, bIsCompactSummary) || bytes.Contains(line, bIsCompactSummaryS) {
				stats.CompactionCount++
			}
			continue
		}

		if bytes.Contains(line, bCmdTag) {
			extractCommands(line, &stats)
		}

		hasUser := bytes.Contains(line, bRoleUser) || bytes.Contains(line, bRoleUserS)
		hasAsst := bytes.Contains(line, bRoleAsst) || bytes.Contains(line, bRoleAsstS)
		if !hasUser && !hasAsst {
			continue
		}

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

		ts := extractTimestamp(line)
		if !ts.IsZero() {
			if stats.FirstTimestamp.IsZero() || ts.Before(stats.FirstTimestamp) {
				stats.FirstTimestamp = ts
			}
			if ts.After(stats.LastTimestamp) {
				stats.LastTimestamp = ts
			}
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

		if isAsst {
			if model := extractStringField(line, bModelQ, bModelQS); model != "" {
				stats.Models[model]++
				if lastModel != "" && model != lastModel {
					stats.ModelSwitches++
				}
				lastModel = model
			}
		}

		if isAsst {
			if usage := extractUsage(line); usage != nil {
				stats.TotalInputTokens += usage.InputTokens
				stats.TotalOutputTokens += usage.OutputTokens
				stats.TotalCacheReadTokens += usage.CacheReadTokens
				stats.TotalCacheCreationTokens += usage.CacheCreationTokens
				stats.OutputTokenSeries = append(stats.OutputTokenSeries, int(usage.OutputTokens))
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

		if isUser {
			hasToolResult := bytes.Contains(line, bToolRes) || bytes.Contains(line, bToolResS)
			if !hasToolResult {
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

		if isAsst {
			asstTurnCount++
		}

		if isAsst && (bytes.Contains(line, bToolUse) || bytes.Contains(line, bToolUseS)) {
			extractToolUses(line, &stats, ts)
			toolIDMap = buildToolIDMap(line)
			if skill := extractFirstSkill(line); skill != "" {
				currentSkill = skill
			}
		}

		if isUser {
			if bytes.Contains(line, bToolRes) || bytes.Contains(line, bToolResS) {
				stats.ToolResultCount += countOccurrences(line, bToolRes) + countOccurrences(line, bToolResS)
			}
			if bytes.Contains(line, bIsErrorT) || bytes.Contains(line, bIsErrorTS) {
				errCount := countOccurrences(line, bIsErrorT) + countOccurrences(line, bIsErrorTS)
				stats.ToolErrorCount += errCount
				if !ts.IsZero() {
					for range errCount {
						stats.ErrorTimestamps = append(stats.ErrorTimestamps, ts)
					}
				}
				for _, name := range extractErrorToolNames(line, toolIDMap) {
					stats.ToolErrors[name]++
					if !ts.IsZero() {
						stats.ToolErrorTimestamps[name] = append(stats.ToolErrorTimestamps[name], ts)
					}
				}
				if currentSkill != "" {
					stats.SkillErrors[currentSkill] += errCount
				}
				if currentCommand != "" {
					stats.CommandErrors[currentCommand] += errCount
				}
			}
		}
	}

	if seenFirstUser && asstTurnCount > 0 {
		stats.TurnsPerRequest = append(stats.TurnsPerRequest, asstTurnCount)
	}

	if gapCount > 0 {
		stats.AvgMsgGap = totalGap / time.Duration(gapCount)
	}

	return stats, sc.Err()
}

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

	braceStart := idx + len(marker) - 1
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

func extractToolUses(line []byte, stats *SessionStats, ts time.Time) {
	markers := [][]byte{bToolUse, bToolUseS}
	for _, marker := range markers {
		offset := 0
		for {
			idx := bytes.Index(line[offset:], marker)
			if idx < 0 {
				break
			}
			pos := offset + idx + len(marker)

			name := extractStringField(line[pos:min(pos+200, len(line))], bNameQ, bNameQS)
			if name != "" {
				stats.ToolCounts[name]++
				if !ts.IsZero() {
					stats.ToolCallTimestamps[name] = append(stats.ToolCallTimestamps[name], ts)
				}

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

				if name == "Skill" {
					if skill := extractSkillName(line, pos); skill != "" {
						stats.SkillCounts[skill]++
					}
				}

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

func isValidCommand(cmd string) bool {
	for i := 1; i < len(cmd); i++ {
		c := cmd[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	return true
}

func extractSkillName(line []byte, pos int) string {
	searchEnd := min(pos+500, len(line))
	return extractStringField(line[pos:searchEnd], bSkillQ, bSkillQS)
}

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
