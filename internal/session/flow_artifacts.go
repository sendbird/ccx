package session

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"time"
)

// ArtifactKind classifies one artifact occurrence extracted from a transcript.
type ArtifactKind string

const (
	ArtifactRef      ArtifactKind = "ref"      // PR / Jira reference (Data: SessionRef)
	ArtifactURL      ArtifactKind = "url"      // generic URL (non-ref)
	ArtifactImage    ArtifactKind = "image"    // pasted image (Data: paste ID int)
	ArtifactChange   ArtifactKind = "change"   // Edit/Write/NotebookEdit occurrence (Data: ChangeData)
	ArtifactFile     ArtifactKind = "file"     // file referenced by a tool (incl. Read)
	ArtifactTask     ArtifactKind = "task"     // TaskCreate / TaskUpdate (Data: TaskEventData)
	ArtifactTodo     ArtifactKind = "todo"     // TodoWrite item occurrence (Data: TodoItem)
	ArtifactPlan     ArtifactKind = "plan"     // ExitPlanMode plan written (Data: PlanData)
	ArtifactHook     ArtifactKind = "hook"     // hook firing (Data: HookInfo)
	ArtifactError    ArtifactKind = "error"    // tool_result with is_error
	ArtifactCommand  ArtifactKind = "command"  // Bash invocation (Data: command string)
	ArtifactDecision ArtifactKind = "decision" // decision marker (Data: DecisionData)
)

// Artifact is one extracted occurrence. Every occurrence is stored — dedupe by
// Key happens at presentation time (DedupeArtifactsByKey).
type Artifact struct {
	ID     string // stable per-occurrence ID
	Kind   ArtifactKind
	NodeID string // owning FlowNode
	Key    string // canonical identity: URL, file path, task ID, paste ID…
	Origin ArtifactOrigin
	Data   any
}

// ArtifactOrigin is the exact provenance of one occurrence.
type ArtifactOrigin struct {
	SessionID   string
	Transcript  string // owning transcript path — NOT implicit current session
	MessageUUID string
	EntryIndex  int
	BlockIndex  int
	ToolUseID   string
	AgentID     string // owning agent (empty for parent transcript)
	WorkflowRun string
	PhaseIndex  int
	Timestamp   time.Time
}

// ChangeData describes one Edit/Write occurrence (not collapsed by path).
type ChangeData struct {
	ToolName  string
	ToolInput string
	Summary   string // e.g. "Edit: -3/+7"
}

// TaskEventData describes one TaskCreate/TaskUpdate occurrence.
type TaskEventData struct {
	ToolName string // "TaskCreate" or "TaskUpdate"
	TaskID   string
	Subject  string
	Status   string // status set by this event ("" when unchanged)
}

// PlanData describes an ExitPlanMode plan write.
type PlanData struct {
	PlanFilePath string
	Plan         string
}

// DecisionKind classifies a decision marker.
type DecisionKind string

const (
	DecisionPlan        DecisionKind = "plan"
	DecisionTask        DecisionKind = "task"
	DecisionMemory      DecisionKind = "memory"
	DecisionFirstChange DecisionKind = "first-change"
	DecisionSteering    DecisionKind = "steering"
)

// DecisionData is the payload of an ArtifactDecision occurrence.
type DecisionData struct {
	Kind  DecisionKind
	Label string // one-line marker label
	// Related points at the artifact occurrence this decision derives from
	// (plan/task/change ID); empty for steering markers.
	Related string
}

// DedupeArtifactsByKey collapses occurrences to one artifact per (Kind, Key),
// keeping the first occurrence in the given order. Presentation-side helper —
// the index itself always stores every occurrence.
func DedupeArtifactsByKey(arts []Artifact) []Artifact {
	seen := make(map[string]bool, len(arts))
	out := make([]Artifact, 0, len(arts))
	for _, a := range arts {
		k := string(a.Kind) + "\x00" + a.Key
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, a)
	}
	return out
}

// Decisions returns the session's decision markers in chronological order,
// restricted to the given scope rooted at the session (scope semantics match
// Artifacts with nodeID = root).
func (fi *FlowIndex) Decisions(scope Scope) []Artifact {
	out := fi.Artifacts(fi.RootID, ArtifactDecision, scope)
	sortArtifactsByTime(out)
	return out
}

// ---- extraction ---------------------------------------------------------

// changeTools maps modifying tool names to the JSON field holding the path.
var changeTools = map[string]string{
	"Edit":         "file_path",
	"MultiEdit":    "file_path",
	"Write":        "file_path",
	"NotebookEdit": "notebook_path",
}

// fileTools maps file-referencing tool names to their path field (superset of
// changeTools; includes read-only references).
var fileTools = map[string]string{
	"Read":         "file_path",
	"Edit":         "file_path",
	"MultiEdit":    "file_path",
	"Write":        "file_path",
	"LSP":          "filePath",
	"NotebookEdit": "notebook_path",
}

// emitTranscriptArtifacts walks one transcript's entries once and appends every
// artifact occurrence to the index. Ownership: parent-transcript artifacts are
// owned by their turn node; agent-transcript artifacts by the agent node.
func (b *flowBuilder) emitTranscriptArtifacts(path string, entries []Entry, agentID, workflowRun string, phaseIndex int) {
	// tool_use_id → tool name for error attribution within this transcript.
	toolNames := make(map[string]string)
	// tool_use_id → Artifact tool input, so the tool_result's published URL can
	// be paired with the author's label/description/file_path for display.
	artifactInputs := make(map[string]artifactToolInput)

	for i := range entries {
		e := &entries[i]
		owner := b.artifactOwner(agentID, i)
		base := ArtifactOrigin{
			SessionID:   b.sess.ID,
			Transcript:  path,
			MessageUUID: e.UUID,
			EntryIndex:  i,
			AgentID:     agentID,
			WorkflowRun: workflowRun,
			PhaseIndex:  phaseIndex,
			Timestamp:   e.Timestamp,
		}

		for bi, blk := range e.Content {
			origin := base
			origin.BlockIndex = bi

			switch blk.Type {
			case "text", "system_tag", "thinking":
				b.emitURLs(blk.Text, owner, origin)

			case "image":
				if blk.ImagePasteID > 0 {
					b.append(Artifact{
						Kind:   ArtifactImage,
						NodeID: owner,
						Key:    imageKey(path, blk.ImagePasteID),
						Origin: origin,
						Data:   blk.ImagePasteID,
					})
				}

			case "tool_use":
				origin.ToolUseID = blk.ID
				if blk.ID != "" && blk.ToolName != "" {
					toolNames[blk.ID] = blk.ToolName
				}
				if blk.ToolName == "Artifact" {
					if in, ok := parseArtifactInput(blk.ToolInput); ok {
						artifactInputs[blk.ID] = in
					}
				}
				b.emitURLs(blk.ToolInput, owner, origin)
				b.emitToolUseArtifacts(blk, owner, origin)

			case "tool_result":
				origin.ToolUseID = blk.ID
				if blk.IsError {
					b.append(Artifact{
						Kind:   ArtifactError,
						NodeID: owner,
						Key:    errorKey(toolNames[blk.ID], blk.ID),
						Origin: origin,
						Data:   toolNames[blk.ID],
					})
				}
				// An Artifact tool_result carries "Published <path> at
				// https://claude.ai/code/artifact/<uuid>". Pair that URL with the
				// tool_use input (label/description/file_path) for a useful Title.
				if in, ok := artifactInputs[blk.ID]; ok {
					b.emitArtifactRef(blk.Text, owner, origin, in)
				}
			}

			// Hooks recorded against tool_use blocks.
			for _, h := range blk.Hooks {
				ho := origin
				b.append(Artifact{
					Kind:   ArtifactHook,
					NodeID: owner,
					Key:    h.Name,
					Origin: ho,
					Data:   h,
				})
			}
		}
	}
}

// artifactOwner resolves the owning node for an artifact: the turn node for
// the parent transcript, the agent node for agent transcripts.
func (b *flowBuilder) artifactOwner(agentID string, entryIdx int) string {
	if agentID != "" {
		id := FlowAgentNodeID(agentID)
		if _, ok := b.fi.nodes[id]; ok {
			return id
		}
		return b.fi.RootID
	}
	return b.turnForEntry(entryIdx)
}

func (b *flowBuilder) append(a Artifact) {
	a.ID = artifactID(len(b.fi.artifacts))
	b.fi.artifacts = append(b.fi.artifacts, a)
}

func artifactID(i int) string { return "art:" + itoa(i) }

// ArtifactByID resolves an artifact occurrence ID (the format produced by
// artifactID and stored in DecisionData.Related) back to its artifact.
func (fi *FlowIndex) ArtifactByID(id string) (Artifact, bool) {
	digits, ok := strings.CutPrefix(id, "art:")
	if !ok || digits == "" {
		return Artifact{}, false
	}
	idx := 0
	for _, r := range digits {
		if r < '0' || r > '9' {
			return Artifact{}, false
		}
		idx = idx*10 + int(r-'0')
	}
	if idx >= len(fi.artifacts) {
		return Artifact{}, false
	}
	return fi.artifacts[idx], true
}

// itoa avoids importing strconv for one call site (fmt is already imported in
// flow.go; keep this tiny and allocation-free for hot paths).
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(buf[pos:])
}

func imageKey(transcript string, pasteID int) string {
	return transcript + "#img:" + itoa(pasteID)
}

func errorKey(toolName, toolUseID string) string {
	if toolUseID != "" {
		return toolUseID
	}
	return toolName
}

// emitURLs extracts ref and generic-URL artifacts from a text fragment.
func (b *flowBuilder) emitURLs(text, owner string, origin ArtifactOrigin) {
	if text == "" || !strings.Contains(text, "http") {
		return
	}
	for _, raw := range refURLRegex.FindAllString(text, -1) {
		u := cleanRefURL(raw)
		if u == "" {
			continue
		}
		if ref, ok := classifyRef(u); ok {
			ref.FirstSeen = origin.Timestamp
			b.append(Artifact{
				Kind:   ArtifactRef,
				NodeID: owner,
				Key:    ref.Label,
				Origin: origin,
				Data:   ref,
			})
			continue
		}
		b.append(Artifact{
			Kind:   ArtifactURL,
			NodeID: owner,
			Key:    u,
			Origin: origin,
		})
	}
}

// artifactToolInput captures the display-relevant fields of an Artifact tool_use
// so the later tool_result can pair its published URL with a human-readable title.
type artifactToolInput struct {
	label       string
	description string
	filePath    string
}

// parseArtifactInput decodes the input of an Artifact tool_use.
func parseArtifactInput(toolInput string) (artifactToolInput, bool) {
	var in struct {
		Label       string `json:"label"`
		Description string `json:"description"`
		FilePath    string `json:"file_path"`
	}
	if err := json.Unmarshal([]byte(toolInput), &in); err != nil {
		return artifactToolInput{}, false
	}
	return artifactToolInput{label: in.Label, description: in.Description, filePath: in.FilePath}, true
}

// emitArtifactRef scans an Artifact tool_result for the published claude.ai URL
// and emits one ArtifactRef (Data: SessionRef{Kind: RefArtifact}) with a Title
// derived from the tool_use input. No-op if the result lacks an artifact URL.
func (b *flowBuilder) emitArtifactRef(resultText, owner string, origin ArtifactOrigin, in artifactToolInput) {
	for _, raw := range refURLRegex.FindAllString(resultText, -1) {
		u := cleanRefURL(raw)
		if u == "" {
			continue
		}
		ref, ok := classifyRef(u)
		if !ok || ref.Kind != RefArtifact {
			continue
		}
		ref.FirstSeen = origin.Timestamp
		ref.Title = artifactTitle(in)
		b.append(Artifact{
			Kind:   ArtifactRef,
			NodeID: owner,
			Key:    ref.URL,
			Origin: origin,
			Data:   ref,
		})
	}
}

// artifactTitle picks the most useful display title for an artifact from its
// tool_use input: explicit label, then description, then file basename.
func artifactTitle(in artifactToolInput) string {
	if in.label != "" {
		return in.label
	}
	if in.description != "" {
		return in.description
	}
	if in.filePath != "" {
		return filepath.Base(in.filePath)
	}
	return ""
}

// emitToolUseArtifacts handles per-tool artifact kinds: changes, files, tasks,
// todos, plans, commands.
func (b *flowBuilder) emitToolUseArtifacts(blk ContentBlock, owner string, origin ArtifactOrigin) {
	switch blk.ToolName {
	case "Bash":
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal([]byte(blk.ToolInput), &in) == nil && in.Command != "" {
			b.append(Artifact{
				Kind:   ArtifactCommand,
				NodeID: owner,
				Key:    firstLine(in.Command),
				Origin: origin,
				Data:   in.Command,
			})
		}

	case "TodoWrite":
		var in struct {
			Todos []TodoItem `json:"todos"`
		}
		if json.Unmarshal([]byte(blk.ToolInput), &in) != nil {
			return
		}
		for _, todo := range in.Todos {
			if strings.TrimSpace(todo.Content) == "" {
				continue
			}
			b.append(Artifact{
				Kind:   ArtifactTodo,
				NodeID: owner,
				Key:    todo.Content,
				Origin: origin,
				Data:   todo,
			})
		}

	case "TaskCreate", "TaskUpdate":
		var in struct {
			ID      string `json:"id"`
			TaskID  string `json:"taskId"`
			Subject string `json:"subject"`
			Status  string `json:"status"`
		}
		if json.Unmarshal([]byte(blk.ToolInput), &in) != nil {
			return
		}
		id := in.ID
		if id == "" {
			id = in.TaskID
		}
		if id == "" && in.Subject == "" {
			return
		}
		data := TaskEventData{ToolName: blk.ToolName, TaskID: id, Subject: in.Subject, Status: in.Status}
		key := id
		if key == "" {
			key = in.Subject
		}
		taskArt := Artifact{Kind: ArtifactTask, NodeID: owner, Key: key, Origin: origin, Data: data}
		b.append(taskArt)
		// Decision: TaskCreate always; TaskUpdate only when it changes state.
		if blk.ToolName == "TaskCreate" || in.Status != "" {
			label := "task " + blk.ToolName[4:] // "Create"/"Update"
			if in.Subject != "" {
				label += ": " + in.Subject
			}
			b.append(Artifact{
				Kind:   ArtifactDecision,
				NodeID: owner,
				Key:    "task:" + key,
				Origin: origin,
				Data:   DecisionData{Kind: DecisionTask, Label: label, Related: artifactID(len(b.fi.artifacts) - 1)},
			})
		}

	case "ExitPlanMode":
		var in struct {
			Plan         string `json:"plan"`
			PlanFilePath string `json:"planFilePath"`
		}
		if json.Unmarshal([]byte(blk.ToolInput), &in) != nil {
			return
		}
		key := in.PlanFilePath
		if key == "" {
			key = "plan:" + origin.MessageUUID
		}
		b.append(Artifact{
			Kind:   ArtifactPlan,
			NodeID: owner,
			Key:    key,
			Origin: origin,
			Data:   PlanData{PlanFilePath: in.PlanFilePath, Plan: in.Plan},
		})
		b.append(Artifact{
			Kind:   ArtifactDecision,
			NodeID: owner,
			Key:    "plan:" + key,
			Origin: origin,
			Data:   DecisionData{Kind: DecisionPlan, Label: "plan written: " + planSlugFromPath(in.PlanFilePath), Related: artifactID(len(b.fi.artifacts) - 1)},
		})
	}

	// Changes (every occurrence — never collapsed by path).
	if field, ok := changeTools[blk.ToolName]; ok {
		if path := jsonStringField(blk.ToolInput, field); path != "" {
			b.append(Artifact{
				Kind:   ArtifactChange,
				NodeID: owner,
				Key:    path,
				Origin: origin,
				Data:   ChangeData{ToolName: blk.ToolName, ToolInput: blk.ToolInput},
			})
			// Memory write decision: Write/Edit under a memory/ dir or MEMORY.md.
			if isMemoryPath(path) {
				b.append(Artifact{
					Kind:   ArtifactDecision,
					NodeID: owner,
					Key:    "memory:" + path,
					Origin: origin,
					Data:   DecisionData{Kind: DecisionMemory, Label: "memory: " + baseName(path), Related: artifactID(len(b.fi.artifacts) - 1)},
				})
			}
		}
	}

	// Files referenced (superset, incl. Read).
	if field, ok := fileTools[blk.ToolName]; ok {
		if path := jsonStringField(blk.ToolInput, field); path != "" {
			b.append(Artifact{
				Kind:   ArtifactFile,
				NodeID: owner,
				Key:    path,
				Origin: origin,
				Data:   blk.ToolName,
			})
		}
	}
}

// jsonStringField extracts a top-level string field from a JSON object string.
func jsonStringField(jsonStr, field string) string {
	if jsonStr == "" {
		return ""
	}
	var m map[string]json.RawMessage
	if json.Unmarshal([]byte(jsonStr), &m) != nil {
		return ""
	}
	raw, ok := m[field]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// isMemoryPath reports whether a written file is a memory note: any path under
// a "memory/" directory or a MEMORY.md file.
func isMemoryPath(path string) bool {
	if strings.HasSuffix(path, "/MEMORY.md") || path == "MEMORY.md" {
		return true
	}
	return strings.Contains(path, "/memory/")
}

func baseName(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

func planSlugFromPath(path string) string {
	base := baseName(path)
	return strings.TrimSuffix(base, ".md")
}
