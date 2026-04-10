package tui

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

// statsDetailMode represents which drill-down detail view is active.
type statsDetailMode int

const (
	statsDetailNone     statsDetailMode = iota
	statsDetailTools                    // built-in tools
	statsDetailMCP                      // MCP tools
	statsDetailAgents                   // agent types
	statsDetailSkills                   // skills
	statsDetailCommands                 // commands
	statsDetailErrors                   // error breakdown
	statsDetailHooks                    // hooks
	statsDetailRepos                    // per-repo breakdown
	statsDetailProjects                 // per-project breakdown
)

const statsDetailLast = statsDetailProjects

func (m statsDetailMode) next() statsDetailMode {
	n := m + 1
	if n > statsDetailLast {
		n = statsDetailTools
	}
	return n
}

func (m statsDetailMode) prev() statsDetailMode {
	n := m - 1
	if n <= statsDetailNone {
		n = statsDetailLast
	}
	return n
}

// renderStatsDetail renders the drill-down detail view for a given category.
func renderStatsDetail(mode statsDetailMode, stats session.GlobalStats, width int) string {
	switch mode {
	case statsDetailTools:
		return renderToolDetail(stats, width, false)
	case statsDetailMCP:
		return renderToolDetail(stats, width, true)
	case statsDetailAgents:
		return renderCategoryDetail("AGENTS", stats.AgentCounts, nil, nil, nil, width)
	case statsDetailSkills:
		return renderCategoryDetail("SKILLS", stats.SkillCounts, stats.SkillErrors, nil, nil, width)
	case statsDetailCommands:
		return renderCategoryDetail("COMMANDS", stats.CommandCounts, stats.CommandErrors, nil, nil, width)
	case statsDetailErrors:
		return renderErrorDetail(stats, width)
	case statsDetailHooks:
		return renderCategoryDetail("HOOKS", stats.HookCounts, nil, stats.HookTimestamps, nil, width)
	case statsDetailRepos:
		return renderProjectDetail(stats, width)
	case statsDetailProjects:
		return renderProjectPathDetail(stats, width)
	}
	return ""
}

// statsDetailTitle returns a short title for the detail mode.
func statsDetailTitle(mode statsDetailMode) string {
	switch mode {
	case statsDetailTools:
		return "Tools"
	case statsDetailMCP:
		return "MCP Tools"
	case statsDetailAgents:
		return "Agents"
	case statsDetailSkills:
		return "Skills"
	case statsDetailCommands:
		return "Commands"
	case statsDetailErrors:
		return "Errors"
	case statsDetailHooks:
		return "Hooks"
	case statsDetailRepos:
		return "Repos"
	case statsDetailProjects:
		return "Projects"
	}
	return ""
}

// renderToolDetail shows detailed per-tool stats with daily timelines and error trends.
func renderToolDetail(stats session.GlobalStats, width int, mcpOnly bool) string {
	counts := make(map[string]int)
	errors := make(map[string]int)
	callTS := make(map[string][]time.Time)
	errTS := make(map[string][]time.Time)

	for k, v := range stats.ToolCounts {
		isMCP := len(k) > 5 && k[:5] == "mcp__"
		if mcpOnly == isMCP {
			counts[k] = v
		}
	}
	for k, v := range stats.ToolErrors {
		isMCP := len(k) > 5 && k[:5] == "mcp__"
		if mcpOnly == isMCP {
			errors[k] = v
		}
	}
	for k, v := range stats.AllToolCallTimestamps {
		isMCP := len(k) > 5 && k[:5] == "mcp__"
		if mcpOnly == isMCP {
			callTS[k] = v
		}
	}
	for k, v := range stats.AllToolErrorTimestamps {
		isMCP := len(k) > 5 && k[:5] == "mcp__"
		if mcpOnly == isMCP {
			errTS[k] = v
		}
	}

	label := "TOOLS"
	if mcpOnly {
		label = "MCP TOOLS"
	}
	return renderCategoryDetail(label, counts, errors, callTS, errTS, width)
}

type detailEntry struct {
	name      string
	count     int
	errors    int
	callTS    []time.Time
	errTS     []time.Time
	errRate   float64
	weekDelta float64
}

// renderCategoryDetail renders a detailed drill-down for a category (tools, skills, commands).
func renderCategoryDetail(label string, counts, errors map[string]int, callTS, errTS map[string][]time.Time, width int) string {
	titleStyle := statTitleStyle
	labelStyle := dimStyle
	accentStyle := statAccentStyle
	errStyle := errorStyle
	ruler := dimStyle.Render(strings.Repeat("─", min(width, 50)))

	var sb strings.Builder

	// Build entries with capped error rates
	var entries []detailEntry
	totalCalls := 0
	totalErrors := 0
	for name, count := range counts {
		e := detailEntry{name: name, count: count}
		if errors != nil {
			e.errors = min(errors[name], count) // cap errors to calls
		}
		if count > 0 {
			e.errRate = float64(e.errors) * 100 / float64(count)
		}
		if callTS != nil {
			e.callTS = callTS[name]
		}
		if errTS != nil {
			e.errTS = errTS[name]
		}
		e.weekDelta = weekOverWeekDelta(e.callTS)
		entries = append(entries, e)
		totalCalls += count
		totalErrors += e.errors
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].count > entries[j].count })

	// Header
	header := fmt.Sprintf("%s DETAIL (%d total", label, totalCalls)
	if totalErrors > 0 {
		rate := float64(totalErrors) * 100 / float64(max(totalCalls, 1))
		header += errStyle.Render(fmt.Sprintf(", %d errors %.0f%%", totalErrors, rate))
	}
	header += ")"
	sb.WriteString(titleStyle.Render(header) + "\n")
	sb.WriteString(ruler + "\n\n")

	// Bar chart of all items
	shortCounts := make(map[string]int, len(entries))
	shortErrors := make(map[string]int, len(entries))
	for _, e := range entries {
		name := shortenToolName(e.name)
		shortCounts[name] += e.count
		if e.errors > 0 {
			shortErrors[name] += e.errors
		}
	}
	renderToolBarWithErrors(&sb, shortCounts, shortErrors, width, 30)
	sb.WriteString("\n")

	// Top error rates (only if errors exist)
	if totalErrors > 0 {
		sb.WriteString(titleStyle.Render("HIGHEST ERROR RATES") + "\n")
		sb.WriteString(ruler + "\n")
		errSorted := make([]detailEntry, len(entries))
		copy(errSorted, entries)
		sort.Slice(errSorted, func(i, j int) bool { return errSorted[i].errRate > errSorted[j].errRate })
		shown := 0
		for _, e := range errSorted {
			if e.errors == 0 || shown >= 5 {
				break
			}
			name := shortenToolName(e.name)
			sb.WriteString(fmt.Sprintf("  %-30s %s  %s\n",
				truncName(name, 30),
				errStyle.Render(fmt.Sprintf("%.0f%%", e.errRate)),
				labelStyle.Render(fmt.Sprintf("(%d err / %d calls)", e.errors, e.count))))
			shown++
		}
		sb.WriteString("\n")
	}

	// Week-over-week trends
	var trending []detailEntry
	for _, e := range entries {
		if len(e.callTS) >= 3 && math.Abs(e.weekDelta) > 0.1 {
			trending = append(trending, e)
		}
	}
	if len(trending) > 0 {
		sort.Slice(trending, func(i, j int) bool {
			return math.Abs(trending[i].weekDelta) > math.Abs(trending[j].weekDelta)
		})
		sb.WriteString(titleStyle.Render("WEEK-OVER-WEEK TRENDS") + "\n")
		sb.WriteString(ruler + "\n")
		shown := 0
		for _, e := range trending {
			if shown >= 8 {
				break
			}
			name := shortenToolName(e.name)
			arrow := "↑"
			deltaStyle := accentStyle
			if e.weekDelta < 0 {
				arrow = "↓"
				deltaStyle = labelStyle
			}
			sb.WriteString(fmt.Sprintf("  %-30s %s %s\n",
				truncName(name, 30),
				deltaStyle.Render(fmt.Sprintf("%s%.0f%%", arrow, math.Abs(e.weekDelta))),
				labelStyle.Render(fmt.Sprintf("(%d calls)", e.count))))
			shown++
		}
		sb.WriteString("\n")
	}

	// Per-item timelines (only for items with timestamps)
	hasTimelines := false
	for _, e := range entries {
		if len(e.callTS) >= 2 {
			hasTimelines = true
			break
		}
	}

	if hasTimelines {
		sb.WriteString(titleStyle.Render("TIMELINES") + "\n")
		sb.WriteString(ruler + "\n")

		maxNameW := 0
		for _, e := range entries {
			n := len(shortenToolName(e.name))
			if n > maxNameW {
				maxNameW = n
			}
		}
		maxLabelW := max(width*2/5, 14)
		if maxNameW > maxLabelW {
			maxNameW = maxLabelW
		}
		sparkW := width - maxNameW - 20
		if sparkW < 8 {
			sparkW = 8
		}

		var firstDay, lastDay string
		shown := 0
		for _, e := range entries {
			if len(e.callTS) < 2 {
				continue
			}
			if shown >= 15 {
				break
			}
			name := shortenToolName(e.name)
			if len(name) > maxNameW {
				name = name[:maxNameW-1] + "…"
			}
			buckets, fd, ld := dailyBuckets(e.callTS, sparkW)
			if len(buckets) < 2 {
				continue
			}
			if firstDay == "" {
				firstDay, lastDay = fd, ld
			}
			spark := sparkline(buckets, sparkW)
			line := fmt.Sprintf("  %-*s %s %d", maxNameW, name, accentStyle.Render(spark), e.count)

			if len(e.errTS) > 0 {
				errBuckets, _, _ := dailyBuckets(e.errTS, min(sparkW/3, 10))
				if hasNonZero(errBuckets) {
					errSpark := sparkline(errBuckets, min(sparkW/3, 10))
					line += "  " + errStyle.Render(errSpark) + errStyle.Render(fmt.Sprintf(" %d err", len(e.errTS)))
				}
			}
			sb.WriteString(line + "\n")
			shown++
		}
		if firstDay != "" {
			sb.WriteString(fmt.Sprintf("  %-*s%s%s\n",
				maxNameW+1, "",
				labelStyle.Render(firstDay),
				labelStyle.Render(fmt.Sprintf("%*s", max(sparkW-len(firstDay)-len(lastDay), 0), lastDay))))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// renderErrorDetail shows a unified error breakdown across all categories with timelines.
func renderErrorDetail(stats session.GlobalStats, width int) string {
	titleStyle := statTitleStyle
	labelStyle := dimStyle
	errStyle := errorStyle
	ruler := dimStyle.Render(strings.Repeat("─", min(width, 50)))

	var sb strings.Builder
	var items []errItem
	totalErrors := 0

	// Collect tool errors
	for name, errs := range stats.ToolErrors {
		if errs > 0 {
			calls := max(stats.ToolCounts[name], errs)
			items = append(items, errItem{
				name:    shortenToolName(name),
				errors:  errs,
				calls:   calls,
				errRate: float64(errs) * 100 / float64(calls),
				callTS:  stats.AllToolCallTimestamps[name],
				errTS:   stats.AllToolErrorTimestamps[name],
			})
			totalErrors += errs
		}
	}
	// Collect skill errors
	for name, errs := range stats.SkillErrors {
		if errs > 0 {
			calls := max(stats.SkillCounts[name], errs)
			items = append(items, errItem{
				name:    "skill:" + name,
				errors:  errs,
				calls:   calls,
				errRate: float64(errs) * 100 / float64(calls),
			})
			totalErrors += errs
		}
	}
	// Collect command errors
	for name, errs := range stats.CommandErrors {
		if errs > 0 {
			calls := max(stats.CommandCounts[name], errs)
			items = append(items, errItem{
				name:    name,
				errors:  errs,
				calls:   calls,
				errRate: float64(errs) * 100 / float64(calls),
			})
			totalErrors += errs
		}
	}

	sort.Slice(items, func(i, j int) bool { return items[i].errors > items[j].errors })

	sb.WriteString(titleStyle.Render(fmt.Sprintf("ERROR DETAIL (%d total)", totalErrors)) + "\n")
	sb.WriteString(ruler + "\n\n")

	// Global error timeline
	sparkW := min(width-6, 50)
	if sparkW < 10 {
		sparkW = 10
	}

	if len(stats.AllErrorTimestamps) > 0 {
		sb.WriteString(titleStyle.Render("ERROR TIMELINE") + "\n")
		sb.WriteString(ruler + "\n")
		buckets, firstDay, lastDay := dailyBuckets(stats.AllErrorTimestamps, sparkW)
		if len(buckets) >= 2 {
			spark := sparkline(buckets, sparkW)
			sb.WriteString(fmt.Sprintf("  %s  %s\n",
				errStyle.Render(spark),
				labelStyle.Render(fmt.Sprintf("%s → %s", firstDay, lastDay))))
		}
		sb.WriteString("\n")
	}

	// Error bar chart
	sb.WriteString(titleStyle.Render("BY SOURCE") + "\n")
	sb.WriteString(ruler + "\n")

	errCounts := make(map[string]int, len(items))
	for _, item := range items {
		errCounts[item.name] = item.errors
	}
	renderErrorBarChart(&sb, items, width, 20)
	sb.WriteString("\n")

	// Overall error rate trend
	if len(stats.AllErrorTimestamps) > 0 && len(stats.AllMsgTimestamps) > 7 {
		sb.WriteString(titleStyle.Render("ERROR RATE TREND") + "\n")
		sb.WriteString(ruler + "\n")
		rollingRates := rollingErrorRate(stats.AllMsgTimestamps, stats.AllErrorTimestamps, sparkW)
		if hasNonZero(rollingRates) {
			rateSpark := sparkline(rollingRates, sparkW)
			sb.WriteString(fmt.Sprintf("  %s  %s\n",
				errStyle.Render(rateSpark),
				labelStyle.Render("rolling error rate %")))
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// renderErrorBarChart renders error items as a bar chart sorted by error count.
func renderErrorBarChart(sb *strings.Builder, items []errItem, width, limit int) {
	if len(items) == 0 {
		return
	}
	errStyle := errorStyle
	rateStyle := dimStyle

	maxErrs := items[0].errors
	maxNameW := 0
	for _, e := range items {
		if len(e.name) > maxNameW {
			maxNameW = len(e.name)
		}
	}
	maxLabelW := max(width*2/5, 14)
	if maxNameW > maxLabelW {
		maxNameW = maxLabelW
	}
	countW := len(fmt.Sprintf("%d", maxErrs))
	barMaxW := width - maxNameW - countW - 20
	if barMaxW < 3 {
		barMaxW = 3
	}

	shown := min(len(items), limit)
	for _, e := range items[:shown] {
		name := e.name
		if len(name) > maxNameW {
			name = name[:maxNameW-1] + "…"
		}
		barLen := e.errors * barMaxW / maxErrs
		if barLen < 1 {
			barLen = 1
		}
		bar := errStyle.Render(strings.Repeat("█", barLen))
		sb.WriteString(fmt.Sprintf("  %-*s %s %s %s\n",
			maxNameW, name,
			bar,
			errStyle.Render(fmt.Sprintf("%*d", countW, e.errors)),
			rateStyle.Render(fmt.Sprintf("(%.0f%% of %d)", e.errRate, e.calls))))
	}
}

// errItem is used by renderErrorDetail (forward-declared for renderErrorBarChart).
type errItem struct {
	name    string
	errors  int
	calls   int
	errRate float64
	callTS  []time.Time
	errTS   []time.Time
}

// weekOverWeekDelta computes the % change in daily average calls between the most recent
// 7-day period and the prior 7-day period. Returns 0 if insufficient data.
func weekOverWeekDelta(timestamps []time.Time) float64 {
	if len(timestamps) < 3 {
		return 0
	}

	sort.Slice(timestamps, func(i, j int) bool { return timestamps[i].Before(timestamps[j]) })
	last := timestamps[len(timestamps)-1]
	cutoff := last.AddDate(0, 0, -7)
	priorCutoff := last.AddDate(0, 0, -14)

	var recentCount, priorCount int
	for _, ts := range timestamps {
		if ts.After(cutoff) {
			recentCount++
		} else if ts.After(priorCutoff) {
			priorCount++
		}
	}

	if priorCount == 0 {
		if recentCount > 0 {
			return 100
		}
		return 0
	}

	return (float64(recentCount) - float64(priorCount)) * 100 / float64(priorCount)
}

// rollingErrorRate computes a rolling error rate across daily buckets.
// Returns rates scaled to 0-100 for sparkline rendering.
func rollingErrorRate(callTS, errTS []time.Time, numBuckets int) []int {
	if len(callTS) == 0 {
		return nil
	}

	sort.Slice(callTS, func(i, j int) bool { return callTS[i].Before(callTS[j]) })
	sort.Slice(errTS, func(i, j int) bool { return errTS[i].Before(errTS[j]) })

	first := callTS[0].Truncate(24 * time.Hour)
	last := callTS[len(callTS)-1].Truncate(24 * time.Hour)
	days := int(last.Sub(first)/(24*time.Hour)) + 1
	if days < 3 {
		return nil
	}

	callBuckets := make([]int, days)
	errBuckets := make([]int, days)

	for _, ts := range callTS {
		idx := int(ts.Sub(first) / (24 * time.Hour))
		if idx >= days {
			idx = days - 1
		}
		callBuckets[idx]++
	}
	for _, ts := range errTS {
		idx := int(ts.Sub(first) / (24 * time.Hour))
		if idx < 0 {
			continue
		}
		if idx >= days {
			idx = days - 1
		}
		errBuckets[idx]++
	}

	// Rolling window (3-day)
	window := min(3, days)
	rates := make([]int, days-window+1)
	for i := range rates {
		calls, errs := 0, 0
		for j := i; j < i+window; j++ {
			calls += callBuckets[j]
			errs += errBuckets[j]
		}
		if calls > 0 {
			rates[i] = int(float64(errs) * 100 / float64(calls))
		}
	}

	// Downsample to numBuckets
	if len(rates) > numBuckets && numBuckets > 0 {
		bucketSize := (len(rates) + numBuckets - 1) / numBuckets
		var ds []int
		for i := 0; i < len(rates); i += bucketSize {
			sum, count := 0, 0
			for j := i; j < min(i+bucketSize, len(rates)); j++ {
				sum += rates[j]
				count++
			}
			ds = append(ds, sum/max(count, 1))
		}
		rates = ds
	}

	return rates
}

func truncName(name string, maxW int) string {
	if len(name) > maxW {
		return name[:maxW-1] + "…"
	}
	return name
}
