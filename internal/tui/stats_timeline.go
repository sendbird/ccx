package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/keyolk/tuikit/chart"
)

// chartLook draws ccx's sparklines: block ramp, or the equal-width ASCII
// ramp under a CJK locale (or CCX_ASCII), where block elements are East Asian
// ambiguous and draw two cells (tuikit/chart).
var chartLook = chart.Look{Glyphs: chart.DetectGlyphs("ccx")}

func histogramBar(value, maxVal, width int) string {
	if width <= 0 {
		return ""
	}
	if maxVal <= 0 || value <= 0 {
		return strings.Repeat(iconBarEmpty, width)
	}
	filled := value * width / maxVal
	if filled < 1 {
		filled = 1
	}
	if filled > width {
		filled = width
	}
	return strings.Repeat(iconBarFull, filled) + strings.Repeat(iconBarEmpty, width-filled)
}

func dualSparkline(a, b []int, maxWidth int) string {
	left := sparkline(a, maxWidth)
	right := sparkline(b, maxWidth)
	floor := chartLook.Glyphs.Levels[0]
	if left == "" {
		left = strings.Repeat(floor, min(len(b), maxWidth))
	}
	if right == "" {
		right = strings.Repeat(floor, min(len(a), maxWidth))
	}
	if len(left) == 0 {
		return ""
	}
	return left + " · " + right
}

// sparkline draws values on a zero baseline, one cell per value up to
// maxWidth (a shorter series stays short, so text after it does not move).
//
// tuikit/chart.Sparkline buckets a long series so every cell covers an equal
// share. The old ceil-sized buckets left fewer cells than the width and a
// last bucket averaging a partial tail, and maxWidth 0 divided by zero.
func sparkline(values []int, maxWidth int) string {
	if len(values) == 0 || maxWidth <= 0 {
		return ""
	}
	fs := make([]float64, len(values))
	peak := 0
	for i, v := range values {
		fs[i] = float64(v)
		peak = max(peak, v)
	}
	o := chart.SparkOpts{Look: chartLook, Min: chart.Fixed(0)}
	if peak <= 0 {
		// The kit draws a constant series at mid-level; an idle series
		// should sit on the floor.
		o.Max = chart.Fixed(1)
	}
	return chart.Sparkline(fs, min(len(values), maxWidth), o)
}

func hasNonZero(vals []int) bool {
	for _, v := range vals {
		if v > 0 {
			return true
		}
	}
	return false
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
func renderToolTimelines(sb *strings.Builder, toolCallTS, toolErrTS map[string][]time.Time, toolCounts map[string]int, start, end time.Time, width, limit int) {
	dur := end.Sub(start)
	if dur <= 0 || len(toolCallTS) == 0 {
		return
	}

	type entry struct {
		name  string
		count int
	}
	var entries []entry
	for name, ts := range toolCallTS {
		if len(ts) > 0 {
			entries = append(entries, entry{name: name, count: toolCounts[name]})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].count > entries[j].count })
	if len(entries) > limit {
		entries = entries[:limit]
	}
	if len(entries) == 0 {
		return
	}

	titleStyle := statTitleStyle
	labelStyle := dimStyle
	accentStyle := statAccentStyle
	errStyle := errorStyle
	ruler := dimStyle.Render(strings.Repeat("─", min(width, 40)))

	sb.WriteString(titleStyle.Render("TOOL TIMELINES") + "\n")
	sb.WriteString(ruler + "\n")

	maxNameW := 0
	for _, e := range entries {
		short := shortenToolName(e.name)
		if len(short) > maxNameW {
			maxNameW = len(short)
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

	for _, e := range entries {
		name := shortenToolName(e.name)
		if len(name) > maxNameW {
			name = name[:maxNameW-1] + "…"
		}
		buckets := timelineBuckets(toolCallTS[e.name], start, end, sparkW)
		spark := sparkline(buckets, sparkW)
		line := fmt.Sprintf("  %-*s %s %d", maxNameW, name, accentStyle.Render(spark), e.count)

		if errTS, ok := toolErrTS[e.name]; ok && len(errTS) > 0 {
			errBuckets := timelineBuckets(errTS, start, end, min(sparkW/3, 10))
			if hasNonZero(errBuckets) {
				errSpark := sparkline(errBuckets, min(sparkW/3, 10))
				line += "  " + errStyle.Render(errSpark) + errStyle.Render(fmt.Sprintf(" %d err", len(errTS)))
			}
		}
		sb.WriteString(line + "\n")
	}

	// Time axis
	sb.WriteString(fmt.Sprintf("  %-*s%s%s\n",
		maxNameW+1, "",
		labelStyle.Render(start.Format("15:04")),
		labelStyle.Render(fmt.Sprintf("%*s", max(sparkW-10, 0), end.Format("15:04")))))
	sb.WriteString("\n")
}

// renderToolDailyTimelines renders per-tool daily activity and error sparklines for global stats.
func renderToolDailyTimelines(sb *strings.Builder, toolCallTS, toolErrTS map[string][]time.Time, toolCounts map[string]int, width, limit int) {
	if len(toolCallTS) == 0 {
		return
	}

	type entry struct {
		name  string
		count int
	}
	var entries []entry
	for name, ts := range toolCallTS {
		if len(ts) > 0 {
			entries = append(entries, entry{name: name, count: toolCounts[name]})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].count > entries[j].count })
	if len(entries) > limit {
		entries = entries[:limit]
	}
	if len(entries) == 0 {
		return
	}

	titleStyle := statTitleStyle
	labelStyle := dimStyle
	accentStyle := statAccentStyle
	errStyle := errorStyle
	ruler := dimStyle.Render(strings.Repeat("─", min(width, 40)))

	sb.WriteString(titleStyle.Render("TOOL TIMELINES (daily)") + "\n")
	sb.WriteString(ruler + "\n")

	maxNameW := 0
	for _, e := range entries {
		short := shortenToolName(e.name)
		if len(short) > maxNameW {
			maxNameW = len(short)
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
	for _, e := range entries {
		name := shortenToolName(e.name)
		if len(name) > maxNameW {
			name = name[:maxNameW-1] + "…"
		}
		buckets, fd, ld := dailyBuckets(toolCallTS[e.name], sparkW)
		if len(buckets) < 2 {
			continue
		}
		if firstDay == "" {
			firstDay, lastDay = fd, ld
		}
		spark := sparkline(buckets, sparkW)
		line := fmt.Sprintf("  %-*s %s %d", maxNameW, name, accentStyle.Render(spark), e.count)

		if errTS, ok := toolErrTS[e.name]; ok && len(errTS) > 0 {
			errBuckets, _, _ := dailyBuckets(errTS, min(sparkW/3, 10))
			if hasNonZero(errBuckets) {
				errSpark := sparkline(errBuckets, min(sparkW/3, 10))
				line += "  " + errStyle.Render(errSpark) + errStyle.Render(fmt.Sprintf(" %d err", len(errTS)))
			}
		}
		sb.WriteString(line + "\n")
	}

	if firstDay != "" {
		sb.WriteString(fmt.Sprintf("  %-*s%s%s\n",
			maxNameW+1, "",
			labelStyle.Render(firstDay),
			labelStyle.Render(fmt.Sprintf("%*s", max(sparkW-len(firstDay)-len(lastDay), 0), lastDay))))
	}
	sb.WriteString("\n")
}
