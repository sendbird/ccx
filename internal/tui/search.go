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

	ti := textinput.New()
	ti.Placeholder = "Search all sessions..."
	ti.Focus()
	ti.Width = 50
	a.searchInput = ti

	a.searchResultList = list.New(nil, list.NewDefaultDelegate(), 0, 0)
	a.searchResultList.SetShowTitle(false)
	a.searchResultList.SetShowStatusBar(false)
	a.searchResultList.SetFilteringEnabled(false)
	a.searchResultList.SetShowHelp(false)
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
	}

	return a, nil
}

func (a *App) openSearchResult(result session.SearchResult) {
	for i, sess := range a.sessions {
		if sess.ID == result.Session.ID {
			a.sessionList.Select(i)
			a.currentSess = sess
			a.openConversation(sess)
			return
		}
	}
}

func (a *App) renderSearchView() string {
	var sb strings.Builder

	titleStyle := statTitleStyle
	ruler := dimStyle.Render(strings.Repeat("─", min(a.width-4, 60)))

	// Title and input
	sb.WriteString("\n")
	sb.WriteString(titleStyle.Render("  SEARCH SESSIONS") + "\n")
	sb.WriteString("  " + ruler + "\n\n")

	inputStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(0, 1).
		Width(min(a.width-6, 60))

	sb.WriteString("  " + inputStyle.Render(a.searchInput.View()) + "\n\n")

	if a.searchLoading {
		sb.WriteString("  " + dimStyle.Render("Searching...") + "\n")
		return sb.String()
	}

	if a.searchQuery != "" && len(a.searchResults) == 0 {
		sb.WriteString("  " + dimStyle.Render("No results found") + "\n")
		return sb.String()
	}

	if len(a.searchResults) > 0 {
		sb.WriteString(fmt.Sprintf("  %s\n\n", dimStyle.Render(fmt.Sprintf("%d results", len(a.searchResults)))))

		// Render results
		listHeight := a.height - 12
		if listHeight < 5 {
			listHeight = 5
		}
		a.searchResultList.SetSize(a.width-4, listHeight)
		sb.WriteString(a.searchResultList.View())
	} else if a.searchQuery == "" {
		sb.WriteString("  " + dimStyle.Render("Type a query and press Enter to search") + "\n\n")

		keyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true)
		descStyle := dimStyle

		sb.WriteString("  " + titleStyle.Render("Syntax") + "\n")
		sb.WriteString("  " + ruler + "\n")
		sb.WriteString(fmt.Sprintf("  %s  %s\n", keyStyle.Render("word1 word2    "), descStyle.Render("AND match (both must appear)")))
		sb.WriteString(fmt.Sprintf("  %s  %s\n", keyStyle.Render("\"exact phrase\" "), descStyle.Render("phrase match")))
		sb.WriteString(fmt.Sprintf("  %s  %s\n", keyStyle.Render("-exclude       "), descStyle.Render("exclude term from results")))
		sb.WriteString("\n")
		sb.WriteString("  " + titleStyle.Render("Filters") + "\n")
		sb.WriteString("  " + ruler + "\n")
		sb.WriteString(fmt.Sprintf("  %s  %s\n", keyStyle.Render("user:          "), descStyle.Render("only user messages")))
		sb.WriteString(fmt.Sprintf("  %s  %s\n", keyStyle.Render("assistant:     "), descStyle.Render("only assistant responses")))
		sb.WriteString(fmt.Sprintf("  %s  %s\n", keyStyle.Render("tool:          "), descStyle.Render("only tool usage")))
	}

	// Help line at bottom
	var help string
	if a.searchInput.Focused() {
		help = "  enter:search  esc:close"
	} else if len(a.searchResults) > 0 {
		help = "  ↑↓/jk:nav  enter:open  /:edit  esc:close"
	} else {
		help = "  esc:close"
	}

	// Add padding to push help to bottom
	contentHeight := strings.Count(sb.String(), "\n")
	neededPadding := a.height - contentHeight - 2
	if neededPadding > 0 {
		sb.WriteString(strings.Repeat("\n", neededPadding))
	}

	sb.WriteString("\n" + dimStyle.Render(help))

	return sb.String()
}

func (a *App) updateSearchResults(results []session.SearchResult) {
	a.searchResults = results
	a.searchLoading = false

	items := make([]list.Item, len(results))
	for i, r := range results {
		items[i] = searchResultItem{result: r}
	}
	a.searchResultList.SetItems(items)
}
