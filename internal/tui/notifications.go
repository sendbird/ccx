package tui

import (
	"fmt"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

// maxNotifyInbox bounds the fleet notification inbox; oldest events drop first.
const maxNotifyInbox = 200

// collectNotifications diffs the current session lifecycle states against the
// previously seen states and appends any notable transitions (→ Wait/Done/
// Stuck) to the inbox. Called on each tick after live state is refreshed.
func (a *App) collectNotifications() {
	if a.notifyPrev == nil {
		a.notifyPrev = make(map[string]session.LifecycleState)
	}
	events := session.DiffLifecycle(a.notifyPrev, a.sessions, time.Now())
	if len(events) == 0 {
		return
	}
	a.notifyInbox = append(a.notifyInbox, events...)
	if len(a.notifyInbox) > maxNotifyInbox {
		a.notifyInbox = a.notifyInbox[len(a.notifyInbox)-maxNotifyInbox:]
	}
}

// notifyUnreadCount returns the number of queued notifications.
func (a *App) notifyUnreadCount() int {
	return len(a.notifyInbox)
}

// dismissNotifications clears the inbox.
func (a *App) dismissNotifications() {
	a.notifyInbox = nil
}

// latestNotification returns the most recent notification, if any.
func (a *App) latestNotification() (session.NotifyEvent, bool) {
	if len(a.notifyInbox) == 0 {
		return session.NotifyEvent{}, false
	}
	return a.notifyInbox[len(a.notifyInbox)-1], true
}

// notifyIndicator returns a short status-bar fragment like "(!)3" when there are
// unread fleet notifications, or "" when the inbox is empty.
func (a *App) notifyIndicator() string {
	n := a.notifyUnreadCount()
	if n == 0 {
		return ""
	}
	return notifyBadgeStyle.Render("(!)") + notifyCountStyle.Render(fmt.Sprintf("%d", n))
}

// jumpToNotification moves the session-list cursor to the most recent
// notification's session and clears the inbox. Returns false when there is
// nothing to jump to (empty inbox or session not currently visible).
func (a *App) jumpToNotification() bool {
	ev, ok := a.latestNotification()
	if !ok {
		return false
	}
	for i, item := range a.sessionList.VisibleItems() {
		si, ok := item.(sessionItem)
		if !ok || si.sess.ID != ev.SessionID {
			continue
		}
		a.sessionList.Select(i)
		a.dismissNotifications()
		return true
	}
	// Session not visible (filtered out or in a collapsed project): still clear
	// the inbox so the indicator doesn't stick forever.
	a.dismissNotifications()
	return false
}
