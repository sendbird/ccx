package session

import (
	"bufio"
	"context"
	"os"
	"sort"
	"strings"
)

// SearchMode records how a set of results was produced, so the UI can be honest
// about what was searched.
type SearchMode int

const (
	// SearchModeScan means every transcript was read end to end.
	SearchModeScan SearchMode = iota
	// SearchModeIndex means results came from the FTS index and are equivalent
	// to a scan for this query.
	SearchModeIndex
	// SearchModeIndexPartial means results came from the index, which does not
	// cover tool_result content.
	SearchModeIndexPartial
)

func (m SearchMode) String() string {
	switch m {
	case SearchModeIndex:
		return "index"
	case SearchModeIndexPartial:
		return "index (no tool output)"
	default:
		return "full scan"
	}
}

// SearchWithIndex answers q from the FTS index when it can, and falls back to
// the full scan when it cannot. It returns the mode actually used.
//
// The caller is responsible for having Sync'd the index; this function never
// writes to it, so a stale index yields stale results rather than a stall.
func SearchWithIndex(ctx context.Context, ix *Index, sessions []*Session, q SearchQuery, limit int) ([]SearchResult, SearchMode, error) {
	if ix == nil || ix.Coverage(q) == CoverageNone {
		return collectScan(ctx, sessions, q, limit), SearchModeScan, nil
	}

	bySession := make(map[string]*Session, len(sessions))
	for _, s := range sessions {
		if s != nil && s.FilePath != "" {
			bySession[s.FilePath] = s
		}
	}

	// The limit is applied per FILE inside queryIndex, not globally: session
	// ranking happens here in Go (it needs session mtime), so a global cut would
	// discard rows the ranking would have kept. Every file contributing its own
	// best `limit` rows is enough for any ordering this function can produce,
	// while keeping a broad query from dragging back tens of thousands of rows.
	hits, err := ix.queryIndex(ctx, q, bySession, limit)
	if err != nil {
		// An index failure is not a search failure — degrade to the scan.
		return collectScan(ctx, sessions, q, limit), SearchModeScan, nil
	}

	results, err := hydrateHits(ctx, hits, bySession, q, limit)
	if err != nil {
		return collectScan(ctx, sessions, q, limit), SearchModeScan, nil
	}

	mode := SearchModeIndex
	if ix.Coverage(q) == CoveragePartial {
		mode = SearchModeIndexPartial
	}
	return results, mode, nil
}

// hydrateHits turns index locations back into SearchResults by re-reading the
// one transcript line each hit points at. Hits arrive grouped by path, so the
// same file is opened once.
func hydrateHits(ctx context.Context, hits []indexHit, bySession map[string]*Session, q SearchQuery, limit int) ([]SearchResult, error) {
	var results []SearchResult

	for i := 0; i < len(hits); {
		select {
		case <-ctx.Done():
			return results, ctx.Err()
		default:
		}

		path := hits[i].path
		j := i
		for j < len(hits) && hits[j].path == path {
			j++
		}
		group := hits[i:j]
		i = j

		sess := bySession[path]
		if sess == nil {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}

		for _, h := range group {
			line, err := readLineAt(f, h.lineOff)
			if err != nil || line == "" {
				continue
			}
			entry, err := ParseEntry(line)
			if err != nil || h.blockIdx >= len(entry.Content) {
				continue
			}
			block := &entry.Content[h.blockIdx]

			res, ok := matchBlock(sess, &entry, block, q)
			if !ok {
				continue
			}
			results = append(results, res)
			if limit > 0 && len(results) >= limit {
				f.Close()
				return results, nil
			}
		}
		f.Close()
	}
	return results, nil
}

// readLineAt reads the single newline-terminated line starting at off.
func readLineAt(f *os.File, off int64) (string, error) {
	if _, err := f.Seek(off, 0); err != nil {
		return "", err
	}
	r := bufio.NewReaderSize(f, 256*1024)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 256*1024), 10*1024*1024)
	if !sc.Scan() {
		return "", sc.Err()
	}
	return sc.Text(), nil
}

// matchBlock re-applies the query to a single block. The index narrows
// candidates; this is what makes the result set exactly match the scan's
// semantics, including exclusions and tool-name prefixes.
func matchBlock(sess *Session, entry *Entry, block *ContentBlock, q SearchQuery) (SearchResult, bool) {
	if entry.IsMeta {
		return SearchResult{}, false
	}
	if q.Role != "" && entry.Role != q.Role {
		return SearchResult{}, false
	}
	if q.ToolName != "" {
		if block.Type != "tool_use" || !toolNameMatches(block.ToolName, q.ToolName) {
			return SearchResult{}, false
		}
	}

	text := blockSearchText(block)
	lower := strings.ToLower(text)

	for _, term := range q.Terms {
		if !strings.Contains(lower, term) {
			return SearchResult{}, false
		}
	}
	for _, phrase := range q.Phrases {
		if !strings.Contains(lower, phrase) {
			return SearchResult{}, false
		}
	}
	for _, excl := range q.Exclude {
		if strings.Contains(lower, excl) {
			return SearchResult{}, false
		}
	}

	// The entry is a loop-local value in the caller; copy it so the returned
	// pointer stays valid and distinct per result.
	e := *entry
	return SearchResult{
		Session: sess,
		Entry:   &e,
		Block:   block,
		Snippet: buildSnippet(text, q.Terms, q.Phrases),
	}, true
}

// collectScan drains the existing full-scan search into an ordered slice.
// Results are sorted newest session first to match the indexed path.
func collectScan(ctx context.Context, sessions []*Session, q SearchQuery, limit int) []SearchResult {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var out []SearchResult
	for res := range SearchSessions(sessions, q, ctx) {
		out = append(out, res)
		if limit > 0 && len(out) >= limit*4 {
			break // over-fetch, then sort and trim below
		}
	}

	// Same order as the indexed path: newest session first, and within one
	// session the user's own words before the model's (see queryIndex).
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := out[i].Session, out[j].Session
		if si == nil || sj == nil {
			return false
		}
		if !si.ModTime.Equal(sj.ModTime) {
			return si.ModTime.After(sj.ModTime)
		}
		if si.ID != sj.ID {
			return si.ID < sj.ID
		}
		return resultRoleRank(out[i]) < resultRoleRank(out[j])
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// resultRoleRank is roleRank for a hydrated result.
func resultRoleRank(r SearchResult) int {
	if r.Entry == nil {
		return 2
	}
	return roleRank(r.Entry.Role)
}
