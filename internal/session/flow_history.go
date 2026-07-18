package session

import "time"

// TouchHistory records the first and last time a file path was written during
// the session, plus how many write occurrences there were. Populated from the
// timestamped ArtifactChange / ArtifactPlan occurrences the flow index already
// stores (every occurrence is kept, never collapsed by path).
type TouchHistory struct {
	First time.Time
	Last  time.Time
	Count int
}

// MemoryTouchHistory returns write history keyed by memory-note basename (e.g.
// "MEMORY.md", "k7s-search-input-ux.md") for every memory file touched in the
// session scope. Only Edit/Write/NotebookEdit occurrences under a memory dir or
// MEMORY.md count. Returns an empty map when the flow index is nil or no memory
// writes exist.
func (fi *FlowIndex) MemoryTouchHistory() map[string]TouchHistory {
	out := make(map[string]TouchHistory)
	if fi == nil {
		return out
	}
	for _, a := range fi.Artifacts(fi.RootID, ArtifactChange, ScopeSession) {
		if !isMemoryPath(a.Key) {
			continue
		}
		accumulateTouch(out, baseName(a.Key), a.Origin.Timestamp)
	}
	return out
}

// PlanTouchHistory returns ExitPlanMode write history keyed by plan file path
// (Artifact.Key of ArtifactPlan occurrences). Returns an empty map when the
// flow index is nil or no plans were written.
func (fi *FlowIndex) PlanTouchHistory() map[string]TouchHistory {
	out := make(map[string]TouchHistory)
	if fi == nil {
		return out
	}
	for _, a := range fi.Artifacts(fi.RootID, ArtifactPlan, ScopeSession) {
		accumulateTouch(out, a.Key, a.Origin.Timestamp)
	}
	return out
}

// accumulateTouch folds one occurrence timestamp into the history for key,
// widening [First, Last] and bumping the count. Zero timestamps still count as
// an occurrence but do not move the bounds.
func accumulateTouch(hist map[string]TouchHistory, key string, ts time.Time) {
	h := hist[key]
	h.Count++
	if !ts.IsZero() {
		if h.First.IsZero() || ts.Before(h.First) {
			h.First = ts
		}
		if ts.After(h.Last) {
			h.Last = ts
		}
	}
	hist[key] = h
}
