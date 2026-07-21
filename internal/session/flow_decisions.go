package session

// markChangeDecisions emits a decision marker for every Edit/Write occurrence,
// in chronological order across all transcripts. The first change to a given
// file path is labeled "first change: <file>"; subsequent edits to the same
// file are labeled "change: <file>" so the timeline shows every change while
// still highlighting where each file was first touched. Memory writes already
// carry their own DecisionMemory marker and are skipped so a memory note does
// not produce two markers for the same write.
func (b *flowBuilder) markChangeDecisions() {
	// Collect change occurrences in chronological order across all transcripts.
	var changes []int // indices into fi.artifacts
	for i := range b.fi.artifacts {
		if b.fi.artifacts[i].Kind == ArtifactChange {
			changes = append(changes, i)
		}
	}
	if len(changes) == 0 {
		return
	}
	// Chronological: sort by timestamp, tiebreak by build order (append order
	// is already chronological per transcript).
	sortChangeOccs(b.fi.artifacts, changes)

	seen := make(map[string]bool)
	for _, ci := range changes {
		a := b.fi.artifacts[ci] // copy — appends below may reallocate the slice
		if isMemoryPath(a.Key) {
			seen[a.Key] = true
			continue // already a DecisionMemory marker
		}
		first := !seen[a.Key]
		seen[a.Key] = true
		// The "first-change:" key prefix drives the inspector's per-file change
		// history lookup; keep it for both first and subsequent markers so every
		// marker inspects to the same full history.
		key := "first-change:" + a.Key
		label := "change: " + baseName(a.Key)
		if first {
			label = "first change: " + baseName(a.Key)
		}
		b.append(Artifact{
			Kind:   ArtifactDecision,
			NodeID: a.NodeID,
			Key:    key,
			Origin: a.Origin,
			Data: DecisionData{
				Kind:    DecisionFirstChange,
				Label:   label,
				Related: a.ID,
			},
		})
	}
}

// sortChangeOccs orders change occurrence indices chronologically (stable).
func sortChangeOccs(arts []Artifact, occs []int) {
	// insertion sort — occurrence lists are small and mostly ordered already.
	for i := 1; i < len(occs); i++ {
		j := i
		for j > 0 && changeBefore(arts[occs[j]], arts[occs[j-1]]) {
			occs[j], occs[j-1] = occs[j-1], occs[j]
			j--
		}
	}
}

func changeBefore(a, b Artifact) bool {
	ta, tb := a.Origin.Timestamp, b.Origin.Timestamp
	if !ta.Equal(tb) {
		return ta.Before(tb)
	}
	if a.Origin.Transcript != b.Origin.Transcript {
		return a.Origin.Transcript < b.Origin.Transcript
	}
	return a.Origin.EntryIndex < b.Origin.EntryIndex
}

// markSteeringDecisions emits a steering marker for each real user turn (not
// meta, not a tool_result carrier, not a slash-command invocation) that is
// followed — before the next user turn — by a plan or task decision. This is
// deliberately conservative: only plan/task decisions count as evidence that
// the user's message redirected the approach.
func (b *flowBuilder) markSteeringDecisions(entries []Entry) {
	// Entry indices (in the parent transcript) that produced plan/task decisions.
	decisionEntries := make(map[int]bool)
	for i := range b.fi.artifacts {
		a := &b.fi.artifacts[i]
		if a.Kind != ArtifactDecision {
			continue
		}
		dd, ok := a.Data.(DecisionData)
		if !ok || (dd.Kind != DecisionPlan && dd.Kind != DecisionTask) {
			continue
		}
		if a.Origin.Transcript != b.sess.FilePath {
			continue // agent-internal decisions don't credit user steering
		}
		decisionEntries[a.Origin.EntryIndex] = true
	}
	if len(decisionEntries) == 0 {
		return
	}

	for i := range entries {
		e := &entries[i]
		if !isSteeringCandidate(e) {
			continue
		}
		// Window: from just after this user turn to the next real user turn.
		end := len(entries)
		for j := i + 1; j < len(entries); j++ {
			if isSteeringCandidate(&entries[j]) {
				end = j
				break
			}
		}
		hit := false
		for j := i + 1; j < end; j++ {
			if decisionEntries[j] {
				hit = true
				break
			}
		}
		if !hit {
			continue
		}
		preview := EntryPreview(*e)
		b.append(Artifact{
			Kind:   ArtifactDecision,
			NodeID: b.turnForEntry(i),
			Key:    "steering:" + e.UUID,
			Origin: ArtifactOrigin{
				SessionID:   b.sess.ID,
				Transcript:  b.sess.FilePath,
				MessageUUID: e.UUID,
				EntryIndex:  i,
				Timestamp:   e.Timestamp,
			},
			Data: DecisionData{Kind: DecisionSteering, Label: preview},
		})
	}
}

// isSteeringCandidate reports whether an entry is a real user message: user
// role, not meta, carries visible text, and is not a tool_result carrier or a
// slash-command invocation.
func isSteeringCandidate(e *Entry) bool {
	if e.Role != "user" || e.IsMeta {
		return false
	}
	hasText := false
	for _, blk := range e.Content {
		switch blk.Type {
		case "tool_result":
			return false
		case "system_tag":
			// command invocations arrive as <command-name> system tags
			if blk.TagName == "command-name" {
				return false
			}
		case "text":
			if len(blk.Text) > 0 {
				hasText = true
			}
		}
	}
	return hasText
}
