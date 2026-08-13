package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// OutputKind classifies one thing a session produced. This is deliberately
// narrower than ArtifactKind: the flow index records every occurrence of
// everything a session *touched* (including reads), while an output is
// something that outlived the session — a plan, a memory note, a scratchpad
// file, an edited source file, a published artifact, a PR, a Jira issue.
type OutputKind string

const (
	OutputPlan       OutputKind = "plan"
	OutputMemory     OutputKind = "memory"
	OutputScratchpad OutputKind = "scratchpad"
	OutputChange     OutputKind = "change"
	OutputArtifact   OutputKind = "artifact"
	OutputPR         OutputKind = "pr"
	OutputJira       OutputKind = "jira"
)

// SessionOutput is one produced item, collapsed per identity (plan slug, file
// path, ref label) with its occurrence count and time span.
type SessionOutput struct {
	Kind   OutputKind
	Title  string // plan slug, memory note name, file basename, ref label
	Detail string // description / relative path / status text
	Path   string // local filesystem path, when the output is a file
	URL    string // external URL, when the output is a ref

	First time.Time
	Last  time.Time
	Count int // number of write occurrences (1 for non-write outputs)

	// MessageUUID is the transcript entry that produced this output, so the
	// browser can jump from the digest row back into the conversation. Empty
	// when the output was discovered on disk rather than in the transcript
	// (scratchpad files, memory notes with no recorded write).
	MessageUUID string

	// Ref carries PR/Jira/artifact status for ref-kinded outputs. Nil otherwise.
	Ref *SessionRef
}

// outputKindOrder ranks kinds for display: the durable, high-signal results
// (what shipped) come before the working material (what was edited on the way).
var outputKindOrder = map[OutputKind]int{
	OutputPR:         0,
	OutputJira:       1,
	OutputArtifact:   2,
	OutputPlan:       3,
	OutputMemory:     4,
	OutputChange:     5,
	OutputScratchpad: 6,
}

// SortOutputs orders outputs by kind, then most-recent-first within a kind.
func SortOutputs(outs []SessionOutput) {
	sort.SliceStable(outs, func(i, j int) bool {
		ki, kj := outputKindOrder[outs[i].Kind], outputKindOrder[outs[j].Kind]
		if ki != kj {
			return ki < kj
		}
		if !outs[i].Last.Equal(outs[j].Last) {
			return outs[i].Last.After(outs[j].Last)
		}
		return outs[i].Title < outs[j].Title
	})
}

// CollectSessionOutputs returns everything the session produced *except*
// references (PR/Jira/artifact), which the caller composes from Session.Refs so
// the existing async extract/resolve pipeline keeps owning their status.
//
// It joins a transcript scan with on-disk state: plan files under
// ~/.claude/plans, the project's memory notes, and the session's scratchpad
// directory. Errors are absorbed: a session whose transcript cannot be read
// still reports whatever the filesystem knows about.
//
// This is I/O- and CPU-bound on large transcripts (multi-megabyte sessions are
// routine), so callers must run it off the UI thread.
func CollectSessionOutputs(sess Session, home string) []SessionOutput {
	outs := collectTranscriptOutputs(sess.FilePath, home)
	outs = append(outs, collectScratchpadOutputs(sess)...)
	outs = mergeMemoryDescriptions(outs, sess, home)
	SortOutputs(outs)
	return outs
}

// RefOutput converts a resolved reference into a SessionOutput so refs and
// on-disk outputs render as one list. Artifacts keep their description as the
// detail line; PR/Jira carry their resolved status text.
func RefOutput(r SessionRef) SessionOutput {
	kind := OutputArtifact
	switch r.Kind {
	case RefPR:
		kind = OutputPR
	case RefJira:
		kind = OutputJira
	}
	detail := RefStatusText(r)
	title := r.Label
	if r.Kind == RefArtifact && r.Title != "" {
		// An artifact's UUID label says nothing; its description is the name a
		// person would recognize, so it leads and the id becomes the detail.
		title, detail = r.Title, r.Label
	}
	ref := r
	return SessionOutput{
		Kind:        kind,
		Title:       title,
		Detail:      detail,
		URL:         r.URL,
		First:       r.FirstSeen,
		Last:        r.FirstSeen,
		Count:       1,
		MessageUUID: r.FirstSeenUUID,
		Ref:         &ref,
	}
}

// collectTranscriptOutputs scans the transcript for plan writes and file
// changes. Like ExtractSessionRefsFromFile it deliberately avoids
// LoadMessages/ParseEntry: fully unmarshaling every entry of a multi-megabyte
// transcript to find a handful of tool_use blocks cost ~2.2s on a 100MB
// session. Instead we scan raw lines, skip any line without a write-tool
// marker, and only decode the survivors.
func collectTranscriptOutputs(filePath, home string) []SessionOutput {
	f, err := os.Open(filePath)
	if err != nil {
		return nil
	}
	defer f.Close()

	byKey := make(map[string]*SessionOutput)
	remember := func(kind OutputKind, key, title, detail, path string, ts time.Time, uuid string) {
		k := string(kind) + "\x00" + key
		o, ok := byKey[k]
		if !ok {
			o = &SessionOutput{
				Kind: kind, Title: title, Detail: detail, Path: path,
				First: ts, Last: ts, MessageUUID: uuid,
			}
			byKey[k] = o
		}
		o.Count++
		if ts.IsZero() {
			return
		}
		if o.First.IsZero() || ts.Before(o.First) {
			o.First = ts
			// The jump target is the *first* time the session produced this
			// output — that is where the decision to write it was made.
			o.MessageUUID = uuid
		}
		if ts.After(o.Last) {
			o.Last = ts
		}
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 10*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if !hasOutputToolMarker(line) {
			continue
		}
		entry, err := ParseEntry(string(line))
		if err != nil {
			continue
		}
		for _, b := range entry.Content {
			if b.Type != "tool_use" || b.ToolInput == "" {
				continue
			}
			if b.ToolName == "ExitPlanMode" {
				var in struct {
					PlanFilePath string `json:"planFilePath"`
				}
				if json.Unmarshal([]byte(b.ToolInput), &in) != nil || in.PlanFilePath == "" {
					continue
				}
				slug := strings.TrimSuffix(baseName(in.PlanFilePath), ".md")
				remember(OutputPlan, in.PlanFilePath, slug, ShortenPath(in.PlanFilePath, home), in.PlanFilePath, entry.Timestamp, entry.UUID)
				continue
			}
			field, ok := changeTools[b.ToolName]
			if !ok {
				continue
			}
			path := jsonStringField(b.ToolInput, field)
			if path == "" {
				continue
			}
			kind := OutputChange
			if isMemoryPath(path) {
				kind = OutputMemory
			}
			remember(kind, path, baseName(path), ShortenPath(path, home), path, entry.Timestamp, entry.UUID)
		}
	}

	outs := make([]SessionOutput, 0, len(byKey))
	for _, o := range byKey {
		outs = append(outs, *o)
	}
	return outs
}

// outputToolMarkers are the JSON needles that make a raw line worth decoding:
// the write tools plus ExitPlanMode. A line without any of them cannot carry an
// output, so it never pays for a JSON unmarshal.
var outputToolMarkers = [][]byte{
	[]byte(`"name":"Edit"`),
	[]byte(`"name":"MultiEdit"`),
	[]byte(`"name":"Write"`),
	[]byte(`"name":"NotebookEdit"`),
	[]byte(`"name":"ExitPlanMode"`),
}

func hasOutputToolMarker(line []byte) bool {
	for _, m := range outputToolMarkers {
		if bytes.Contains(line, m) {
			return true
		}
	}
	return false
}

// collectScratchpadOutputs lists the session's scratchpad files. Unlike the
// scratchpad preview we do not read bodies here — the digest only needs name,
// size and mtime, and a large scratchpad would otherwise cost megabytes per
// navigation.
func collectScratchpadOutputs(sess Session) []SessionOutput {
	files := LoadScratchpadFiles(sess.ProjectPath, sess.ID)
	outs := make([]SessionOutput, 0, len(files))
	for _, f := range files {
		mt := time.Unix(f.ModTime, 0)
		outs = append(outs, SessionOutput{
			Kind:   OutputScratchpad,
			Title:  f.Name,
			Detail: humanBytes(f.Size),
			Path:   f.Path,
			First:  mt,
			Last:   mt,
			Count:  1,
		})
	}
	return outs
}

// mergeMemoryDescriptions replaces a memory output's raw path detail with the
// note's frontmatter description, which is what makes a memory row readable.
// Notes the session wrote but that no longer exist on disk keep the path.
func mergeMemoryDescriptions(outs []SessionOutput, sess Session, home string) []SessionOutput {
	hasMemory := false
	for _, o := range outs {
		if o.Kind == OutputMemory {
			hasMemory = true
			break
		}
	}
	if !hasMemory {
		return outs
	}
	notes := LoadMemoryNotes(sess.ProjectPath, home)
	if len(notes) == 0 {
		return outs
	}
	byFile := make(map[string]MemoryNote, len(notes))
	for _, n := range notes {
		byFile[n.FileName] = n
	}
	for i := range outs {
		if outs[i].Kind != OutputMemory {
			continue
		}
		n, ok := byFile[outs[i].Title]
		if !ok {
			continue
		}
		if n.Name != "" {
			outs[i].Title = n.Name
		}
		if n.Description != "" {
			outs[i].Detail = n.Description
		}
	}
	return outs
}

// PlanFileOutputs lists plan files recorded on the session by the scanner
// (PlanSlugs) that the transcript walk did not already surface. A resumed
// session inherits its parent's plan slug without re-running ExitPlanMode, so
// without this the plan it is actually working from would be invisible.
func PlanFileOutputs(sess Session, home string, have []SessionOutput) []SessionOutput {
	if len(sess.PlanSlugs) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(have))
	for _, o := range have {
		if o.Kind == OutputPlan {
			seen[strings.TrimSuffix(baseName(o.Path), ".md")] = true
		}
	}
	var outs []SessionOutput
	for _, slug := range sess.PlanSlugs {
		if slug == "" || seen[slug] {
			continue
		}
		path := filepath.Join(home, ".claude", "plans", slug+".md")
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		outs = append(outs, SessionOutput{
			Kind:   OutputPlan,
			Title:  slug,
			Detail: ShortenPath(path, home),
			Path:   path,
			First:  info.ModTime(),
			Last:   info.ModTime(),
			Count:  1,
		})
	}
	return outs
}

// humanBytes formats a byte count compactly (e.g. "1.2 KB").
func humanBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}
