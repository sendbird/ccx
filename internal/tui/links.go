package tui

import (
	tea "github.com/charmbracelet/bubbletea"
	"github.com/keyolk/tuikit/link"
	"github.com/sendbird/ccx/internal/opener"
	"github.com/sendbird/ccx/internal/session"
)

// UseOpenerForLinks makes the open-link key (link.Open) honor the user's
// open.command_template, as every other ccx open path does. Without a
// template the kit's own opener stays: it is the same OS default ccx uses,
// and it also refuses to open a browser on the remote host over SSH.
//
// link.Launcher is process-wide, so this is called once at startup from the
// config ccx runs with, never from NewApp: tests stub link.Launcher and must
// not have it replaced by whatever config the machine has.
func UseOpenerForLinks(cfg opener.Config) {
	if cfg.CommandTemplate == "" {
		return
	}
	link.Launcher = func(u string) error { return opener.Open(cfg, u) }
}

// sessionLinkURLs is the session's PR, Jira and artifact URLs, best first
// (session.sortRefs order). Refs already on the session are used as they are;
// otherwise they are extracted from the transcript, offline (no status
// lookup), so this is safe to run inside a command.
func sessionLinkURLs(sess session.Session) []string {
	refs := sess.Refs
	if len(refs) == 0 && sess.FilePath != "" {
		refs = session.ExtractSessionRefsFromFile(sess.FilePath)
	}
	urls := make([]string, 0, len(refs))
	for _, r := range refs {
		urls = append(urls, r.URL)
	}
	return urls
}

// openSessionLink opens the first link of sess (a session usually has one PR
// worth opening; OpenFirst says when there were more). The transcript scan
// runs in the command, off the update loop.
func openSessionLink(sess session.Session) tea.Cmd {
	return func() tea.Msg { return link.OpenFirst(sessionLinkURLs(sess))() }
}

// conversationLinkText is the text the open-link key searches for URLs in the
// conversation: the focused block of the preview when one is picked, else the
// selected message. It mirrors what the copy action copies, so "copy" and
// "open link" act on the same thing.
func (a *App) conversationLinkText() string {
	if fs := a.conv.split.Folds; fs != nil && fs.BlockCursor >= 0 && fs.BlockCursor < len(fs.Entry.Content) {
		if text := blockPlainText(fs.Entry.Content[fs.BlockCursor]); text != "" {
			return text
		}
	}
	item, ok := a.selectedConversationItem()
	if !ok {
		return ""
	}
	switch item.kind {
	case convMsg:
		return entryFullText(item.merged.entry)
	case convAgent:
		return entryFullText(buildAgentPreviewEntry(item.agent))
	}
	return ""
}

// openConversationLink opens the first URL in the selected message or block.
func (a *App) openConversationLink() tea.Cmd {
	return link.OpenFirst(link.Find(a.conversationLinkText()))
}

// reportOpened shows the result of the open-link key in the footer toast.
func (a *App) reportOpened(msg link.OpenedMsg) {
	a.flash(msg.NoteWith(ccxLinkNotes))
}

// ccxLinkNotes is the kit's toast text in ccx's capitalized toast wording
// ("Opened …", "Copied …").
var ccxLinkNotes = link.Notes{
	Opened:      "Opened %s",
	OpenedFirst: "Opened %s (the first of %d links)",
	Failed:      "Open failed: %s",
	None:        "No link here",
}
