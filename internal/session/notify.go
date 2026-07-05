package session

import "time"

// NotifyEvent is a notable lifecycle transition for one session, suitable for
// surfacing in a fleet notification inbox. It is produced by diffing successive
// scans: e.g. a live session that just finished all its work (→ Done) or that
// went idle with unfinished work (→ Wait) is something the user running many
// parallel tmux sessions wants to be told about.
type NotifyEvent struct {
	SessionID   string
	ProjectName string
	From        LifecycleState
	To          LifecycleState
	At          time.Time
}

// notifyWorthy reports whether a transition into `to` is worth surfacing.
// Transitions into "attention" states (Wait/Done/Stuck) are notable; entering
// Busy/BG/None is routine churn and is ignored.
func notifyWorthy(to LifecycleState) bool {
	switch to {
	case LifecycleWait, LifecycleDone, LifecycleStuck:
		return true
	default:
		return false
	}
}

// DiffLifecycle compares previous per-session lifecycle states against the
// current sessions and returns the notable transitions. prev maps session ID →
// its lifecycle at the last scan; it is UPDATED in place to the current states
// so the caller can feed it back next tick. now stamps the emitted events.
//
// A transition fires only when the state actually changed AND the new state is
// notifyWorthy — so a session sitting in Wait across many ticks fires once, not
// every tick. Brand-new sessions (absent from prev) that appear already in a
// worthy state also fire once.
func DiffLifecycle(prev map[string]LifecycleState, sessions []Session, now time.Time) []NotifyEvent {
	var events []NotifyEvent
	seen := make(map[string]bool, len(sessions))

	for _, s := range sessions {
		seen[s.ID] = true
		cur := s.Lifecycle()
		old, existed := prev[s.ID]
		prev[s.ID] = cur

		if existed && old == cur {
			continue // no change
		}
		if !notifyWorthy(cur) {
			continue
		}
		from := old
		if !existed {
			from = LifecycleNone
		}
		events = append(events, NotifyEvent{
			SessionID:   s.ID,
			ProjectName: s.ProjectName,
			From:        from,
			To:          cur,
			At:          now,
		})
	}

	// Drop states for sessions that disappeared so prev doesn't grow unbounded.
	for id := range prev {
		if !seen[id] {
			delete(prev, id)
		}
	}
	return events
}

// LifecycleLabel returns a short human label for a lifecycle state, matching the
// badge vocabulary used in the session browser.
func LifecycleLabel(s LifecycleState) string {
	switch s {
	case LifecycleBusy:
		return "BUSY"
	case LifecycleBG:
		return "BG"
	case LifecycleStuck:
		return "STUCK"
	case LifecycleWait:
		return "WAIT"
	case LifecycleDone:
		return "DONE"
	default:
		return ""
	}
}
