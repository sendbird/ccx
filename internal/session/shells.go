package session

import (
	"encoding/json"
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
		case "killed", "stopped":
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

// LoadShellJobsFromEntries scans parsed entries for background Bash and Monitor
// tool invocations. It correlates BashOutput/KillShell calls (which carry a
// tool_use_id) back to the originating shell so we can show how many polls
// happened and whether the shell was explicitly killed.
//
// Entries are assumed to be in chronological order, matching how the JSONL is
// stored on disk.
func LoadShellJobsFromEntries(entries []Entry) []ShellJob {
	var jobs []ShellJob
	byID := make(map[string]int) // tool_use ID → index in jobs

	for _, e := range entries {
		for _, b := range e.Content {
			if b.Type != "tool_use" {
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
