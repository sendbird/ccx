package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
)

type searchResultItem struct {
	result session.SearchResult
	// live is resolved from the session store at render time, not taken from
	// result.Session — the latter is a snapshot from when the search ran, and a
	// session can start or stop while the results are on screen.
	live bool
	here bool
}

func (i searchResultItem) FilterValue() string {
	return i.result.Snippet
}

func (i searchResultItem) Title() string {
	sess := i.result.Session
	var badges string
	if i.here {
		badges += hereBadge.Render("[HERE]")
	}
	if i.live {
		badges += liveBadge.Render("[LIVE]")
	}
	if badges != "" {
		badges += " "
	}
	return fmt.Sprintf("%s%s • %s", badges, sess.ProjectName, timeAgo(sess.ModTime))
}

func (i searchResultItem) Description() string {
	snippet := i.result.Snippet
	if len(snippet) > 100 {
		snippet = snippet[:97] + "..."
	}
	return snippet
}

type searchResultsMsg struct {
	result session.SearchResult
}

type searchDoneMsg struct{}

func (a *App) enterSearchMode() {
	a.searchActive = true
	a.searchQuery = ""
	a.searchResults = nil
	a.searchLoading = false

	ti := textinput.New()
	ti.Placeholder = "Search all sessions..."
	ti.Focus()
	ti.Width = 50
	a.searchInput = ti

	a.searchResultList = list.New(nil, list.NewDefaultDelegate(), 0, 0)
	initListBase(&a.searchResultList)
	a.searchResultList.SetFilteringEnabled(false)
}

func (a *App) exitSearchMode() {
	a.searchActive = false
	a.searchInput.Blur()
	if a.searchCancel != nil {
		a.searchCancel()
		a.searchCancel = nil
	}
}

func (a *App) executeSearch() tea.Cmd {
	query := strings.TrimSpace(a.searchInput.Value())
	if query == "" {
		return nil
	}

	if a.searchCancel != nil {
		a.searchCancel()
	}

	ctx, cancel := context.WithCancel(context.Background())
	a.searchCancel = cancel
	a.searchResults = nil
	a.searchQuery = query
	a.searchLoading = true

	sessions := make([]*session.Session, len(a.sessions))
	for i := range a.sessions {
		sessions[i] = &a.sessions[i]
	}

	parsed := session.ParseSearchQuery(query)

	return func() tea.Msg {
		results := session.SearchSessions(sessions, parsed, ctx)

		go func() {
			for result := range results {
				// Send each result as a message (will be batched by tea runtime)
				// This is a simplified approach - in production you'd batch these
				_ = result
			}
		}()

		// Collect all results synchronously for simplicity
		var allResults []session.SearchResult
		for result := range results {
			allResults = append(allResults, result)
		}

		return searchBatchMsg{results: allResults}
	}
}

type searchBatchMsg struct {
	results []session.SearchResult
}

func (a *App) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if a.searchInput.Focused() {
		switch key {
		case "enter":
			a.searchInput.Blur()
			return a, a.executeSearch()
		case "esc":
			a.exitSearchMode()
			return a, nil
		}

		var cmd tea.Cmd
		a.searchInput, cmd = a.searchInput.Update(msg)
		return a, cmd
	}

	// Results navigation
	switch key {
	case "esc", "q":
		a.exitSearchMode()
		return a, nil

	case "/":
		a.searchInput.Focus()
		return a, nil

	case "enter":
		if item, ok := a.searchResultList.SelectedItem().(searchResultItem); ok {
			a.exitSearchMode()
			a.openSearchResult(item.result)
		}
		return a, nil

	case "j", "down":
		a.searchResultList, _ = a.searchResultList.Update(msg)
		return a, nil

	case "k", "up":
		a.searchResultList, _ = a.searchResultList.Update(msg)
		return a, nil

	case a.keymap.Actions.Resume:
		// Take the hit straight into a working session: attach to the live pane
		// if one exists, otherwise resume the transcript in a tmux window.
		// resumeSession already branches on live vs not, so searching and then
		// resuming is the same action the session list offers.
		item, ok := a.searchResultList.SelectedItem().(searchResultItem)
		if !ok || item.result.Session == nil {
			return a, nil
		}
		sess, ok := a.sessionByIDFromStore(item.result.Session.ID)
		if !ok {
			// The session left the store (deleted or moved) since the search ran.
			sess = *item.result.Session
		}
		if sess.IsRemote {
			a.copiedMsg = "Use Enter to attach to remote session"
			return a, nil
		}
		a.exitSearchMode()
		return a.resumeSession(sess)
	}

	return a, nil
}

func (a *App) openSearchResult(result session.SearchResult) {
	for i, sess := range a.sessions {
		if sess.ID == result.Session.ID {
			a.sessionList.Select(i)
			a.currentSess = sess
			a.openConversation(sess)

			// Jump to the message containing the search result. Selection indices
			// are always in the list's visible coordinate space.
			targetUUID := result.Entry.UUID
			for idx, raw := range a.convList.VisibleItems() {
				item, ok := raw.(convItem)
				if !ok || item.kind != convMsg {
					continue
				}

				// Check all entries in the merged range.
				for j := item.merged.startIdx; j <= item.merged.endIdx; j++ {
					if j < len(a.conv.messages) && a.conv.messages[j].UUID == targetUUID {
						a.selectConvBody(idx)
						a.updateConvPreview()
						return
					}
				}
			}
			return
		}
	}
}

// renderSearchModal renders the cross-session search as a centered modal
// overlaid on the current screen (bg), instead of a full-screen takeover. The
// session list stays visible behind it, matching the URL/files/actions menus.
func (a *App) renderSearchModal(bg string) string {
	screenW, screenH := a.width, a.height

	// Modal geometry: ~72 cols (capped to screen), body height a fraction of
	// the screen so results scroll inside the box.
	modalW := min(72, screenW-6)
	if modalW < 30 {
		modalW = max(screenW-4, 20)
	}
	innerW := modalW - 2 // account for border+padding horizontal
	bodyMaxH := max(screenH-8, 6)

	titleStyle := statTitleStyle
	keyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
	descStyle := dimStyle

	var sb strings.Builder
	sb.WriteString(titleStyle.Render("Search Sessions") + "\n")

	// Input row. innerW is the modal's content budget (modalW minus the modal's
	// own horizontal padding). The input box itself draws a border (+2), so its
	// lipgloss Width must be innerW-2 or the box wraps and shatters the modal.
	inputBoxW := max(innerW-2, 8)
	a.searchInput.Width = max(inputBoxW-4, 8)
	inputStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(0, 1).
		Width(inputBoxW)
	sb.WriteString(inputStyle.Render(a.searchInput.View()) + "\n")

	switch {
	case a.searchLoading:
		sb.WriteString(dimStyle.Render("Searching…"))
	case a.searchQuery != "" && len(a.searchResults) == 0:
		sb.WriteString(dimStyle.Render("No results found"))
	case len(a.searchResults) > 0:
		sb.WriteString(dimStyle.Render(fmt.Sprintf("%d results", len(a.searchResults))) + "\n")
		// Reserve rows already used (title + input box(3) + count + help) so the
		// list fits inside the modal without overflowing.
		listH := max(min(len(a.searchResults), bodyMaxH-6), 3)
		a.searchResultList.SetSize(innerW, listH)
		sb.WriteString(a.searchResultList.View())
	default:
		// Empty query: compact syntax + filters help.
		sb.WriteString(dimStyle.Render("Type a query and press Enter.") + "\n\n")
		sb.WriteString(titleStyle.Render("Syntax") + "\n")
		sb.WriteString(fmt.Sprintf("%s %s\n", keyStyle.Render("word1 word2 "), descStyle.Render("AND match")))
		sb.WriteString(fmt.Sprintf("%s %s\n", keyStyle.Render("\"phrase\"    "), descStyle.Render("exact phrase")))
		sb.WriteString(fmt.Sprintf("%s %s\n", keyStyle.Render("-exclude    "), descStyle.Render("exclude term")))
		sb.WriteString("\n" + titleStyle.Render("Scopes") + "\n")
		sb.WriteString(fmt.Sprintf("%s %s\n", keyStyle.Render("user:       "), descStyle.Render("user messages only")))
		sb.WriteString(fmt.Sprintf("%s %s\n", keyStyle.Render("assistant:  "), descStyle.Render("assistant only")))
		sb.WriteString(fmt.Sprintf("%s %s", keyStyle.Render("tool:Name   "), descStyle.Render("tool calls (tool:mcp*)")))
	}

	// Help line.
	var help string
	switch {
	case a.searchInput.Focused():
		help = "enter:search  esc:close"
	case len(a.searchResults) > 0:
		help = fmt.Sprintf("↑↓/jk:nav  enter:open  %s:resume/attach  /:edit  esc:close", a.keymap.Actions.Resume)
	default:
		help = "esc:close"
	}
	sb.WriteString("\n\n" + dimStyle.Render(help))

	// Clamp body height to fit the screen.
	body := sb.String()
	bodyLines := strings.Split(body, "\n")
	if len(bodyLines) > bodyMaxH {
		bodyLines = bodyLines[:bodyMaxH]
		body = strings.Join(bodyLines, "\n")
	}

	modalStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorPrimary).
		Width(modalW).
		Padding(0, 1)
	return overlayCenter(bg, modalStyle.Render(body), screenW, screenH)
}

func (a *App) updateSearchResults(results []session.SearchResult) {
	a.searchResults = results
	a.searchLoading = false
	a.rebuildSearchResultItems()
}

// rebuildSearchResultItems re-renders the result rows against the current
// session store. Live state is read here rather than carried on the search
// result: a search snapshot goes stale the moment a session starts or exits,
// and the badge is what the user decides "jump or resume" from.
func (a *App) rebuildSearchResultItems() {
	items := make([]list.Item, len(a.searchResults))
	for i, r := range a.searchResults {
		item := searchResultItem{result: r}
		if r.Session != nil {
			if fresh, ok := a.sessionByIDFromStore(r.Session.ID); ok {
				item.live = fresh.IsLive
				item.here = fresh.IsCurrentWindow
			}
		}
		items[i] = item
	}
	idx := a.searchResultList.Index()
	a.searchResultList.SetItems(items)
	if idx > 0 && idx < len(items) {
		a.searchResultList.Select(idx)
	}
}
