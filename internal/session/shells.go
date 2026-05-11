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
