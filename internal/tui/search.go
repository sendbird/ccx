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
}

func (i searchResultItem) FilterValue() string {
	return i.result.Snippet
}

func (i searchResultItem) Title() string {
	sess := i.result.Session
	return fmt.Sprintf("%s • %s", sess.ProjectName, timeAgo(sess.ModTime))
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
	// Drop any highlight carried over from a previous jump; the next result
	// picked from this session sets its own.
	a.convHighlightTerms = nil

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

	// Open the index on the main loop, not inside the command: the command runs
	// on its own goroutine and must not mutate App state.
	if a.contentIndex == nil {
		if ix, err := session.OpenIndex(a.config.ClaudeDir); err == nil {
			a.contentIndex = ix
		}
	}
	ix := a.contentIndex

	return func() tea.Msg {
		// Bring the index up to date first: only transcripts whose mtime or
		// size moved are re-read, so the steady-state cost is a stat per
		// session. A failure here is not fatal — SearchWithIndex falls back to
		// the full scan when the index is nil or unusable.
		if ix != nil {
			if _, err := ix.Sync(ctx, sessions, nil); err != nil && ctx.Err() != nil {
				return searchBatchMsg{}
			}
		}

		results, mode, err := session.SearchWithIndex(ctx, ix, sessions, parsed, searchResultLimit)
		if err != nil {
			return searchBatchMsg{}
		}
		return searchBatchMsg{results: results, mode: mode}
	}
}

// searchResultLimit caps how many hits are hydrated and shown. A broad query
// can match tens of thousands of blocks; past the first few hundred the list is
// no longer something a person scrolls, and building them all costs real time.
const searchResultLimit = 500

type searchBatchMsg struct {
	results []session.SearchResult
	mode    session.SearchMode
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
	}

	return a, nil
}

func (a *App) openSearchResult(result session.SearchResult) {
	// The query's text terms, kept so the match the user picked stays visible in
	// the conversation they land on.
	parsed := session.ParseSearchQuery(a.searchQuery)
	terms := append(append([]string(nil), parsed.Terms...), parsed.Phrases...)

	for i, sess := range a.sessions {
		if sess.ID == result.Session.ID {
			a.sessionList.Select(i)
			a.currentSess = sess
			a.openConversation(sess)

			// Set after openConversation, which clears the highlight so that the
			// other entry points cannot inherit a stale query.
			a.convHighlightTerms = terms
			if a.conv.split.Folds != nil {
				a.conv.split.Folds.ExtraHighlight = terms
			}

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
		// Say what was actually searched. The index does not cover tool_result
		// content, so a silent "N results" would overstate the coverage.
		count := fmt.Sprintf("%d results", len(a.searchResults))
		if len(a.searchResults) >= searchResultLimit {
			count = fmt.Sprintf("first %d results", searchResultLimit)
		}
		if a.searchMode == session.SearchModeIndexPartial {
			count += dimStyle.Render("  ·  tool output not indexed")
		}
		sb.WriteString(dimStyle.Render(count) + "\n")
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
		help = "↑↓/jk:nav  enter:open  /:edit  esc:close"
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

func (a *App) updateSearchResults(results []session.SearchResult, mode session.SearchMode) {
	a.searchResults = results
	a.searchMode = mode
	a.searchLoading = false

	items := make([]list.Item, len(results))
	for i, r := range results {
		items[i] = searchResultItem{result: r}
	}
	a.searchResultList.SetItems(items)
}
