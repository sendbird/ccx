package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
)

var sparkChars = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

func renderSessionStats(stats session.SessionStats, width int) string {
	if stats.MessageCount == 0 {
		return dimStyle.Render("(no data)")
	}

	var sb strings.Builder
	titleStyle := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	numStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
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
		sb.WriteString(fmt.Sprintf("  Rate      %s\n",
			labelStyle.Render(fmt.Sprintf("%.1f msg/min", rate))))
	}
	if stats.CompactionCount > 0 {
		warnStyle := lipgloss.NewStyle().Foreground(colorError)
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
			userStyle := lipgloss.NewStyle().Foreground(colorUser)
			sb.WriteString(fmt.Sprintf("  Activity  %s\n", userStyle.Render(spark)))
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

	inputStyle := lipgloss.NewStyle().Foreground(colorUser)
	outputStyle := lipgloss.NewStyle().Foreground(colorAssistant)

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
		costStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214")) // orange
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
			eStyle := lipgloss.NewStyle().Foreground(colorError)
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
			eStyle := lipgloss.NewStyle().Foreground(colorError)
			header += eStyle.Render(fmt.Sprintf("  %d errors (%.1f%%)", totalMCPErrors, errRate))
		}
		sb.WriteString(titleStyle.Render(header) + "\n")
		sb.WriteString(ruler + "\n")
		renderToolBarWithErrors(&sb, stats.MCPToolCounts, mcpErrors, width, 10)
		sb.WriteString("\n")
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
	}

	return sb.String()
}

// renderToolBar renders a sorted bar chart of tool name -> count.
// If errors is provided, error counts are shown as a red portion of the bar.
func renderToolBar(sb *strings.Builder, counts map[string]int, width int) {
	renderToolBarWithErrors(sb, counts, nil, width, 10)
}

func renderToolBarWithErrors(sb *strings.Builder, counts map[string]int, errors map[string]int, width int, limit int) {
	type toolEntry struct {
		name   string
		count  int
		errors int
	}
	var entries []toolEntry
	maxCount := 0
	maxNameW := 0
	for name, count := range counts {
		short := shortenToolName(name)
		e := toolEntry{name: short, count: count}
		if errors != nil {
			e.errors = min(errors[name], count) // cap errors to count
		}
		entries = append(entries, e)
		if count > maxCount {
			maxCount = count
		}
		if len(short) > maxNameW {
			maxNameW = len(short)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].count > entries[j].count
	})

	if len(entries) > limit {
		entries = entries[:limit]
	}

	// Allow label column up to 40% of width, minimum 14
	maxLabelW := max(width*2/5, 14)
	if maxNameW > maxLabelW {
		maxNameW = maxLabelW
	}
	countW := len(fmt.Sprintf("%d", maxCount))
	barMaxW := width - maxNameW - countW - 6 // "  name  ██  N"
	if barMaxW < 3 {
		barMaxW = 3
	}

	barStyle := lipgloss.NewStyle().Foreground(colorAccent)
	errBarStyle := lipgloss.NewStyle().Foreground(colorError)

	for _, e := range entries {
		name := e.name
		if len(name) > maxNameW {
			name = name[:maxNameW-1] + "…"
		}
		barLen := e.count * barMaxW / maxCount
		if barLen < 1 && e.count > 0 {
			barLen = 1
		}

		var bar string
		if e.errors > 0 && e.count > 0 {
			errBarLen := e.errors * barLen / e.count
			if errBarLen < 1 {
				errBarLen = 1
			}
			if errBarLen > barLen {
				errBarLen = barLen
			}
			okLen := barLen - errBarLen
			if okLen > 0 {
				bar = barStyle.Render(strings.Repeat("█", okLen))
			}
			bar += errBarStyle.Render(strings.Repeat("█", errBarLen))
		} else {
			bar = barStyle.Render(strings.Repeat("█", barLen))
		}

		countLabel := fmt.Sprintf("%*d", countW, e.count)
		if e.errors > 0 {
			countLabel += errBarStyle.Render(fmt.Sprintf(" (%d err)", e.errors))
		}
		sb.WriteString(fmt.Sprintf("  %-*s %s %s\n", maxNameW, name, bar, countLabel))
	}
}

func sparkline(values []int, maxWidth int) string {
	if len(values) == 0 {
		return ""
	}
	// Downsample if too many points
	if len(values) > maxWidth {
		bucketSize := (len(values) + maxWidth - 1) / maxWidth
		var ds []int
		for i := 0; i < len(values); i += bucketSize {
			sum, count := 0, 0
			for j := i; j < min(i+bucketSize, len(values)); j++ {
				sum += values[j]
				count++
			}
			ds = append(ds, sum/max(count, 1))
		}
		values = ds
	}
	// Find max
	maxVal := 0
	for _, v := range values {
		if v > maxVal {
			maxVal = v
		}
	}
	if maxVal == 0 {
		return strings.Repeat(string(sparkChars[0]), len(values))
	}
	var sb strings.Builder
	for _, v := range values {
		idx := v * (len(sparkChars) - 1) / maxVal
		sb.WriteRune(sparkChars[idx])
	}
	return sb.String()
}

func turnsStats(turns []int) (avg float64, maxT int) {
	if len(turns) == 0 {
		return 0, 0
	}
	sum := 0
	for _, t := range turns {
		sum += t
		if t > maxT {
			maxT = t
		}
	}
	avg = float64(sum) / float64(len(turns))
	return
}

// timelineBuckets distributes timestamps into N buckets over a time range.
func timelineBuckets(timestamps []time.Time, start, end time.Time, n int) []int {
	dur := end.Sub(start)
	if dur <= 0 || n <= 0 {
		return nil
	}
	buckets := make([]int, n)
	bucketDur := dur / time.Duration(n)
	for _, ts := range timestamps {
		idx := int(ts.Sub(start) / bucketDur)
		if idx < 0 {
			idx = 0
		}
		if idx >= n {
			idx = n - 1
		}
		buckets[idx]++
	}
	return buckets
}

// dailyBuckets distributes timestamps into per-day counts, returning buckets and day labels.
func dailyBuckets(timestamps []time.Time, n int) ([]int, string, string) {
	if len(timestamps) == 0 {
		return nil, "", ""
	}
	sort.Slice(timestamps, func(i, j int) bool { return timestamps[i].Before(timestamps[j]) })
	first := timestamps[0].Truncate(24 * time.Hour)
	last := timestamps[len(timestamps)-1].Truncate(24 * time.Hour)
	days := int(last.Sub(first)/(24*time.Hour)) + 1
	if days < 2 {
		return nil, "", ""
	}
	buckets := make([]int, days)
	for _, ts := range timestamps {
		idx := int(ts.Sub(first) / (24 * time.Hour))
		if idx >= days {
			idx = days - 1
		}
		buckets[idx]++
	}
	// Downsample if too many days
	if days > n && n > 0 {
		bucketSize := (days + n - 1) / n
		var ds []int
		for i := 0; i < days; i += bucketSize {
			sum := 0
			for j := i; j < min(i+bucketSize, days); j++ {
				sum += buckets[j]
			}
			ds = append(ds, sum)
		}
		buckets = ds
	}
	return buckets, first.Format("Jan 2"), last.Format("Jan 2")
}

func fmtCost(usd float64) string {
	if usd < 0.01 {
		return fmt.Sprintf("$%.4f", usd)
	}
	if usd < 1.0 {
		return fmt.Sprintf("$%.3f", usd)
	}
	return fmt.Sprintf("$%.2f", usd)
}

func fmtNum(n int64) string {
	if n < 0 {
		return "-" + fmtNum(-n)
	}
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	return strings.Join(parts, ",")
}

func fmtDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh %dm", h, m)
}

func shortenToolName(name string) string {
	// Strip mcp__ prefix for display:
	// mcp__claude_ai_Slack__slack_search_channels → Slack/slack_search_channels
	// mcp__grafana__query_prometheus → grafana/query_prometheus
	if len(name) > 6 && name[:5] == "mcp__" {
		rest := name[5:]
		parts := strings.SplitN(rest, "__", 2)
		if len(parts) == 2 {
			server := parts[0]
			// Strip "claude_ai_" prefix from connector names
			server = strings.TrimPrefix(server, "claude_ai_")
			return server + "/" + parts[1]
		}
		return rest
	}
	return name
}

func shortenModel(name string) string {
	// "claude-opus-4-5-20251101" → "opus-4.5"
	// "claude-sonnet-4-5-20251101" → "sonnet-4.5"
	name = strings.TrimPrefix(name, "claude-")
	// Remove date suffix
	if idx := strings.LastIndex(name, "-20"); idx > 0 {
		name = name[:idx]
	}
	return name
}

func renderGlobalStats(stats session.GlobalStats, width int) string {
	if stats.SessionCount == 0 {
		return dimStyle.Render("(no sessions found)")
	}

	var sb strings.Builder
	titleStyle := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	numStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
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
		warnStyle := lipgloss.NewStyle().Foreground(colorError)
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

	inputStyle := lipgloss.NewStyle().Foreground(colorUser)
	outputStyle := lipgloss.NewStyle().Foreground(colorAssistant)

	sb.WriteString(fmt.Sprintf("  Input       %s", inputStyle.Render(fmtNum(totalInput))))
	if cacheRatio > 0 {
		sb.WriteString(labelStyle.Render(fmt.Sprintf("  (cache hit %.0f%%)", cacheRatio)))
	}
	sb.WriteString("\n")
	sb.WriteString(fmt.Sprintf("  Output      %s\n", outputStyle.Render(fmtNum(stats.TotalOutputTokens))))
	sb.WriteString(fmt.Sprintf("  Cache Read  %s\n", labelStyle.Render(fmtNum(stats.TotalCacheReadTokens))))
	sb.WriteString(fmt.Sprintf("  Cache Write %s\n", labelStyle.Render(fmtNum(stats.TotalCacheCreationTokens))))
	if stats.TotalCostUSD > 0 {
		costStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
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
			eStyle := lipgloss.NewStyle().Foreground(colorError)
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
			eStyle := lipgloss.NewStyle().Foreground(colorError)
			header += eStyle.Render(fmt.Sprintf("  %d errors (%.1f%%)", totalMCPErrors, errRate))
		}
		sb.WriteString(titleStyle.Render(header) + "\n")
		sb.WriteString(ruler + "\n")
		renderToolBarWithErrors(&sb, stats.MCPToolCounts, mcpErrors, width, 15)
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
				userStyle := lipgloss.NewStyle().Foreground(colorUser)
				sb.WriteString(fmt.Sprintf("  %s\n", userStyle.Render(spark)))
				sb.WriteString(fmt.Sprintf("  %s%s%s\n",
					labelStyle.Render(firstDay),
					labelStyle.Render(strings.Repeat(" ", max(sparkW-len(firstDay)-len(lastDay), 0))),
					labelStyle.Render(lastDay)))
			}
		}
	}

	return sb.String()
}

// renderToolBarN renders a sorted bar chart, limited to top N entries.
func renderToolBarN(sb *strings.Builder, counts map[string]int, width int, limit int) {
	renderToolBarWithErrors(sb, counts, nil, width, limit)
}
