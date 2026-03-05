package session

import "time"

// AggregateStats scans all session files and aggregates their statistics.
func AggregateStats(sessions []Session) GlobalStats {
	g := GlobalStats{
		ToolCounts:             make(map[string]int),
		MCPToolCounts:          make(map[string]int),
		SkillCounts:            make(map[string]int),
		CommandCounts:          make(map[string]int),
		Models:                 make(map[string]int),
		ModelTokens:            make(map[string]*ModelUsage),
		ToolErrors:             make(map[string]int),
		SkillErrors:            make(map[string]int),
		CommandErrors:          make(map[string]int),
		AllToolCallTimestamps:  make(map[string][]time.Time),
		AllToolErrorTimestamps: make(map[string][]time.Time),
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
		g.AllErrorTimestamps = append(g.AllErrorTimestamps, stats.ErrorTimestamps...)
		g.AllMsgTimestamps = append(g.AllMsgTimestamps, stats.MsgTimestamps...)
		for name, ts := range stats.ToolCallTimestamps {
			g.AllToolCallTimestamps[name] = append(g.AllToolCallTimestamps[name], ts...)
		}
		for name, ts := range stats.ToolErrorTimestamps {
			g.AllToolErrorTimestamps[name] = append(g.AllToolErrorTimestamps[name], ts...)
		}
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
	return claudePricing["sonnet"]
}
