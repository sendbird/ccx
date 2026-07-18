package session

import (
	"encoding/json"
	"regexp"
	"strings"
)

type shellInputBash struct {
	Command                   string      `json:"command"`
	Description               string      `json:"description"`
	RunInBackground           bool        `json:"run_in_background"`
	Timeout                   json.Number `json:"timeout"`
	DangerouslyDisableSandbox bool        `json:"dangerouslyDisableSandbox"`
}

type shellInputMonitor struct {
	Command                   string      `json:"command"`
	Description               string      `json:"description"`
	Persistent                bool        `json:"persistent"`
	TimeoutMS                 json.Number `json:"timeout_ms"`
	DangerouslyDisableSandbox bool        `json:"dangerouslyDisableSandbox"`
}

type shellInputResult struct {
	ToolUseID string `json:"tool_use_id"`
}

// ActiveShellJobs returns the subset of the session's recorded shell jobs
// that look like they are still alive: a `Monitor` invocation that hasn't
// been killed, or a background `Bash` whose latest poll did not return
// completion. We can't observe child process state from a JSONL alone, so
// this is a heuristic — but it matches what we render as `[BG]`.
func (s Session) ActiveShellJobs() []ShellJob {
	if !s.IsLive || !s.HasShellJobs {
		return nil
	}
	var out []ShellJob
	for _, j := range s.ShellJobs {
		switch j.Status {
		case "killed", "stopped", "completed", "failed":
			continue
		}
		out = append(out, j)
	}
	return out
}

// ActiveMonitorCount returns how many `Monitor` tool invocations look
// active for this session — useful for showing a count next to the [BG]
// badge in the session list.
func (s Session) ActiveMonitorCount() int {
	n := 0
	for _, j := range s.ActiveShellJobs() {
		if j.ToolName == "Monitor" {
			n++
		}
	}
	return n
}

// ActiveBashJobCount returns how many backgrounded Bash calls look active.
func (s Session) ActiveBashJobCount() int {
	n := 0
	for _, j := range s.ActiveShellJobs() {
		if j.ToolName == "Bash" {
			n++
		}
	}
	return n
}

// MonitorInputSummary extracts a short human label from a Monitor tool_use
// input JSON: its description (preferred) or, failing that, the first line of
// its command. Also reports whether the monitor is persistent. Returns
// ok=false when the input can't be parsed.
func MonitorInputSummary(toolInput string) (desc string, persistent, ok bool) {
	var in shellInputMonitor
	if err := json.Unmarshal([]byte(toolInput), &in); err != nil {
		return "", false, false
	}
	desc = in.Description
	if desc == "" {
		desc = firstLine(in.Command)
	}
	return desc, in.Persistent, true
}

func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}

// LoadShellJobs reads a session's JSONL and returns its shell/monitor jobs.
// Convenience wrapper over LoadMessages + LoadShellJobsFromEntries.
func LoadShellJobs(filePath string) []ShellJob {
	entries, err := LoadMessages(filePath)
	if err != nil {
		return nil
	}
	return LoadShellJobsFromEntries(entries)
}

// EnrichLiveSessions fills the per-session runtime detail that only matters for
// live sessions and is too expensive to compute for every session during the
// fast scan: the ShellJobs list (for active-monitor counts) and the
// AwaitingInput flag (unanswered AskUserQuestion). Non-live sessions are left
// untouched. One JSONL read per live session; the live set is small.
func EnrichLiveSessions(sessions []Session) {
	for i := range sessions {
		if !sessions[i].IsLive {
			continue
		}
		if sessions[i].FilePath == "" {
			continue
		}
		entries, err := LoadMessages(sessions[i].FilePath)
		if err != nil {
			continue
		}
		if sessions[i].HasShellJobs {
			sessions[i].ShellJobs = LoadShellJobsFromEntries(entries)
		}
		sessions[i].AwaitingInput = AwaitingUserInput(entries)
	}
}

// AwaitingUserInput reports whether the session's last tool interaction is an
// unanswered AskUserQuestion — i.e. Claude asked the user a question and is
// blocked waiting for the answer. Detected purely from the JSONL: the most
// recent AskUserQuestion tool_use has no matching tool_result. Only meaningful
// for live sessions.
func AwaitingUserInput(entries []Entry) bool {
	// Find the last AskUserQuestion tool_use and collect all tool_result IDs
	// that appear after it.
	lastAskID := ""
	lastAskIdx := -1
	for i := range entries {
		for _, b := range entries[i].Content {
			if b.Type == "tool_use" && b.ToolName == "AskUserQuestion" {
				lastAskID = b.ID
				lastAskIdx = i
			}
		}
	}
	if lastAskIdx < 0 || lastAskID == "" {
		return false
	}
	// Look for a tool_result referencing that ID at or after the ask.
	for i := lastAskIdx; i < len(entries); i++ {
		for _, b := range entries[i].Content {
			if b.Type == "tool_result" && b.ID == lastAskID {
				return false // answered
			}
		}
	}
	return true
}

// LoadShellJobsFromEntries scans parsed entries for background Bash and Monitor
// tool invocations. It correlates BashOutput/KillShell calls (which carry a
// tool_use_id) back to the originating shell so we can show how many polls
// happened and whether the shell was explicitly killed. It also inspects
// BashOutput tool_result content and <task-notification> blocks for completion
// records, promoting a job's status to "completed" or "failed" when the shell
// verifiably exited. When no completion signal is detectable, statuses stay at
// the conservative "running"/"polled"/"killed"/"stopped" set.
//
// Entries are assumed to be in chronological order, matching how the JSONL is
// stored on disk.
func LoadShellJobsFromEntries(entries []Entry) []ShellJob {
	var jobs []ShellJob
	byID := make(map[string]int)         // shell tool_use ID → index in jobs
	pollShell := make(map[string]string) // BashOutput tool_use ID → shell tool_use ID

	for _, e := range entries {
		for _, b := range e.Content {
			switch b.Type {
			case "tool_result":
				// A BashOutput result may carry an explicit shell exit status.
				shellID, ok := pollShell[b.ID]
				if !ok {
					continue
				}
				idx, ok := byID[shellID]
				if !ok {
					continue
				}
				if st := shellResultStatus(b.Text); st != "" {
					jobs[idx].Status = st
					if !e.Timestamp.IsZero() {
						jobs[idx].LastEventAt = e.Timestamp
					}
				}
				continue

			case "text", "system_tag":
				// task-notification blocks record background-shell completion:
				// <tool-use-id>toolu_…</tool-use-id> … <status>completed|failed</status>
				if strings.Contains(b.Text, "tool-use-id") {
					applyTaskNotification(b.Text, e, byID, jobs)
				}
				continue

			case "tool_use":
				// handled below
			default:
				continue
			}

			switch b.ToolName {
			case "Bash":
				var in shellInputBash
				if err := json.Unmarshal([]byte(b.ToolInput), &in); err != nil {
					continue
				}
				if !in.RunInBackground {
					continue
				}
				timeout, _ := in.Timeout.Int64()
				job := ShellJob{
					ID:                        b.ID,
					ToolName:                  "Bash",
					Command:                   in.Command,
					Description:               in.Description,
					TimeoutMS:                 int(timeout),
					DangerouslyDisableSandbox: in.DangerouslyDisableSandbox,
					StartedAt:                 e.Timestamp,
					LastEventAt:               e.Timestamp,
					Status:                    "running",
				}
				if b.ID != "" {
					byID[b.ID] = len(jobs)
				}
				jobs = append(jobs, job)

			case "Monitor":
				var in shellInputMonitor
				if err := json.Unmarshal([]byte(b.ToolInput), &in); err != nil {
					continue
				}
				timeout, _ := in.TimeoutMS.Int64()
				job := ShellJob{
					ID:                        b.ID,
					ToolName:                  "Monitor",
					Command:                   in.Command,
					Description:               in.Description,
					Persistent:                in.Persistent,
					TimeoutMS:                 int(timeout),
					DangerouslyDisableSandbox: in.DangerouslyDisableSandbox,
					StartedAt:                 e.Timestamp,
					LastEventAt:               e.Timestamp,
					Status:                    "running",
				}
				if b.ID != "" {
					byID[b.ID] = len(jobs)
				}
				jobs = append(jobs, job)

			case "BashOutput":
				var in shellInputResult
				if err := json.Unmarshal([]byte(b.ToolInput), &in); err != nil {
					continue
				}
				if idx, ok := byID[in.ToolUseID]; ok {
					if b.ID != "" {
						pollShell[b.ID] = in.ToolUseID
					}
					jobs[idx].PollCount++
					if !e.Timestamp.IsZero() {
						jobs[idx].LastEventAt = e.Timestamp
					}
					if jobs[idx].Status == "running" {
						jobs[idx].Status = "polled"
					}
				}

			case "KillShell":
				var in shellInputResult
				if err := json.Unmarshal([]byte(b.ToolInput), &in); err != nil {
					continue
				}
				if idx, ok := byID[in.ToolUseID]; ok {
					if !e.Timestamp.IsZero() {
						jobs[idx].LastEventAt = e.Timestamp
					}
					jobs[idx].Status = "killed"
				}
			}
		}
	}

	return jobs
}

// shellStatusRe matches an explicit shell exit status embedded in a result or
// notification body, e.g. "<status>completed</status>".
var shellStatusRe = regexp.MustCompile(`<status>(completed|failed|stopped)</status>`)

// shellToolUseIDRe pulls the <tool-use-id> out of a task-notification body.
var shellToolUseIDRe = regexp.MustCompile(`<tool-use-id>([^<]+)</tool-use-id>`)

// shellResultStatus inspects a BashOutput tool_result body for an explicit
// completion record. Returns "completed"/"failed"/"stopped" when the shell
// verifiably exited, or "" when undetectable (keep the current status).
func shellResultStatus(text string) string {
	if text == "" {
		return ""
	}
	if m := shellStatusRe.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	// Prose form: "exited with code N" / "completed (exit code N)".
	if strings.Contains(text, "exit code 0") || strings.Contains(text, "exited with code 0") {
		return "completed"
	}
	if strings.Contains(text, "exited with code") || strings.Contains(text, "(exit code") {
		return "failed"
	}
	return ""
}

// applyTaskNotification parses a <task-notification> body carrying a
// <tool-use-id> back-reference to a background shell and promotes that job's
// status when the notification records completion or failure.
func applyTaskNotification(text string, e Entry, byID map[string]int, jobs []ShellJob) {
	idm := shellToolUseIDRe.FindStringSubmatch(text)
	if idm == nil {
		return
	}
	idx, ok := byID[idm[1]]
	if !ok {
		return
	}
	st := shellStatusRe.FindStringSubmatch(text)
	if st == nil {
		return
	}
	// "stopped" from a notification is already covered by the conservative
	// status set; completed/failed are the new verifiable states.
	jobs[idx].Status = st[1]
	if !e.Timestamp.IsZero() {
		jobs[idx].LastEventAt = e.Timestamp
	}
}
