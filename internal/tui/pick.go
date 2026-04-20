package tui

import "github.com/sendbird/ccx/internal/session"

// PickResult is a sum-type sentinel for results returned by the TUI when
// launched in pick mode. Concrete implementations live alongside the entity
// types they represent; new entities (entries, URLs, files, …) append a new
// variant + one case arm in the JSON emitter.
type PickResult interface {
	isPickResult()
}

// SessionsResult is the PickResult variant emitted by `ccx pick session`.
type SessionsResult struct {
	Items []session.Session
}

func (SessionsResult) isPickResult() {}
