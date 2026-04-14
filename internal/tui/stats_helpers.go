package tui

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
)

func renderErrorBreakdown(sb *strings.Builder, toolErrors, toolCounts map[string]int, skillErrors, skillCounts map[string]int, cmdErrors, cmdCounts map[string]int, totalErrors int, width int, ruler string, titleStyle lipgloss.Style) {
	errStyle := errorStyle
	rateStyle := dimStyle

	type errEntry struct {
		name   string
		errors int
		calls  int
	}
	var entries []errEntry

	// Collect tool errors
	for name, errs := range toolErrors {
		if errs > 0 {
			entries = append(entries, errEntry{name: shortenToolName(name), errors: errs, calls: toolCounts[name]})
		}
	}
	// Collect skill errors
	for name, errs := range skillErrors {
		if errs > 0 {
			entries = append(entries, errEntry{name: "skill:" + name, errors: errs, calls: skillCounts[name]})
		}
	}
	// Collect command errors
	for name, errs := range cmdErrors {
		if errs > 0 {
			entries = append(entries, errEntry{name: name, errors: errs, calls: cmdCounts[name]})
		}
	}

	if len(entries) == 0 {
		return
	}

	// Sort by error count descending
	sort.Slice(entries, func(i, j int) bool { return entries[i].errors > entries[j].errors })

	sb.WriteString(titleStyle.Render(fmt.Sprintf("ERRORS (%d total)", totalErrors)) + "\n")
	sb.WriteString(ruler + "\n")

	maxErrs := entries[0].errors
	maxNameW := 0
	for _, e := range entries {
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

	limit := min(len(entries), 15)
	for _, e := range entries[:limit] {
		name := e.name
		if len(name) > maxNameW {
			name = name[:maxNameW-1] + "…"
		}
		barLen := e.errors * barMaxW / maxErrs
		if barLen < 1 {
			barLen = 1
		}
		bar := errStyle.Render(strings.Repeat("█", barLen))
		rate := float64(e.errors) * 100 / float64(max(e.calls, 1))
		sb.WriteString(fmt.Sprintf("  %-*s %s %s %s\n",
			maxNameW, name,
			bar,
			errStyle.Render(fmt.Sprintf("%*d", countW, e.errors)),
			rateStyle.Render(fmt.Sprintf("(%.0f%% of %d)", rate, e.calls))))
	}
	sb.WriteString("\n")
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

	barStyle := statAccentStyle
	errBarStyle := errorStyle

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

func renderToolBarN(sb *strings.Builder, counts map[string]int, width int, limit int) {
	renderToolBarWithErrors(sb, counts, nil, width, limit)
}

// renderProjectStats renders per-project token/cost breakdown.
// Each project gets two lines: name on top, then cost bar + stats.
func renderProjectStats(sb *strings.Builder, projects []session.ProjectStats, width int) {
	if len(projects) == 0 {
		return
	}

	numStyle := statNumStyle
	labelStyle := dimStyle
	costStyle := statCostStyle

	maxCost := projects[0].CostUSD
	if maxCost <= 0 {
		maxCost = 1
	}

	barW := width - 36
	if barW < 5 {
		barW = 5
	}

	limit := min(len(projects), 15)
	for _, ps := range projects[:limit] {
		// Line 1: project path (shortened with ~ for home dir)
		path := ps.ProjectPath
		if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home) {
			path = "~" + path[len(home):]
		}
		maxPathW := width - 4
		if len(path) > maxPathW {
			path = "..." + path[len(path)-maxPathW+3:]
		}
		sb.WriteString(fmt.Sprintf("  %s\n", path))

		// Line 2: cost + bar + stats
		barLen := int(float64(barW) * ps.CostUSD / maxCost)
		if barLen < 1 && ps.CostUSD > 0 {
			barLen = 1
		}
		bar := strings.Repeat("█", barLen)

		sb.WriteString(fmt.Sprintf("    %s %s  %s out  %s sess  %s msgs\n",
			costStyle.Render(fmt.Sprintf("%7s", fmtCost(ps.CostUSD))),
			labelStyle.Render(bar),
			numStyle.Render(fmtNum(ps.TotalOutputTokens)),
			labelStyle.Render(fmt.Sprintf("%d", ps.SessionCount)),
			labelStyle.Render(fmt.Sprintf("%d", ps.TotalMessages))))
	}
	if len(projects) > limit {
		sb.WriteString(labelStyle.Render(fmt.Sprintf("  ... and %d more projects\n", len(projects)-limit)))
	}
}

// renderProjectDetail renders the full project stats drill-down page.
func renderProjectDetail(stats session.GlobalStats, width int) string {
	if len(stats.ProjectStats) == 0 {
		return dimStyle.Render("(no project data)")
	}

	var sb strings.Builder
	titleStyle := statTitleStyle
	numStyle := statNumStyle
	labelStyle := dimStyle
	costStyle := statCostStyle
	ruler := dimStyle.Render(strings.Repeat("─", min(width, 40)))

	sb.WriteString(titleStyle.Render(fmt.Sprintf("PROJECTS (%d)", len(stats.ProjectStats))) + "\n")
	sb.WriteString(ruler + "\n\n")

	maxCost := stats.ProjectStats[0].CostUSD
	if maxCost <= 0 {
		maxCost = 1
	}

	barW := width - 36
	if barW < 5 {
		barW = 5
	}

	for i, ps := range stats.ProjectStats {
		// Project path (shortened with ~ for home dir)
		path := ps.ProjectPath
		if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home) {
			path = "~" + path[len(home):]
		}

		// Rank number
		rank := fmt.Sprintf("%2d. ", i+1)

		maxPathW := width - len(rank) - 2
		if len(path) > maxPathW {
			path = "..." + path[len(path)-maxPathW+3:]
		}
		sb.WriteString(fmt.Sprintf("  %s%s\n", labelStyle.Render(rank), path))

		// Cost bar
		barLen := int(float64(barW) * ps.CostUSD / maxCost)
		if barLen < 1 && ps.CostUSD > 0 {
			barLen = 1
		}
		bar := strings.Repeat("█", barLen)

		sb.WriteString(fmt.Sprintf("      %s %s\n",
			costStyle.Render(fmt.Sprintf("%7s", fmtCost(ps.CostUSD))),
			labelStyle.Render(bar)))

		// Token details
		sb.WriteString(fmt.Sprintf("      %s out   %s sess   %s msgs   %s duration\n",
			numStyle.Render(fmtNum(ps.TotalOutputTokens)),
			labelStyle.Render(fmt.Sprintf("%d", ps.SessionCount)),
			labelStyle.Render(fmt.Sprintf("%d", ps.TotalMessages)),
			labelStyle.Render(fmtDuration(ps.TotalDuration))))
		sb.WriteString("\n")
	}

	return sb.String()
}

