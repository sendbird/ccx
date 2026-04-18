package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/sendbird/ccx/internal/session"
)

type sessionResultEntry struct {
	ID              string `json:"id"`
	ProjectRootPath string `json:"project_root_path"`
	TranscriptPath  string `json:"transcript_path"`
}

var (
	pickDimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	pickAccentStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#38BDF8"))
	pickLiveStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#10B981")).Bold(true)
	pickBusyStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B")).Bold(true)
	pickWTStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#A78BFA")).Bold(true)
)

type sessionPickerModel struct {
	all       []session.Session
	filterVal []string // parallel to all
	visible   []int    // indices into all
	cursor    int
	multi     bool

	selected map[string]bool // session.ID → selected
	order    []string        // IDs in selection order

	searching   bool
	searchInput textinput.Model
	query       string

	width     int
	height    int
	confirmed bool // true → user pressed enter; false at quit → cancel
}

// newSessionPickerModel takes sessions already paired with their filter values
// (precomputed by the caller for reuse).
func newSessionPickerModel(sessions []session.Session, filterVals []string, query string, multi bool) sessionPickerModel {
	m := sessionPickerModel{
		all:       sessions,
		filterVal: filterVals,
		multi:     multi,
		query:     query,
		selected:  make(map[string]bool),
	}
	m.recomputeVisible()
	return m
}

func (m sessionPickerModel) Init() tea.Cmd { return nil }

func (m *sessionPickerModel) recomputeVisible() {
	m.visible = m.visible[:0]
	for i, fv := range m.filterVal {
		if session.Matches(fv, m.query) {
			m.visible = append(m.visible, i)
		}
	}
	if m.cursor >= len(m.visible) {
		m.cursor = len(m.visible) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
}

func (m sessionPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tea.KeyMsg:
		if m.searching {
			return m.handleSearchKey(msg)
		}
		return m.handleKey(msg)
	}
	return m, nil
}

func (m sessionPickerModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "esc", "q":
		return m, tea.Quit
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.visible)-1 {
			m.cursor++
		}
	case "/":
		m.searching = true
		ti := textinput.New()
		ti.Prompt = "/"
		ti.Width = 40
		ti.SetValue(m.query)
		ti.Focus()
		m.searchInput = ti
	case " ":
		if m.multi && m.cursor >= 0 && m.cursor < len(m.visible) {
			s := m.all[m.visible[m.cursor]]
			if m.selected[s.ID] {
				delete(m.selected, s.ID)
				for i, id := range m.order {
					if id == s.ID {
						m.order = append(m.order[:i], m.order[i+1:]...)
						break
					}
				}
			} else {
				m.selected[s.ID] = true
				m.order = append(m.order, s.ID)
			}
			if m.cursor < len(m.visible)-1 {
				m.cursor++
			}
		}
	case "enter":
		if len(m.visible) == 0 {
			return m, nil
		}
		m.confirmed = true
		return m, tea.Quit
	}
	return m, nil
}

func (m sessionPickerModel) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.searching = false
		return m, nil
	case "enter":
		m.searching = false
		m.query = m.searchInput.Value()
		m.recomputeVisible()
		return m, nil
	default:
		var cmd tea.Cmd
		m.searchInput, cmd = m.searchInput.Update(msg)
		m.query = m.searchInput.Value()
		m.recomputeVisible()
		return m, cmd
	}
}

func (m sessionPickerModel) result() []sessionResultEntry {
	if m.multi && len(m.order) > 0 {
		out := make([]sessionResultEntry, 0, len(m.order))
		byID := make(map[string]session.Session, len(m.all))
		for _, s := range m.all {
			byID[s.ID] = s
		}
		for _, id := range m.order {
			if s, ok := byID[id]; ok {
				out = append(out, sessionToResult(s))
			}
		}
		return out
	}
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return nil
	}
	return []sessionResultEntry{sessionToResult(m.all[m.visible[m.cursor]])}
}

// Kept standalone so a row-agnostic picker (Phase 2) can swap implementations
// via an interface method instead of a rewrite.
func sessionToResult(s session.Session) sessionResultEntry {
	return sessionResultEntry{
		ID:              s.ID,
		ProjectRootPath: s.ProjectPath,
		TranscriptPath:  s.FilePath,
	}
}

func renderSessionRow(s session.Session, width int) string {
	timeStr := timeAgoShort(s.ModTime)
	proj := s.ProjectName
	if proj == "" {
		proj = s.ShortID
	}
	branch := ""
	if s.GitBranch != "" {
		branch = " (" + s.GitBranch + ")"
	}
	prompt := s.FirstPrompt
	if len(prompt) > 60 {
		prompt = prompt[:57] + "..."
	}

	badges := ""
	if s.IsLive {
		if s.IsResponding {
			badges += " " + pickBusyStyle.Render("[BUSY]")
		} else {
			badges += " " + pickLiveStyle.Render("[LIVE]")
		}
	}
	if s.IsWorktree {
		badges += " " + pickWTStyle.Render("[WT]")
	}

	head := fmt.Sprintf("%s  %s%s",
		pickDimStyle.Render(fmt.Sprintf("%-10s", timeStr)),
		pickAccentStyle.Render(proj),
		pickDimStyle.Render(branch),
	)
	head += badges
	line := head + "  " + pickDimStyle.Render(prompt)

	if width > 0 && lipgloss.Width(line) > width-2 {
		line = head
	}
	return line
}

func timeAgoShort(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("Jan 02")
	}
}

func (m sessionPickerModel) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}

	title := pickAccentStyle.Render(fmt.Sprintf(" pick session (%d/%d)", len(m.visible), len(m.all)))
	if m.multi {
		title += pickDimStyle.Render(fmt.Sprintf("  [multi: %d selected]", len(m.order)))
	}

	listH := m.height - 3
	if listH < 3 {
		listH = 3
	}

	start := 0
	if m.cursor >= start+listH {
		start = m.cursor - listH + 1
	}
	end := start + listH
	if end > len(m.visible) {
		end = len(m.visible)
	}

	var lines []string
	for i := start; i < end; i++ {
		s := m.all[m.visible[i]]
		row := renderSessionRow(s, m.width-4)
		cursor := "  "
		if m.multi && m.selected[s.ID] {
			cursor = pickAccentStyle.Render("✓ ")
		}
		if i == m.cursor {
			cursor = pickAccentStyle.Render("> ")
			if m.multi && m.selected[s.ID] {
				cursor = pickAccentStyle.Render("✓>")
			}
			row = pickAccentStyle.Render(row)
		}
		lines = append(lines, cursor+row)
	}
	if len(lines) == 0 {
		lines = append(lines, pickDimStyle.Render("  no matches"))
	}

	var searchLine string
	if m.searching {
		searchLine = m.searchInput.View()
	} else if m.query != "" {
		searchLine = pickDimStyle.Render("[" + m.query + "]")
	}

	actions := "↵ confirm  / search  esc cancel"
	if m.multi {
		actions = "↵ confirm  space select  / search  esc cancel"
	}
	footer := pickDimStyle.Render(actions)

	body := strings.Join(lines, "\n")
	if searchLine != "" {
		return title + "\n" + searchLine + "\n" + body + "\n" + footer
	}
	return title + "\n" + body + "\n" + footer
}
