package session

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"strings"
	"sync"
)

type SearchQuery struct {
	Terms    []string // AND-matched terms (lowercased)
	Phrases  []string // Exact phrases (lowercased)
	Exclude  []string // Negated terms (lowercased)
	ToolName string   // Filter by tool name
	Role     string   // "user" or "assistant"
}

type SearchResult struct {
	Session *Session
	Entry   *Entry
	Block   *ContentBlock
	Snippet string
}

func ParseSearchQuery(input string) SearchQuery {
	input = strings.TrimSpace(input)
	if input == "" {
		return SearchQuery{}
	}

	var q SearchQuery
	var current strings.Builder
	inQuote := false

	flush := func() {
		token := strings.TrimSpace(current.String())
		current.Reset()
		if token == "" {
			return
		}

		lower := strings.ToLower(token)

		if strings.HasPrefix(lower, "-") && len(lower) > 1 {
			q.Exclude = append(q.Exclude, lower[1:])
			return
		}

		if strings.HasPrefix(lower, "tool:") {
			q.ToolName = lower[5:]
			return
		}

		if strings.HasPrefix(lower, "user:") {
			q.Role = "user"
			if rest := lower[5:]; rest != "" {
				q.Terms = append(q.Terms, rest)
			}
			return
		}

		if strings.HasPrefix(lower, "assistant:") {
			q.Role = "assistant"
			if rest := lower[10:]; rest != "" {
				q.Terms = append(q.Terms, rest)
			}
			return
		}

		q.Terms = append(q.Terms, lower)
	}

	for _, r := range input {
		if r == '"' {
			if inQuote {
				phrase := strings.ToLower(strings.TrimSpace(current.String()))
				current.Reset()
				if phrase != "" {
					q.Phrases = append(q.Phrases, phrase)
				}
			}
			inQuote = !inQuote
			continue
		}

		if r == ' ' && !inQuote {
			flush()
			continue
		}

		current.WriteRune(r)
	}
	flush()

	return q
}

func (q SearchQuery) IsEmpty() bool {
	return len(q.Terms) == 0 && len(q.Phrases) == 0 && len(q.Exclude) == 0 && q.ToolName == "" && q.Role == ""
}

func SearchSessions(sessions []*Session, query SearchQuery, ctx context.Context) <-chan SearchResult {
	results := make(chan SearchResult, 100)

	go func() {
		defer close(results)

		if query.IsEmpty() {
			return
		}

		var wg sync.WaitGroup
		sem := make(chan struct{}, 50) // max 50 concurrent file scans

		for _, sess := range sessions {
			select {
			case <-ctx.Done():
				return
			default:
			}

			wg.Add(1)
			sem <- struct{}{}

			go func(s *Session) {
				defer wg.Done()
				defer func() { <-sem }()
				searchSession(s, query, ctx, results)
			}(sess)
		}

		wg.Wait()
	}()

	return results
}

func searchSession(sess *Session, query SearchQuery, ctx context.Context, results chan<- SearchResult) {
	f, err := os.Open(sess.FilePath)
	if err != nil {
		return
	}
	defer f.Close()

	// Build byte patterns for fast pre-filtering
	var mustContain [][]byte
	for _, term := range query.Terms {
		mustContain = append(mustContain, []byte(term))
	}
	for _, phrase := range query.Phrases {
		mustContain = append(mustContain, []byte(phrase))
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 256*1024), 10*1024*1024) // starts at 256 KB and can grow upto 10 MB

	for sc.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}

		// Skip metadata lines
		if bytes.Contains(line, []byte(`"isMeta":true`)) || bytes.Contains(line, []byte(`"isMeta": true`)) {
			continue
		}

		// Fast byte-level pre-filter: line must contain all terms
		lineLower := bytes.ToLower(line)
		match := true
		for _, pattern := range mustContain {
			if !bytes.Contains(lineLower, pattern) {
				match = false
				break
			}
		}
		if !match {
			continue
		}

		// Now parse the entry
		entry, err := ParseEntry(string(line))
		if err != nil {
			continue
		}

		// Role filter
		if query.Role != "" && entry.Role != query.Role {
			continue
		}

		// Search through content blocks
		for i := range entry.Content {
			block := &entry.Content[i]

			// Tool filter
			if query.ToolName != "" {
				if block.Type != "tool_use" || !strings.EqualFold(block.ToolName, query.ToolName) {
					continue
				}
			}

			searchText := blockSearchText(block)
			searchLower := strings.ToLower(searchText)

			// Check all terms match
			allMatch := true
			for _, term := range query.Terms {
				if !strings.Contains(searchLower, term) {
					allMatch = false
					break
				}
			}
			if !allMatch {
				continue
			}

			// Check phrases match
			for _, phrase := range query.Phrases {
				if !strings.Contains(searchLower, phrase) {
					allMatch = false
					break
				}
			}
			if !allMatch {
				continue
			}

			// Check exclusions
			excluded := false
			for _, excl := range query.Exclude {
				if strings.Contains(searchLower, excl) {
					excluded = true
					break
				}
			}
			if excluded {
				continue
			}

			// Build snippet
			snippet := buildSnippet(searchText, query.Terms, query.Phrases)

			select {
			case results <- SearchResult{
				Session: sess,
				Entry:   &entry,
				Block:   block,
				Snippet: snippet,
			}:
			case <-ctx.Done():
				return
			}
		}
	}
}

func blockSearchText(block *ContentBlock) string {
	switch block.Type {
	case "text":
		return block.Text
	case "tool_use":
		return block.ToolName + " " + block.ToolInput
	case "tool_result":
		return block.Text
	case "thinking":
		return block.Text
	default:
		return block.Text
	}
}

func buildSnippet(text string, terms, phrases []string) string {
	if len(text) == 0 {
		return ""
	}

	// Find first match position
	textLower := strings.ToLower(text)
	firstMatch := len(text)

	for _, term := range terms {
		if idx := strings.Index(textLower, term); idx >= 0 && idx < firstMatch {
			firstMatch = idx
		}
	}
	for _, phrase := range phrases {
		if idx := strings.Index(textLower, phrase); idx >= 0 && idx < firstMatch {
			firstMatch = idx
		}
	}

	// Extract context around match
	start := firstMatch - 40
	if start < 0 {
		start = 0
	}
	end := firstMatch + 80
	if end > len(text) {
		end = len(text)
	}

	snippet := text[start:end]
	snippet = strings.ReplaceAll(snippet, "\n", " ")
	snippet = strings.TrimSpace(snippet)

	if start > 0 {
		snippet = "..." + snippet
	}
	if end < len(text) {
		snippet = snippet + "..."
	}

	// Highlight matched terms (bold + underline works across themes)
	snippet = highlightMatches(snippet, terms, phrases)

	return snippet
}

func highlightMatches(text string, terms, phrases []string) string {
	// ANSI: bold + underline, reset
	const hlStart = "\x1b[1;4m"
	const hlEnd = "\x1b[0m"

	textLower := strings.ToLower(text)

	type span struct{ start, end int }
	var spans []span

	for _, term := range terms {
		idx := 0
		for {
			pos := strings.Index(textLower[idx:], term)
			if pos < 0 {
				break
			}
			absPos := idx + pos
			spans = append(spans, span{absPos, absPos + len(term)})
			idx = absPos + len(term)
		}
	}
	for _, phrase := range phrases {
		idx := 0
		for {
			pos := strings.Index(textLower[idx:], phrase)
			if pos < 0 {
				break
			}
			absPos := idx + pos
			spans = append(spans, span{absPos, absPos + len(phrase)})
			idx = absPos + len(phrase)
		}
	}

	if len(spans) == 0 {
		return text
	}

	// Sort by start
	for i := 0; i < len(spans)-1; i++ {
		for j := i + 1; j < len(spans); j++ {
			if spans[j].start < spans[i].start {
				spans[i], spans[j] = spans[j], spans[i]
			}
		}
	}

	// Merge overlapping
	merged := []span{spans[0]}
	for i := 1; i < len(spans); i++ {
		last := &merged[len(merged)-1]
		if spans[i].start <= last.end {
			if spans[i].end > last.end {
				last.end = spans[i].end
			}
		} else {
			merged = append(merged, spans[i])
		}
	}

	// Build highlighted string
	var out strings.Builder
	cursor := 0
	for _, s := range merged {
		if s.start > cursor {
			out.WriteString(text[cursor:s.start])
		}
		out.WriteString(hlStart)
		out.WriteString(text[s.start:s.end])
		out.WriteString(hlEnd)
		cursor = s.end
	}
	if cursor < len(text) {
		out.WriteString(text[cursor:])
	}

	return out.String()
}
