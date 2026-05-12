package session

import "time"

// LifecycleState describes the current high-level state of a session.
//
//   - LifecycleBusy:  Claude is actively responding (file mtime within ~10s).
//   - LifecycleBG:    A background shell, Monitor, or active cron is in flight.
//   - LifecycleStuck: Live process exists but file is stale beyond stuckThreshold
//     while there is still unfinished work to do.
//   - LifecycleWait:  Live, idle, and has unfinished todos/tasks — waiting for
//     the user to continue.
//   - LifecycleDone:  Session had todos/tasks and they are all completed.
//   - LifecycleNone:  No notable state to surface.
type LifecycleState int

const (
	LifecycleNone LifecycleState = iota
	LifecycleDone
	LifecycleWait
	LifecycleStuck
	LifecycleBG
	LifecycleBusy
)

// stuckThreshold is how stale a live session's mtime must be before we call it
// stuck. Anything within this window is considered just "idle/waiting".
const stuckThreshold = 30 * time.Minute

// Lifecycle returns the single highest-priority lifecycle state for the
// session. The mapping is intentionally mutually exclusive: at most one of
// [BG]/[WAIT]/[DONE]/[STUCK] is rendered in addition to [LIVE]/[BUSY].
func (s Session) Lifecycle() LifecycleState {
	if s.IsResponding {
		return LifecycleBusy
	}
	if s.hasActiveBG() {
		return LifecycleBG
	}
	if s.IsLive && s.isStaleStuck() {
		return LifecycleStuck
	}
	if s.IsLive && s.hasUnfinishedWork() {
		return LifecycleWait
	}
	if s.allWorkCompleted() {
		return LifecycleDone
	}
	return LifecycleNone
}

// hasActiveBG reports whether the session has background work that is still
// expected to be running. We treat any session that contains a shell or
// Monitor invocation as "BG-capable" while the Claude process is live, and we
// treat active (not-yet-deleted) crons as BG regardless of liveness because
// crons fire on their own schedule.
func (s Session) hasActiveBG() bool {
	if s.IsLive && s.HasShellJobs {
		return true
	}
	for _, c := range s.Crons {
		if c.Status == "active" {
			return true
		}
	}
	return false
}

// hasUnfinishedWork reports whether the session has any todo or task in a
// non-completed state.
func (s Session) hasUnfinishedWork() bool {
	for _, t := range s.Todos {
		if t.Status == "pending" || t.Status == "in_progress" {
			return true
		}
	}
	for _, t := range s.Tasks {
		if t.Status == "pending" || t.Status == "in_progress" {
			return true
		}
	}
	return false
}

// allWorkCompleted reports whether the session had todos or tasks AND all of
// them are completed. Empty sessions (no todos/tasks recorded) are not
// considered "done" — we don't have enough signal to say so.
func (s Session) allWorkCompleted() bool {
	if len(s.Todos) == 0 && len(s.Tasks) == 0 {
		return false
	}
	for _, t := range s.Todos {
		if t.Status != "completed" {
			return false
		}
	}
	for _, t := range s.Tasks {
		if t.Status != "completed" {
			return false
		}
	}
	return true
}

// isStaleStuck reports whether a live session has not updated its JSONL for
// long enough that we suspect it is stuck — but only if there is still
// unfinished work to do. A live session sitting idle with nothing pending is
// not stuck; it is simply waiting for the next prompt.
func (s Session) isStaleStuck() bool {
	if s.ModTime.IsZero() {
		return false
	}
	if time.Since(s.ModTime) < stuckThreshold {
		return false
	}
	return s.hasUnfinishedWork()
}
