package tui

import (
	"fmt"
	"strings"

	"github.com/sendbird/ccx/internal/session"
)

func renderSessionStats(stats session.SessionStats, width int) string {
	if stats.MessageCount == 0 {
		return dimStyle.Render("(no data)")
	}

	var sb strings.Builder
	titleStyle := statTitleStyle
	numStyle := statNumStyle
	labelStyle := dimStyle
	ruler := dimStyle.Render(strings.Repeat("─", min(width, 40)))

	// ── TIMELINE ──
	sb.WriteString(titleStyle.Render("TIMELINE") + "\n")
	sb.WriteString(ruler + "\n")

	dur := stats.LastTimestamp.Sub(stats.FirstTimestamp)
	sb.WriteString(fmt.Sprintf("  Duration  %s\n", numStyle.Render(fmtDuration(dur))))

	if !stats.FirstTimestamp.IsZero() {
		sb.WriteString(fmt.Sprintf("  Period    %s\n",
			labelStyle.Render(stats.FirstTimestamp.Format("15:04")+" → "+stats.LastTimestamp.Format("15:04"))))
	}

	sb.WriteString(fmt.Sprintf("  Messages  %s",
		numStyle.Render(fmt.Sprintf("%d", stats.MessageCount))))
	sb.WriteString(labelStyle.Render(fmt.Sprintf(" (%d user, %d assistant)",
		stats.UserMsgCount, stats.AsstMsgCount)))
	sb.WriteString("\n")

	if dur > 0 {
		rate := float64(stats.MessageCount) / dur.Minutes()
		rateLine := fmt.Sprintf("%.1f msg/min", rate)
		// Inline rate sparkline
		if len(stats.MsgTimestamps) > 2 {
			rateSparkW := min(width-24, 20)
			if rateSparkW > 5 {
				rateBuckets := timelineBuckets(stats.MsgTimestamps, stats.FirstTimestamp, stats.LastTimestamp, rateSparkW)
				rateLine += "  " + sparkline(rateBuckets, rateSparkW)
			}
		}
		sb.WriteString(fmt.Sprintf("  Rate      %s\n", labelStyle.Render(rateLine)))
	}
	if stats.CompactionCount > 0 {
		warnStyle := errorStyle
		sb.WriteString(fmt.Sprintf("  Compacted %s\n",
			warnStyle.Render(fmt.Sprintf("%d×", stats.CompactionCount))))
	}
	if len(stats.TurnsPerRequest) > 0 {
		avg, maxT := turnsStats(stats.TurnsPerRequest)
		sb.WriteString(fmt.Sprintf("  Turns/Req %s",
			numStyle.Render(fmt.Sprintf("%.1f avg", avg))))
		sb.WriteString(labelStyle.Render(fmt.Sprintf("  (max %d, %d reqs)", maxT, len(stats.TurnsPerRequest))))
		sb.WriteString("\n")
		// Sparkline of turns per request
		if len(stats.TurnsPerRequest) > 2 {
			sparkW := min(width-12, 40)
			if sparkW > 5 {
				spark := sparkline(stats.TurnsPerRequest, sparkW)
				sb.WriteString(fmt.Sprintf("  Per Req   %s\n",
					labelStyle.Render(spark)))
			}
		}
	}
	// Activity timeline sparkline (message density over session duration)
	if len(stats.MsgTimestamps) > 2 && dur > 0 {
		sparkW := min(width-12, 40)
		if sparkW > 5 {
			buckets := timelineBuckets(stats.MsgTimestamps, stats.FirstTimestamp, stats.LastTimestamp, sparkW)
			spark := sparkline(buckets, sparkW)
			userStyle := statInputStyle
			sb.WriteString(fmt.Sprintf("  Activity  %s\n", userStyle.Render(spark)))
			// Error timeline (same time scale, red)
			if len(stats.ErrorTimestamps) > 0 {
				errBuckets := timelineBuckets(stats.ErrorTimestamps, stats.FirstTimestamp, stats.LastTimestamp, sparkW)
				if hasNonZero(errBuckets) {
					errSpark := sparkline(errBuckets, sparkW)
					errStyle := errorStyle
					sb.WriteString(fmt.Sprintf("  Errors    %s\n", errStyle.Render(errSpark)))
				}
			}
			// Time axis labels
			sb.WriteString(fmt.Sprintf("  %s%s%s\n",
				labelStyle.Render(stats.FirstTimestamp.Format("15:04")),
				labelStyle.Render(strings.Repeat(" ", max(sparkW-10, 0))),
				labelStyle.Render(stats.LastTimestamp.Format("15:04"))))
		}
	}
	sb.WriteString("\n")

	// ── TOKENS ──
	sb.WriteString(titleStyle.Render("TOKENS") + "\n")
	sb.WriteString(ruler + "\n")

	totalInput := stats.TotalInputTokens + stats.TotalCacheReadTokens + stats.TotalCacheCreationTokens
	cacheRatio := float64(0)
	if totalInput > 0 {
		cacheRatio = float64(stats.TotalCacheReadTokens) * 100 / float64(totalInput)
	}

	inputStyle := statInputStyle
	outputStyle := statOutputStyle

	sb.WriteString(fmt.Sprintf("  Input       %s", inputStyle.Render(fmtNum(totalInput))))
	if cacheRatio > 0 {
		sb.WriteString(labelStyle.Render(fmt.Sprintf("  (cache hit %.0f%%)", cacheRatio)))
	}
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("  Output      %s\n", outputStyle.Render(fmtNum(stats.TotalOutputTokens))))
	sb.WriteString(fmt.Sprintf("  Cache Read  %s\n", labelStyle.Render(fmtNum(stats.TotalCacheReadTokens))))
	sb.WriteString(fmt.Sprintf("  Cache Write %s\n", labelStyle.Render(fmtNum(stats.TotalCacheCreationTokens))))

	// Cost estimate
	cost := session.EstimateCost(stats.ModelTokens)
	if cost > 0 {
		costStyle := statCostStyle
		sb.WriteString(fmt.Sprintf("  Cost        %s\n", costStyle.Render(fmtCost(cost))))
	}

	// Sparkline for output tokens
	if len(stats.OutputTokenSeries) > 1 {
		sparkW := min(width-12, 60)
		if sparkW > 5 {
			spark := sparkline(stats.OutputTokenSeries, sparkW)
			sb.WriteString(fmt.Sprintf("  Output    %s\n",
				outputStyle.Render(spark)))
		}
	}

	// Tokens per turn
	if stats.AsstMsgCount > 0 {
		tokPerTurn := stats.TotalOutputTokens / int64(stats.AsstMsgCount)
		sb.WriteString(fmt.Sprintf("  Out/Turn    %s\n", labelStyle.Render(fmtNum(tokPerTurn))))
	}
	sb.WriteString("\n")

	// ── EFFICIENCY ──
	hasEfficiency := stats.ModelSwitches > 0 || stats.AvgMsgGap > 0 || stats.ToolCounts["Agent"] > 0
	if hasEfficiency {
		sb.WriteString(titleStyle.Render("EFFICIENCY") + "\n")
		sb.WriteString(ruler + "\n")
		if stats.ModelSwitches > 0 {
			sb.WriteString(fmt.Sprintf("  Model Switches  %s\n",
				numStyle.Render(fmt.Sprintf("%d", stats.ModelSwitches))))
		}
		if agentCount := stats.ToolCounts["Agent"]; agentCount > 0 {
			sb.WriteString(fmt.Sprintf("  Agent Spawns    %s\n",
				numStyle.Render(fmt.Sprintf("%d", agentCount))))
		}
		if stats.AvgMsgGap > 0 {
			sb.WriteString(fmt.Sprintf("  Avg Msg Gap     %s",
				labelStyle.Render(fmtDuration(stats.AvgMsgGap))))
			if stats.MaxMsgGap > 0 {
				sb.WriteString(labelStyle.Render(fmt.Sprintf("  (max %s)", fmtDuration(stats.MaxMsgGap))))
			}
			sb.WriteString("\n")
		}
		sb.WriteString("\n")
	}

	// ── TOOLS ──
	if len(stats.ToolCounts) > 0 {
		totalCalls := 0
		for _, c := range stats.ToolCounts {
			totalCalls += c
		}
		header := fmt.Sprintf("TOOLS (%d calls)", totalCalls)
		if stats.ToolErrorCount > 0 {
			errRate := float64(stats.ToolErrorCount) * 100 / float64(max(stats.ToolResultCount, 1))
			eStyle := errorStyle
			header += eStyle.Render(fmt.Sprintf("  %d errors (%.1f%%)", stats.ToolErrorCount, errRate))
		}
		sb.WriteString(titleStyle.Render(header) + "\n")
		sb.WriteString(ruler + "\n")
		builtinCounts := make(map[string]int)
		for k, v := range stats.ToolCounts {
			if len(k) <= 5 || k[:5] != "mcp__" {
				builtinCounts[k] = v
			}
		}
		builtinErrors := make(map[string]int)
		for k, v := range stats.ToolErrors {
			if len(k) <= 5 || k[:5] != "mcp__" {
				builtinErrors[k] = v
			}
		}
		renderToolBarWithErrors(&sb, builtinCounts, builtinErrors, width, 10)
		sb.WriteString("\n")
	}

	// ── MCP TOOLS ──
	if len(stats.MCPToolCounts) > 0 {
		totalMCP := 0
		for _, c := range stats.MCPToolCounts {
			totalMCP += c
		}
		mcpErrors := make(map[string]int)
		totalMCPErrors := 0
		for k, v := range stats.ToolErrors {
			if len(k) > 5 && k[:5] == "mcp__" {
				mcpErrors[k] = v
				totalMCPErrors += v
			}
		}
		header := fmt.Sprintf("MCP TOOLS (%d calls)", totalMCP)
		if totalMCPErrors > 0 {
			errRate := float64(totalMCPErrors) * 100 / float64(max(totalMCP, 1))
			eStyle := errorStyle
			header += eStyle.Render(fmt.Sprintf("  %d errors (%.1f%%)", totalMCPErrors, errRate))
		}
		sb.WriteString(titleStyle.Render(header) + "\n")
		sb.WriteString(ruler + "\n")
		renderToolBarWithErrors(&sb, stats.MCPToolCounts, mcpErrors, width, 10)
		sb.WriteString("\n")
	}

	// ── TOOL TIMELINES ──
	if len(stats.ToolCallTimestamps) > 0 && dur > 0 {
		renderToolTimelines(&sb, stats.ToolCallTimestamps, stats.ToolErrorTimestamps, stats.ToolCounts, stats.FirstTimestamp, stats.LastTimestamp, width, 10)
	}

	// ── CODE ──
	if stats.WriteCount > 0 || stats.EditCount > 0 {
		sb.WriteString(titleStyle.Render("CODE") + "\n")
		sb.WriteString(ruler + "\n")
		sb.WriteString(fmt.Sprintf("  Write %s  Edit %s  Files %s\n",
			numStyle.Render(fmt.Sprintf("%d", stats.WriteCount)),
			numStyle.Render(fmt.Sprintf("%d", stats.EditCount)),
			numStyle.Render(fmt.Sprintf("%d", len(stats.FilesTouched))),
		))
		sb.WriteString("\n")
	}

	// ── COMMANDS ──
	if len(stats.CommandCounts) > 0 {
		totalCmds := 0
		for _, c := range stats.CommandCounts {
			totalCmds += c
		}
		sb.WriteString(titleStyle.Render(fmt.Sprintf("COMMANDS (%d)", totalCmds)) + "\n")
		sb.WriteString(ruler + "\n")
		renderToolBarWithErrors(&sb, stats.CommandCounts, stats.CommandErrors, width, 10)
		sb.WriteString("\n")
	}

	// ── SKILLS ──
	if len(stats.SkillCounts) > 0 {
		totalSkills := 0
		for _, c := range stats.SkillCounts {
			totalSkills += c
		}
		sb.WriteString(titleStyle.Render(fmt.Sprintf("SKILLS (%d)", totalSkills)) + "\n")
		sb.WriteString(ruler + "\n")
		renderToolBarWithErrors(&sb, stats.SkillCounts, stats.SkillErrors, width, 10)
		sb.WriteString("\n")
	}

	// ── HOOKS ──
	if len(stats.HookCounts) > 0 {
		totalHooks := 0
		for _, c := range stats.HookCounts {
			totalHooks += c
		}
		sb.WriteString(titleStyle.Render(fmt.Sprintf("HOOKS (%d)", totalHooks)) + "\n")
		sb.WriteString(ruler + "\n")
		renderToolBarN(&sb, stats.HookCounts, width, 10)
		sb.WriteString("\n")
	}

	// ── MODELS ──
	if len(stats.Models) > 0 {
		sb.WriteString(titleStyle.Render("MODELS") + "\n")
		sb.WriteString(ruler + "\n")
		for name, count := range stats.Models {
			// Shorten model name
			short := shortenModel(name)
			sb.WriteString(fmt.Sprintf("  %-20s %s\n", short,
				numStyle.Render(fmt.Sprintf("%d", count))))
		}
		sb.WriteString("\n")
	}

	// ── ERRORS ──
	if stats.ToolErrorCount > 0 {
		renderErrorBreakdown(&sb, stats.ToolErrors, stats.ToolCounts, stats.SkillErrors, stats.SkillCounts, stats.CommandErrors, stats.CommandCounts, stats.ToolErrorCount, width, ruler, titleStyle)
	}

	return sb.String()
}

// renderErrorBreakdown renders a dedicated error section showing tools/skills/commands sorted by error count.

func renderGlobalStats(stats session.GlobalStats, width int) string {
	if stats.SessionCount == 0 {
		return dimStyle.Render("(no sessions found)")
	}

	var sb strings.Builder
	titleStyle := statTitleStyle
	numStyle := statNumStyle
	labelStyle := dimStyle
	ruler := dimStyle.Render(strings.Repeat("─", min(width, 40)))

	// ── OVERVIEW ──
	sb.WriteString(titleStyle.Render("OVERVIEW") + "\n")
	sb.WriteString(ruler + "\n")
	sb.WriteString(fmt.Sprintf("  Sessions      %s\n", numStyle.Render(fmt.Sprintf("%d", stats.SessionCount))))
	sb.WriteString(fmt.Sprintf("  Messages      %s", numStyle.Render(fmt.Sprintf("%d", stats.TotalMessages))))
	sb.WriteString(labelStyle.Render(fmt.Sprintf(" (%d user, %d assistant)", stats.TotalUserMsgs, stats.TotalAsstMsgs)))
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("  Total Time    %s\n", numStyle.Render(fmtDuration(stats.TotalDuration))))
	sb.WriteString(fmt.Sprintf("  Avg Duration  %s\n", numStyle.Render(fmtDuration(stats.AvgDuration))))
	if stats.SessionCount > 0 {
		avgMsgs := stats.TotalMessages / stats.SessionCount
		sb.WriteString(fmt.Sprintf("  Avg Msgs/Sess %s\n", labelStyle.Render(fmt.Sprintf("%d", avgMsgs))))
	}
	if stats.TotalCompactions > 0 {
		warnStyle := errorStyle
		sb.WriteString(fmt.Sprintf("  Compactions   %s",
			warnStyle.Render(fmt.Sprintf("%d×", stats.TotalCompactions))))
		sb.WriteString(labelStyle.Render(fmt.Sprintf(" (%d/%d sessions)",
			stats.SessionsWithCompaction, stats.SessionCount)))
		sb.WriteString("\n")
	}
	if len(stats.AllTurnsPerRequest) > 0 {
		avg, maxT := turnsStats(stats.AllTurnsPerRequest)
		sb.WriteString(fmt.Sprintf("  Turns/Req     %s",
			numStyle.Render(fmt.Sprintf("%.1f avg", avg))))
		sb.WriteString(labelStyle.Render(fmt.Sprintf("  (max %d, %d reqs)", maxT, len(stats.AllTurnsPerRequest))))
		sb.WriteString("\n")
	}
	sb.WriteString("\n")

	// ── TOKENS ──
	sb.WriteString(titleStyle.Render("TOKENS") + "\n")
	sb.WriteString(ruler + "\n")

	totalInput := stats.TotalInputTokens + stats.TotalCacheReadTokens + stats.TotalCacheCreationTokens
	cacheRatio := float64(0)
	if totalInput > 0 {
		cacheRatio = float64(stats.TotalCacheReadTokens) * 100 / float64(totalInput)
	}

	inputStyle := statInputStyle
	outputStyle := statOutputStyle

	sb.WriteString(fmt.Sprintf("  Input       %s", inputStyle.Render(fmtNum(totalInput))))
	if cacheRatio > 0 {
		sb.WriteString(labelStyle.Render(fmt.Sprintf("  (cache hit %.0f%%)", cacheRatio)))
	}
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("  Output      %s\n", outputStyle.Render(fmtNum(stats.TotalOutputTokens))))
	sb.WriteString(fmt.Sprintf("  Cache Read  %s\n", labelStyle.Render(fmtNum(stats.TotalCacheReadTokens))))
	sb.WriteString(fmt.Sprintf("  Cache Write %s\n", labelStyle.Render(fmtNum(stats.TotalCacheCreationTokens))))
	if stats.TotalCostUSD > 0 {
		costStyle := statCostStyle
		sb.WriteString(fmt.Sprintf("  Cost        %s", costStyle.Render(fmtCost(stats.TotalCostUSD))))
		if stats.SessionCount > 0 {
			avgCost := stats.TotalCostUSD / float64(stats.SessionCount)
			sb.WriteString(labelStyle.Render(fmt.Sprintf("  (avg %s/sess)", fmtCost(avgCost))))
		}
		sb.WriteString("\n")
	}
	if stats.SessionCount > 0 {
		avgOut := stats.TotalOutputTokens / int64(stats.SessionCount)
		sb.WriteString(fmt.Sprintf("  Avg Out/Sess %s\n", labelStyle.Render(fmtNum(avgOut))))
	}
	sb.WriteString("\n")

	// ── EFFICIENCY ──
	hasEff := stats.TotalModelSwitches > 0 || stats.ToolCounts["Agent"] > 0
	if hasEff {
		sb.WriteString(titleStyle.Render("EFFICIENCY") + "\n")
		sb.WriteString(ruler + "\n")
		if stats.TotalModelSwitches > 0 {
			sb.WriteString(fmt.Sprintf("  Model Switches  %s",
				numStyle.Render(fmt.Sprintf("%d", stats.TotalModelSwitches))))
			sb.WriteString(labelStyle.Render(fmt.Sprintf("  (%d sessions)", stats.SessionsWithSwitches)))
			sb.WriteString("\n")
		}
		if agentCount := stats.ToolCounts["Agent"]; agentCount > 0 {
			sb.WriteString(fmt.Sprintf("  Agent Spawns    %s\n",
				numStyle.Render(fmt.Sprintf("%d", agentCount))))
		}
		sb.WriteString("\n")
	}

	// ── TOOLS ──
	if len(stats.ToolCounts) > 0 {
		totalCalls := 0
		for _, c := range stats.ToolCounts {
			totalCalls += c
		}
		header := fmt.Sprintf("TOOLS (%d calls)", totalCalls)
		if stats.TotalToolErrors > 0 {
			errRate := float64(stats.TotalToolErrors) * 100 / float64(max(stats.TotalToolResults, 1))
			eStyle := errorStyle
			header += eStyle.Render(fmt.Sprintf("  %d errors (%.1f%%)", stats.TotalToolErrors, errRate))
		}
		sb.WriteString(titleStyle.Render(header) + "\n")
		sb.WriteString(ruler + "\n")
		// Split: built-in tools vs MCP tools
		builtinCounts := make(map[string]int)
		for k, v := range stats.ToolCounts {
			if len(k) <= 5 || k[:5] != "mcp__" {
				builtinCounts[k] = v
			}
		}
		builtinErrors := make(map[string]int)
		for k, v := range stats.ToolErrors {
			if len(k) <= 5 || k[:5] != "mcp__" {
				builtinErrors[k] = v
			}
		}
		renderToolBarWithErrors(&sb, builtinCounts, builtinErrors, width, 15)
		sb.WriteString("\n")
	}

	// ── MCP TOOLS ──
	if len(stats.MCPToolCounts) > 0 {
		totalMCP := 0
		for _, c := range stats.MCPToolCounts {
			totalMCP += c
		}
		// Build MCP-only errors
		mcpErrors := make(map[string]int)
		totalMCPErrors := 0
		for k, v := range stats.ToolErrors {
			if len(k) > 5 && k[:5] == "mcp__" {
				mcpErrors[k] = v
				totalMCPErrors += v
			}
		}
		header := fmt.Sprintf("MCP TOOLS (%d calls)", totalMCP)
		if totalMCPErrors > 0 {
			errRate := float64(totalMCPErrors) * 100 / float64(max(totalMCP, 1))
			eStyle := errorStyle
			header += eStyle.Render(fmt.Sprintf("  %d errors (%.1f%%)", totalMCPErrors, errRate))
		}
		sb.WriteString(titleStyle.Render(header) + "\n")
		sb.WriteString(ruler + "\n")
		renderToolBarWithErrors(&sb, stats.MCPToolCounts, mcpErrors, width, 15)
		sb.WriteString("\n")
	}

	// ── TOOL TIMELINES (daily) ──
	if len(stats.AllToolCallTimestamps) > 0 {
		renderToolDailyTimelines(&sb, stats.AllToolCallTimestamps, stats.AllToolErrorTimestamps, stats.ToolCounts, width, 10)
	}

	// ── AGENTS ──
	if len(stats.AgentCounts) > 0 {
		totalAgents := 0
		for _, c := range stats.AgentCounts {
			totalAgents += c
		}
		sb.WriteString(titleStyle.Render(fmt.Sprintf("AGENTS (%d spawns)", totalAgents)) + "\n")
		sb.WriteString(ruler + "\n")
		renderToolBarN(&sb, stats.AgentCounts, width, 10)
		sb.WriteString("\n")
	}

	// ── SKILLS ──
	if len(stats.SkillCounts) > 0 {
		totalSkills := 0
		for _, c := range stats.SkillCounts {
			totalSkills += c
		}
		sb.WriteString(titleStyle.Render(fmt.Sprintf("SKILLS (%d)", totalSkills)) + "\n")
		sb.WriteString(ruler + "\n")
		renderToolBarWithErrors(&sb, stats.SkillCounts, stats.SkillErrors, width, 15)
		sb.WriteString("\n")
	}

	// ── COMMANDS ──
	if len(stats.CommandCounts) > 0 {
		totalCmds := 0
		for _, c := range stats.CommandCounts {
			totalCmds += c
		}
		sb.WriteString(titleStyle.Render(fmt.Sprintf("COMMANDS (%d)", totalCmds)) + "\n")
		sb.WriteString(ruler + "\n")
		renderToolBarWithErrors(&sb, stats.CommandCounts, stats.CommandErrors, width, 15)
		sb.WriteString("\n")
	}

	// ── HOOKS ──
	if len(stats.HookCounts) > 0 {
		totalHooks := 0
		for _, c := range stats.HookCounts {
			totalHooks += c
		}
		sb.WriteString(titleStyle.Render(fmt.Sprintf("HOOKS (%d)", totalHooks)) + "\n")
		sb.WriteString(ruler + "\n")
		renderToolBarN(&sb, stats.HookCounts, width, 15)
		sb.WriteString("\n")
	}

	// ── MODELS ──
	if len(stats.Models) > 0 {
		sb.WriteString(titleStyle.Render("MODELS") + "\n")
		sb.WriteString(ruler + "\n")
		shortModels := make(map[string]int, len(stats.Models))
		for name, count := range stats.Models {
			shortModels[shortenModel(name)] += count
		}
		renderToolBarN(&sb, shortModels, width, 10)
		sb.WriteString("\n")
	}

	// ── CODE ──
	if stats.TotalWrites > 0 || stats.TotalEdits > 0 {
		sb.WriteString(titleStyle.Render("CODE") + "\n")
		sb.WriteString(ruler + "\n")
		sb.WriteString(fmt.Sprintf("  Write %s  Edit %s  Files %s\n",
			numStyle.Render(fmt.Sprintf("%d", stats.TotalWrites)),
			numStyle.Render(fmt.Sprintf("%d", stats.TotalEdits)),
			numStyle.Render(fmt.Sprintf("%d", stats.TotalFiles)),
		))
		sb.WriteString("\n")
	}

	// ── ERRORS ──
	if stats.TotalToolErrors > 0 {
		renderErrorBreakdown(&sb, stats.ToolErrors, stats.ToolCounts, stats.SkillErrors, stats.SkillCounts, stats.CommandErrors, stats.CommandCounts, stats.TotalToolErrors, width, ruler, titleStyle)
	}

	// ── SESSION DURATIONS (sparkline) ──
	if len(stats.SessionDurations) > 1 {
		sb.WriteString(titleStyle.Render("SESSION DURATIONS") + "\n")
		sb.WriteString(ruler + "\n")
		durVals := make([]int, len(stats.SessionDurations))
		for i, d := range stats.SessionDurations {
			durVals[i] = int(d.Seconds())
		}
		sparkW := min(width-4, 60)
		if sparkW > 5 {
			spark := sparkline(durVals, sparkW)
			sb.WriteString(fmt.Sprintf("  %s\n", outputStyle.Render(spark)))
		}
		sb.WriteString("\n")
	}

	// ── SESSION TOKENS (sparkline) ──
	if len(stats.SessionTokens) > 1 {
		sb.WriteString(titleStyle.Render("SESSION OUTPUT TOKENS") + "\n")
		sb.WriteString(ruler + "\n")
		tokVals := make([]int, len(stats.SessionTokens))
		for i, t := range stats.SessionTokens {
			tokVals[i] = int(t)
		}
		sparkW := min(width-4, 60)
		if sparkW > 5 {
			spark := sparkline(tokVals, sparkW)
			sb.WriteString(fmt.Sprintf("  %s\n", outputStyle.Render(spark)))
		}
		sb.WriteString("\n")
	}

	// ── DAILY ACTIVITY ──
	if len(stats.SessionStarts) > 1 {
		sparkW := min(width-4, 60)
		if sparkW > 5 {
			buckets, firstDay, lastDay := dailyBuckets(stats.SessionStarts, sparkW)
			if len(buckets) > 1 {
				sb.WriteString(titleStyle.Render("DAILY ACTIVITY") + "\n")
				sb.WriteString(ruler + "\n")
				spark := sparkline(buckets, sparkW)
				userStyle := statInputStyle
				sb.WriteString(fmt.Sprintf("  Sessions  %s\n", userStyle.Render(spark)))
				// Daily message timeline
				if len(stats.AllMsgTimestamps) > 0 {
					msgBuckets, _, _ := dailyBuckets(stats.AllMsgTimestamps, sparkW)
					for len(msgBuckets) < len(buckets) {
						msgBuckets = append(msgBuckets, 0)
					}
					if hasNonZero(msgBuckets) {
						msgSpark := sparkline(msgBuckets, sparkW)
						msgStyle := statAccentStyle
						sb.WriteString(fmt.Sprintf("  Messages  %s\n", msgStyle.Render(msgSpark)))
					}
				}
				// Daily error timeline (same date scale, red)
				if len(stats.AllErrorTimestamps) > 0 {
					errBuckets, _, _ := dailyBuckets(stats.AllErrorTimestamps, sparkW)
					// Pad to same length as session buckets
					for len(errBuckets) < len(buckets) {
						errBuckets = append(errBuckets, 0)
					}
					if hasNonZero(errBuckets) {
						errSpark := sparkline(errBuckets, sparkW)
						errStyle := errorStyle
						sb.WriteString(fmt.Sprintf("  Errors    %s\n", errStyle.Render(errSpark)))
					}
				}
				sb.WriteString(fmt.Sprintf("  %s%s%s\n",
					labelStyle.Render(firstDay),
					labelStyle.Render(strings.Repeat(" ", max(sparkW-len(firstDay)-len(lastDay), 0))),
					labelStyle.Render(lastDay)))
			}
		}
	}

	return sb.String()
}

// renderToolTimelines renders per-tool activity and error sparklines for a session.
