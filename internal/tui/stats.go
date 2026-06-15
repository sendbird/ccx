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
	ruler := dimStyle.Render(strings.Repeat("─", min(width, 52)))

	dur := stats.LastTimestamp.Sub(stats.FirstTimestamp)
	totalInput := stats.TotalInputTokens + stats.TotalCacheReadTokens + stats.TotalCacheCreationTokens
	cost := session.EstimateCost(stats.ModelTokens)
	cacheRatio := float64(0)
	if totalInput > 0 {
		cacheRatio = float64(stats.TotalCacheReadTokens) * 100 / float64(totalInput)
	}

	sb.WriteString(titleStyle.Render(sectionTitle(iconFolderOpen, "SESSION OVERVIEW")) + "\n")
	sb.WriteString(ruler + "\n")
	sb.WriteString(fmt.Sprintf("  %s  %s msgs  %s  %s tokens\n",
		statInputStyle.Render(roleChip("user"))+labelStyle.Render(fmt.Sprintf(" %d", stats.UserMsgCount)),
		statOutputStyle.Render(fmt.Sprintf("%d", stats.MessageCount)),
		numStyle.Render(fmtDuration(dur)),
		statAccentStyle.Render(fmtNum(stats.TotalOutputTokens))))
	sb.WriteString(fmt.Sprintf("  %s  %s msgs  %s  %s cost\n",
		assistantLabelStyle.Render(roleChip("assistant"))+labelStyle.Render(fmt.Sprintf(" %d", stats.AsstMsgCount)),
		labelStyle.Render(fmt.Sprintf("%d asst", stats.AsstMsgCount)),
		labelStyle.Render(stats.FirstTimestamp.Format("15:04")+" → "+stats.LastTimestamp.Format("15:04")),
		statCostStyle.Render(fmtCost(cost))))
	if stats.CompactionCount > 0 {
		sb.WriteString(fmt.Sprintf("  %s  compacted %d×\n", errorStyle.Render(iconBadgeStuck), stats.CompactionCount))
	}
	if stats.ModelSwitches > 0 || stats.ToolCounts["Agent"] > 0 {
		sb.WriteString(fmt.Sprintf("  %s  model switches %d  %s  agents %d\n",
			busyBadge.Render(iconBadgeBusy), stats.ModelSwitches,
			agentBadgeStyle.Render(iconAgent), stats.ToolCounts["Agent"]))
	}
	sb.WriteString("\n")

	sb.WriteString(titleStyle.Render(sectionTitle(iconBadgeMon, "ACTIVITY")) + "\n")
	sb.WriteString(ruler + "\n")
	if len(stats.MsgTimestamps) > 2 && dur > 0 {
		sparkW := min(width-18, 42)
		if sparkW > 8 {
			msgBuckets := timelineBuckets(stats.MsgTimestamps, stats.FirstTimestamp, stats.LastTimestamp, sparkW)
			errBuckets := timelineBuckets(stats.ErrorTimestamps, stats.FirstTimestamp, stats.LastTimestamp, sparkW)
			sb.WriteString(fmt.Sprintf("  Msgs    %s\n", statInputStyle.Render(sparkline(msgBuckets, sparkW))))
			if hasNonZero(errBuckets) {
				sb.WriteString(fmt.Sprintf("  Errors  %s\n", errorStyle.Render(sparkline(errBuckets, sparkW))))
			}
			rate := float64(stats.MessageCount) / max(dur.Minutes(), 1)
			sb.WriteString(fmt.Sprintf("  Rate    %s  %s%s%s\n",
				labelStyle.Render(fmt.Sprintf("%.1f msg/min", rate)),
				labelStyle.Render(stats.FirstTimestamp.Format("15:04")),
				labelStyle.Render(strings.Repeat(" ", max(sparkW-10, 0))),
				labelStyle.Render(stats.LastTimestamp.Format("15:04"))))
		}
	}
	if len(stats.TurnsPerRequest) > 0 {
		avg, maxT := turnsStats(stats.TurnsPerRequest)
		sparkW := min(width-18, 30)
		sb.WriteString(fmt.Sprintf("  Turns   %s  max %d\n", numStyle.Render(fmt.Sprintf("%.1f avg", avg)), maxT))
		if sparkW > 8 {
			sb.WriteString(fmt.Sprintf("  Flow    %s\n", dimStyle.Render(sparkline(stats.TurnsPerRequest, sparkW))))
		}
	}
	sb.WriteString("\n")

	sb.WriteString(titleStyle.Render(sectionTitle(iconTask, "TOKENS")) + "\n")
	sb.WriteString(ruler + "\n")
	inputBarW := min(width-22, 34)
	if inputBarW < 8 {
		inputBarW = 8
	}
	maxToken := int(max(max(totalInput, stats.TotalOutputTokens), 1))
	sb.WriteString(fmt.Sprintf("  In      %s %s\n",
		statInputStyle.Render(histogramBar(int(totalInput), maxToken, inputBarW)),
		statInputStyle.Render(fmtNum(totalInput))))
	sb.WriteString(fmt.Sprintf("  Out     %s %s\n",
		statOutputStyle.Render(histogramBar(int(stats.TotalOutputTokens), maxToken, inputBarW)),
		statOutputStyle.Render(fmtNum(stats.TotalOutputTokens))))
	sb.WriteString(fmt.Sprintf("  Cache   %s hit %.0f%%  write %s\n",
		labelStyle.Render(fmtNum(stats.TotalCacheReadTokens)), cacheRatio,
		labelStyle.Render(fmtNum(stats.TotalCacheCreationTokens))))
	if len(stats.OutputTokenSeries) > 1 {
		sb.WriteString(fmt.Sprintf("  Trend   %s\n", statOutputStyle.Render(sparkline(stats.OutputTokenSeries, min(width-18, 40)))))
	}
	if stats.AsstMsgCount > 0 {
		sb.WriteString(fmt.Sprintf("  AvgOut  %s / turn\n", labelStyle.Render(fmtNum(stats.TotalOutputTokens/int64(stats.AsstMsgCount)))))
	}
	sb.WriteString("\n")

	if len(stats.ToolCounts) > 0 {
		totalCalls := 0
		for _, c := range stats.ToolCounts {
			totalCalls += c
		}
		header := fmt.Sprintf(sectionTitle(iconTask, "TOOLS")+" %s", labelStyle.Render(fmt.Sprintf("(%d calls)", totalCalls)))
		if stats.ToolErrorCount > 0 {
			header += "  " + errorStyle.Render(fmt.Sprintf("%d err", stats.ToolErrorCount))
		}
		sb.WriteString(titleStyle.Render(header) + "\n")
		sb.WriteString(ruler + "\n")
		builtinCounts := make(map[string]int)
		builtinErrors := make(map[string]int)
		for k, v := range stats.ToolCounts {
			if len(k) <= 5 || k[:5] != "mcp__" {
				builtinCounts[k] = v
			}
		}
		for k, v := range stats.ToolErrors {
			if len(k) <= 5 || k[:5] != "mcp__" {
				builtinErrors[k] = v
			}
		}
		renderToolBarWithErrors(&sb, builtinCounts, builtinErrors, width, 8)
		sb.WriteString("\n")
	}

	if len(stats.MCPToolCounts) > 0 {
		totalMCP := 0
		for _, c := range stats.MCPToolCounts {
			totalMCP += c
		}
		mcpErrors := make(map[string]int)
		for k, v := range stats.ToolErrors {
			if len(k) > 5 && k[:5] == "mcp__" {
				mcpErrors[k] = v
			}
		}
		sb.WriteString(titleStyle.Render(fmt.Sprintf(sectionTitle(iconAgent, "MCP")+" %s", labelStyle.Render(fmt.Sprintf("(%d calls)", totalMCP)))) + "\n")
		sb.WriteString(ruler + "\n")
		renderToolBarWithErrors(&sb, stats.MCPToolCounts, mcpErrors, width, 6)
		sb.WriteString("\n")
	}

	if stats.WriteCount > 0 || stats.EditCount > 0 || len(stats.Models) > 0 {
		sb.WriteString(titleStyle.Render(sectionTitle(iconRoleCompact, "CODE & MODELS")) + "\n")
		sb.WriteString(ruler + "\n")
		sb.WriteString(fmt.Sprintf("  %s %d  %s %d  %s %d\n",
			taskBadgeStyle.Render(iconTask), stats.WriteCount,
			busyBadge.Render(iconActive), stats.EditCount,
			dimStyle.Render(iconFolder), len(stats.FilesTouched)))
		if len(stats.Models) > 0 {
			shortModels := make(map[string]int, len(stats.Models))
			for name, count := range stats.Models {
				shortModels[shortenModel(name)] += count
			}
			renderToolBarN(&sb, shortModels, width, 6)
		}
		sb.WriteString("\n")
	}

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
	ruler := dimStyle.Render(strings.Repeat("─", min(width, 52)))

	totalInput := stats.TotalInputTokens + stats.TotalCacheReadTokens + stats.TotalCacheCreationTokens
	cacheRatio := float64(0)
	if totalInput > 0 {
		cacheRatio = float64(stats.TotalCacheReadTokens) * 100 / float64(totalInput)
	}

	sb.WriteString(titleStyle.Render(sectionTitle(iconFolderOpen, "GLOBAL OVERVIEW")) + "\n")
	sb.WriteString(ruler + "\n")
	sb.WriteString(fmt.Sprintf("  %s  %s sessions  %s duration  %s cost\n",
		liveBadge.Render(iconBadgeLive)+labelStyle.Render(fmt.Sprintf(" %d", stats.SessionCount)),
		numStyle.Render(fmt.Sprintf("%d", stats.TotalMessages)),
		numStyle.Render(fmtDuration(stats.TotalDuration)),
		statCostStyle.Render(fmtCost(stats.TotalCostUSD))))
	sb.WriteString(fmt.Sprintf("  %s  %d user  %s  %d asst  avg %s msgs/sess\n",
		userLabelStyle.Render(roleChip("user")), stats.TotalUserMsgs,
		assistantLabelStyle.Render(roleChip("assistant")), stats.TotalAsstMsgs,
		labelStyle.Render(fmt.Sprintf("%d", max(stats.TotalMessages/max(stats.SessionCount, 1), 0)))))
	if stats.TotalCompactions > 0 {
		sb.WriteString(fmt.Sprintf("  %s  compacted %d× across %d sessions\n",
			errorStyle.Render(iconBadgeStuck), stats.TotalCompactions, stats.SessionsWithCompaction))
	}
	if stats.TotalModelSwitches > 0 || stats.ToolCounts["Agent"] > 0 {
		sb.WriteString(fmt.Sprintf("  %s  switches %d  %s  agents %d\n",
			busyBadge.Render(iconBadgeBusy), stats.TotalModelSwitches,
			agentBadgeStyle.Render(iconAgent), stats.ToolCounts["Agent"]))
	}
	sb.WriteString("\n")

	sb.WriteString(titleStyle.Render(sectionTitle(iconTask, "TOKEN MIX")) + "\n")
	sb.WriteString(ruler + "\n")
	barW := min(width-22, 36)
	if barW < 8 {
		barW = 8
	}
	maxToken := int(max(max(totalInput, stats.TotalOutputTokens), 1))
	sb.WriteString(fmt.Sprintf("  In      %s %s\n",
		statInputStyle.Render(histogramBar(int(totalInput), maxToken, barW)),
		statInputStyle.Render(fmtNum(totalInput))))
	sb.WriteString(fmt.Sprintf("  Out     %s %s\n",
		statOutputStyle.Render(histogramBar(int(stats.TotalOutputTokens), maxToken, barW)),
		statOutputStyle.Render(fmtNum(stats.TotalOutputTokens))))
	sb.WriteString(fmt.Sprintf("  Cache   %s hit %.0f%%  write %s\n",
		labelStyle.Render(fmtNum(stats.TotalCacheReadTokens)), cacheRatio,
		labelStyle.Render(fmtNum(stats.TotalCacheCreationTokens))))
	if stats.SessionCount > 0 {
		sb.WriteString(fmt.Sprintf("  AvgOut  %s / sess\n", labelStyle.Render(fmtNum(stats.TotalOutputTokens/int64(stats.SessionCount)))))
	}
	sb.WriteString("\n")

	if len(stats.SessionStarts) > 1 {
		sparkW := min(width-14, 42)
		if sparkW > 8 {
			buckets, firstDay, lastDay := dailyBuckets(stats.SessionStarts, sparkW)
			if len(buckets) > 1 {
				sb.WriteString(titleStyle.Render(sectionTitle(iconBadgeMon, "DAILY ACTIVITY")) + "\n")
				sb.WriteString(ruler + "\n")
				sb.WriteString(fmt.Sprintf("  Sess    %s\n", statInputStyle.Render(sparkline(buckets, sparkW))))
				if len(stats.AllMsgTimestamps) > 0 {
					msgBuckets, _, _ := dailyBuckets(stats.AllMsgTimestamps, sparkW)
					if hasNonZero(msgBuckets) {
						sb.WriteString(fmt.Sprintf("  Msgs    %s\n", statAccentStyle.Render(sparkline(msgBuckets, sparkW))))
					}
				}
				if len(stats.AllErrorTimestamps) > 0 {
					errBuckets, _, _ := dailyBuckets(stats.AllErrorTimestamps, sparkW)
					if hasNonZero(errBuckets) {
						sb.WriteString(fmt.Sprintf("  Errs    %s\n", errorStyle.Render(sparkline(errBuckets, sparkW))))
					}
				}
				sb.WriteString(fmt.Sprintf("  %s%s%s\n", labelStyle.Render(firstDay), labelStyle.Render(strings.Repeat(" ", max(sparkW-len(firstDay)-len(lastDay), 0))), labelStyle.Render(lastDay)))
				sb.WriteString("\n")
			}
		}
	}

	if len(stats.ToolCounts) > 0 {
		totalCalls := 0
		for _, c := range stats.ToolCounts {
			totalCalls += c
		}
		header := fmt.Sprintf(sectionTitle(iconTask, "TOOLS")+" %s", labelStyle.Render(fmt.Sprintf("(%d calls)", totalCalls)))
		if stats.TotalToolErrors > 0 {
			header += "  " + errorStyle.Render(fmt.Sprintf("%d err", stats.TotalToolErrors))
		}
		sb.WriteString(titleStyle.Render(header) + "\n")
		sb.WriteString(ruler + "\n")
		builtinCounts := make(map[string]int)
		builtinErrors := make(map[string]int)
		for k, v := range stats.ToolCounts {
			if len(k) <= 5 || k[:5] != "mcp__" {
				builtinCounts[k] = v
			}
		}
		for k, v := range stats.ToolErrors {
			if len(k) <= 5 || k[:5] != "mcp__" {
				builtinErrors[k] = v
			}
		}
		renderToolBarWithErrors(&sb, builtinCounts, builtinErrors, width, 10)
		sb.WriteString("\n")
	}

	if len(stats.MCPToolCounts) > 0 {
		mcpErrors := make(map[string]int)
		totalMCP := 0
		totalMCPErrors := 0
		for _, c := range stats.MCPToolCounts {
			totalMCP += c
		}
		for k, v := range stats.ToolErrors {
			if len(k) > 5 && k[:5] == "mcp__" {
				mcpErrors[k] = v
				totalMCPErrors += v
			}
		}
		header := fmt.Sprintf(sectionTitle(iconAgent, "MCP")+" %s", labelStyle.Render(fmt.Sprintf("(%d calls)", totalMCP)))
		if totalMCPErrors > 0 {
			header += "  " + errorStyle.Render(fmt.Sprintf("%d err", totalMCPErrors))
		}
		sb.WriteString(titleStyle.Render(header) + "\n")
		sb.WriteString(ruler + "\n")
		renderToolBarWithErrors(&sb, stats.MCPToolCounts, mcpErrors, width, 8)
		sb.WriteString("\n")
	}

	if len(stats.AgentCounts) > 0 {
		totalAgents := 0
		for _, c := range stats.AgentCounts {
			totalAgents += c
		}
		sb.WriteString(titleStyle.Render(fmt.Sprintf(sectionTitle(iconAgent, "AGENTS")+" %s", labelStyle.Render(fmt.Sprintf("(%d spawns)", totalAgents)))) + "\n")
		sb.WriteString(ruler + "\n")
		renderToolBarN(&sb, stats.AgentCounts, width, 8)
		sb.WriteString("\n")
	}

	if len(stats.SkillCounts) > 0 {
		totalSkills := 0
		for _, c := range stats.SkillCounts {
			totalSkills += c
		}
		sb.WriteString(titleStyle.Render(fmt.Sprintf(sectionTitle(iconRoleCompact, "SKILLS")+" %s", labelStyle.Render(fmt.Sprintf("(%d)", totalSkills)))) + "\n")
		sb.WriteString(ruler + "\n")
		renderToolBarWithErrors(&sb, stats.SkillCounts, stats.SkillErrors, width, 8)
		sb.WriteString("\n")
	}

	if len(stats.CommandCounts) > 0 {
		totalCmds := 0
		for _, c := range stats.CommandCounts {
			totalCmds += c
		}
		sb.WriteString(titleStyle.Render(fmt.Sprintf(sectionTitle(iconHook, "COMMANDS")+" %s", labelStyle.Render(fmt.Sprintf("(%d)", totalCmds)))) + "\n")
		sb.WriteString(ruler + "\n")
		renderToolBarWithErrors(&sb, stats.CommandCounts, stats.CommandErrors, width, 8)
		sb.WriteString("\n")
	}

	if len(stats.HookCounts) > 0 {
		totalHooks := 0
		for _, c := range stats.HookCounts {
			totalHooks += c
		}
		sb.WriteString(titleStyle.Render(fmt.Sprintf(sectionTitle(iconHook, "HOOKS")+" %s", labelStyle.Render(fmt.Sprintf("(%d)", totalHooks)))) + "\n")
		sb.WriteString(ruler + "\n")
		renderToolBarN(&sb, stats.HookCounts, width, 8)
		sb.WriteString("\n")
	}

	if len(stats.Models) > 0 || stats.TotalWrites > 0 || stats.TotalEdits > 0 {
		sb.WriteString(titleStyle.Render(sectionTitle(iconRoleCompact, "MODELS & CODE")) + "\n")
		sb.WriteString(ruler + "\n")
		if len(stats.Models) > 0 {
			shortModels := make(map[string]int, len(stats.Models))
			for name, count := range stats.Models {
				shortModels[shortenModel(name)] += count
			}
			renderToolBarN(&sb, shortModels, width, 6)
		}
		sb.WriteString(fmt.Sprintf("  %s %d  %s %d  %s %d\n",
			taskBadgeStyle.Render(iconTask), stats.TotalWrites,
			busyBadge.Render(iconActive), stats.TotalEdits,
			dimStyle.Render(iconFolder), stats.TotalFiles))
		sb.WriteString("\n")
	}

	if len(stats.ProjectStats) > 0 {
		sb.WriteString(titleStyle.Render(sectionTitle(iconFolder, "TOP PROJECTS")) + "\n")
		sb.WriteString(ruler + "\n")
		renderProjectStats(&sb, stats.ProjectStats, width)
		sb.WriteString("\n")
	}

	if stats.TotalToolErrors > 0 {
		renderErrorBreakdown(&sb, stats.ToolErrors, stats.ToolCounts, stats.SkillErrors, stats.SkillCounts, stats.CommandErrors, stats.CommandCounts, stats.TotalToolErrors, width, ruler, titleStyle)
	}

	return sb.String()
}

// renderToolTimelines renders per-tool activity and error sparklines for a session.
