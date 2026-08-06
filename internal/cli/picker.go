package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/sendbird/ccx/internal/extract"
	"github.com/sendbird/ccx/internal/kitty"
	"github.com/sendbird/ccx/internal/opener"
	"github.com/sendbird/ccx/internal/session"
	"github.com/sendbird/ccx/internal/tui"
)

var pickerDiffStyles = extract.DiffStyles{
	Add:    func(s string) string { return lipgloss.NewStyle().Foreground(lipgloss.Color("#4ADE80")).Render(s) },
	Del:    func(s string) string { return lipgloss.NewStyle().Foreground(lipgloss.Color("#F87171")).Render(s) },
	Hunk:   func(s string) string { return lipgloss.NewStyle().Foreground(lipgloss.Color("#7DD3FC")).Render(s) },
	Header: func(s string) string { return lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280")).Render(s) },
}

// PickerResult is returned when the picker exits with a jump target.
type PickerResult struct {
	SessionID string
	EntryUUID string
}

type pickerPreviewMode int

// minRowLabelWidth is the floor for a list row's label. Optional detail (ref
// count, PR/Jira status) is dropped rather than squeezing the label below this.
const minRowLabelWidth = 12

const (
	pickerPreviewConversation pickerPreviewMode = iota
	pickerPreviewArtifacts
)

type pickerModel struct {
	kind     string // "urls", "files", "images", "changes"
	allItems []PickerItem
	items    []PickerItem // filtered
	cursor   int
	selected map[int]bool // indices in allItems

	// Ref selection: when an item has multiple refs, Enter opens ref picker
	refPicking bool // true when choosing which ref to jump to
	refCursor  int

	// Preview focus: right-arrow/tab moves focus to preview for scrolling
	previewFocused   bool
	previewMode      pickerPreviewMode
	artifactCursor   int
	artifactSelected map[int]bool

	searching   bool
	searchInput textinput.Model
	searchTerm  string

	// showAllRefs overrides the refs picker's default open-only filter so
	// merged/closed PRs and done Jira issues are listed too. Toggled with M,
	// or implicitly when the search term contains an explicit is:merged /
	// is:closed / is:all tag.
	showAllRefs bool

	preview viewport.Model
	width   int
	height  int

	// termFocused tracks whether the host terminal/tmux window is focused.
	// When false we hide Kitty graphics so the image doesn't linger on
	// unrelated windows. Starts true until we first receive a BlurMsg.
	termFocused bool
	// focusReportingEnabled is set after we ask the terminal to send focus
	// events, so we only ask once per session.
	focusReportingEnabled bool

	// refStatus holds resolved PR/Jira status keyed by URL, filled
	// asynchronously after the picker opens (urls kind only) so PR/Jira rows
	// show OPEN/MERGED/review/checks inline.
	refStatus map[string]session.SessionRef

	result *PickerResult
	quit   bool

	// opener configures how selected URLs are opened (open.command_template,
	// falling back to the OS default). Shared with the TUI so both open paths
	// honor the same config.
	opener opener.Config

	// ctx is what the picker needs to re-extract its items on `R` (refresh):
	// the subcommand and the session file to reload.
	ctx pickerContext

	// langmap maps CJK jamo/letters to the Latin key at the same physical
	// position, so picker shortcuts fire under a CJK input source.
	langmap map[rune]string
}

func newPickerModel(kind string, items []PickerItem, openerCfg opener.Config, ctx pickerContext) pickerModel {
	configPath := filepath.Join(os.Getenv("HOME"), ".config", "ccx", "config.yaml")
	return pickerModel{
		kind:             kind,
		allItems:         items,
		items:            items,
		selected:         make(map[int]bool),
		artifactSelected: make(map[int]bool),
		previewMode:      pickerPreviewConversation,
		termFocused:      true,
		refStatus:        make(map[string]session.SessionRef),
		opener:           openerCfg,
		ctx:              ctx,
		langmap:          tui.LoadLangmap(configPath),
	}
}

// pickerRefStatusMsg carries one resolved PR/Jira status back into the picker.
type pickerRefStatusMsg struct {
	ref session.SessionRef
}

func (m pickerModel) Init() tea.Cmd {
	return m.resolveRefsCmd()
}

// resolveRefsCmd asynchronously resolves PR/Jira status for every GitHub-PR or
// Jira URL in the list, streaming each result back as a pickerRefStatusMsg. It
// reuses session.ResolveRef so results are TTL-cached and share the bounded
// concurrency used elsewhere. Non-urls kinds have no PR/Jira links → no-op.
func (m pickerModel) resolveRefsCmd() tea.Cmd {
	if m.kind != "urls" && m.kind != "refs" {
		return nil
	}
	var cmds []tea.Cmd
	for _, it := range m.allItems {
		if it.Item.Category != "pr" && it.Item.Category != "jira" {
			continue
		}
		ref, ok := session.ClassifyURLRef(it.Item.URL)
		if !ok {
			continue
		}
		r := ref
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			return pickerRefStatusMsg{ref: session.ResolveRef(ctx, r)}
		})
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// kittyPickerTickMsg fires every 250ms when Kitty is supported so we can
// poll tmux pane visibility — tmux does NOT forward focus-out events on
// window switch, so BlurMsg alone isn't enough to detect the transition.
type kittyPickerTickMsg time.Time

func kittyPickerTickCmd() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(t time.Time) tea.Msg {
		return kittyPickerTickMsg(t)
	})
}

func (m pickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case pickerRefStatusMsg:
		if m.refStatus != nil {
			m.refStatus[msg.ref.URL] = msg.ref
		}
		// A ref's newly-resolved state may move it out of (or into) the
		// default open-only filter, so re-filter to keep the list honest.
		if m.kind == "refs" {
			m.filterItems()
			m.updatePreview()
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.preview = viewport.New(m.previewWidth(), m.height-3)
		m.updatePreview()
		// tmux pane geometry changes on resize — invalidate the cached
		// offset so Kitty cursor positioning recomputes next render.
		kitty.InvalidatePaneOffset()
		// Ask the terminal to send focus events the first time we have a
		// real size (Bubbletea delivers the first WindowSizeMsg early). We
		// only need this when Kitty is supported so we can hide the image
		// while the user is on another tmux window.
		var cmd tea.Cmd
		if kitty.Supported() && !m.focusReportingEnabled {
			m.focusReportingEnabled = true
			cmd = tea.Batch(tea.EnableReportFocus, kittyPickerTickCmd())
		}
		return m, cmd
	case kittyPickerTickMsg:
		// Poll tmux pane visibility. BlurMsg arrives only when the outer
		// terminal loses focus (cmd-tab, etc.) — tmux intra-session window
		// switches do not surface a focus event, so we rely on this tick
		// to notice that our pane is no longer on screen and wipe any
		// lingering Kitty graphics before they bleed into the new window.
		if kitty.Supported() {
			visible := kitty.PaneVisible()
			if !visible && m.termFocused {
				m.termFocused = false
				fmt.Fprint(os.Stdout, kitty.ClearImages())
				_ = os.Stdout.Sync()
			} else if visible && !m.termFocused {
				m.termFocused = true
				kitty.InvalidatePaneOffset()
			}
		}
		return m, kittyPickerTickCmd()
	case tea.BlurMsg:
		// BlurMsg fires on BOTH same-window pane focus changes and on
		// tmux window switches. We only want to clear Kitty graphics in
		// the latter case — same-window pane moves leave our pane on
		// screen and the image must stay visible.
		if kitty.Supported() && !kitty.PaneVisible() {
			m.termFocused = false
			// tmux stops forwarding our pane's passthrough output as soon
			// as the window switches away, so a clear emitted from the
			// next render would be dropped. Write directly to stdout here
			// while our pane is still being drained.
			fmt.Fprint(os.Stdout, kitty.ClearImages())
			_ = os.Stdout.Sync()
		}
		return m, nil
	case tea.FocusMsg:
		m.termFocused = true
		kitty.InvalidatePaneOffset()
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.quit = true
			return m, tea.Quit
		}
		// Under a CJK input source, map single-jamo keys to the Latin key at the
		// same physical position so picker shortcuts work. Skipped while searching
		// so Korean can be typed into the filter verbatim.
		if !m.searching {
			msg = tui.NormalizeCJKKey(msg, m.langmap)
		}
		if m.refPicking {
			return m.handleRefKey(msg)
		}
		if m.previewFocused {
			return m.handlePreviewKey(msg)
		}
		if m.searching {
			return m.handleSearchKey(msg)
		}
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *pickerModel) cycleConversationPreviewMode(reverse bool) {
	if m.kind != "conversation" {
		return
	}
	if reverse {
		if m.previewMode == pickerPreviewConversation {
			m.previewMode = pickerPreviewArtifacts
		} else {
			m.previewMode = pickerPreviewConversation
		}
	} else {
		if m.previewMode == pickerPreviewConversation {
			m.previewMode = pickerPreviewArtifacts
		} else {
			m.previewMode = pickerPreviewConversation
		}
	}
	m.updatePreview()
}

// --- Preview focus mode ---

func (m pickerModel) handlePreviewKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.kind == "conversation" {
		if m.cursor >= 0 && m.cursor < len(m.items) {
			artifacts := m.items[m.cursor].ConversationArtifacts
			switch key {
			case "up", "k":
				if len(artifacts) > 0 && m.artifactCursor > 0 {
					m.artifactCursor--
					m.updatePreview()
					return m, nil
				}
				m.preview.LineUp(3)
				return m, nil
			case "down", "j":
				if len(artifacts) > 0 && m.artifactCursor < len(artifacts)-1 {
					m.artifactCursor++
					m.updatePreview()
					return m, nil
				}
				m.preview.LineDown(3)
				return m, nil
			case " ":
				if len(artifacts) > 0 {
					if m.artifactSelected[m.artifactCursor] {
						delete(m.artifactSelected, m.artifactCursor)
					} else {
						m.artifactSelected[m.artifactCursor] = true
					}
					m.updatePreview()
				}
				return m, nil
			}
		}
	}
	switch key {
	case "esc", "left", "h":
		m.previewFocused = false
		return m, nil
	case "tab":
		if m.kind == "conversation" {
			m.cycleConversationPreviewMode(false)
			return m, nil
		}
		m.previewFocused = false
		return m, nil
	case "shift+tab":
		if m.kind == "conversation" {
			m.cycleConversationPreviewMode(true)
			return m, nil
		}
		return m, nil
	case "q":
		m.previewFocused = false
		return m, nil
	case "up", "k":
		m.preview.LineUp(3)
		return m, nil
	case "down", "j":
		m.preview.LineDown(3)
		return m, nil
	case "ctrl+d":
		m.preview.HalfViewDown()
		return m, nil
	case "ctrl+u":
		m.preview.HalfViewUp()
		return m, nil
	case "g":
		m.preview.GotoTop()
		return m, nil
	case "G":
		m.preview.GotoBottom()
		return m, nil
	case "enter", "e", "o":
		// Pass through to normal handler
		m.previewFocused = false
		return m.handleKey(msg)
	}
	return m, nil
}

// --- Ref selection mode ---

func (m pickerModel) handleRefKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	item := m.items[m.cursor]
	switch key {
	case "esc":
		m.refPicking = false
		m.updatePreview()
		return m, nil
	case "up", "k":
		if m.refCursor > 0 {
			m.refCursor--
			m.updatePreview()
		}
		return m, nil
	case "down", "j":
		if m.refCursor < len(item.Refs)-1 {
			m.refCursor++
			m.updatePreview()
		}
		return m, nil
	case "enter":
		if m.refCursor >= 0 && m.refCursor < len(item.Refs) {
			ref := item.Refs[m.refCursor]
			m.result = &PickerResult{SessionID: item.SessionID, EntryUUID: ref.EntryUUID}
			return m, tea.Quit
		}
		return m, nil
	}
	// Number keys 1-9 for quick ref selection
	if len(key) == 1 && key[0] >= '1' && key[0] <= '9' {
		idx := int(key[0] - '1')
		if idx < len(item.Refs) {
			ref := item.Refs[idx]
			m.result = &PickerResult{SessionID: item.SessionID, EntryUUID: ref.EntryUUID}
			return m, tea.Quit
		}
	}
	return m, nil
}

// --- Search mode ---

func (m pickerModel) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.searching = false
		return m, nil
	case "enter":
		m.searching = false
		m.searchTerm = m.searchInput.Value()
		m.filterItems()
		return m, nil
	default:
		var cmd tea.Cmd
		m.searchInput, cmd = m.searchInput.Update(msg)
		m.searchTerm = m.searchInput.Value()
		m.filterItems()
		return m, cmd
	}
}

// --- Normal mode ---

func (m pickerModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	switch key {
	case "q", "esc":
		if m.searchTerm != "" {
			m.searchTerm = ""
			m.filterItems()
			m.updatePreview()
			return m, nil
		}
		m.quit = true
		return m, tea.Quit

	case "M":
		// Toggle showing merged/closed PRs and done Jira issues. Only
		// meaningful for the refs picker, where the default open-only filter
		// would otherwise hide them.
		if m.kind == "refs" {
			m.showAllRefs = !m.showAllRefs
			m.filterItems()
			m.updatePreview()
		}
		return m, nil

	case "R":
		// Refresh: re-read the session file and rebuild the item list, keeping
		// the active search term. Selection is reset (indices may have shifted).
		if fresh, err := extractItems(m.ctx.command, m.ctx.filePath, m.ctx.sessID); err == nil {
			m.allItems = fresh
			m.selected = make(map[int]bool)
			m.refPicking = false
			// A failed lookup is cached like any other resolution. Manual refresh
			// must force a network retry instead of replaying that unknown state.
			if m.kind == "refs" || m.kind == "urls" {
				session.ClearRefCache()
				m.refStatus = make(map[string]session.SessionRef)
			}
			m.filterItems() // reapplies m.searchTerm and clamps the cursor
			m.updatePreview()
			return m, m.resolveRefsCmd()
		}
		return m, nil

	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
			m.updatePreview()
		}
		return m, nil

	case "down", "j":
		if m.cursor < len(m.items)-1 {
			m.cursor++
			m.updatePreview()
		}
		return m, nil

	case "right", "l":
		m.previewFocused = true
		return m, nil
	case "tab":
		if m.kind == "conversation" && m.previewFocused {
			m.cycleConversationPreviewMode(false)
			return m, nil
		}
		m.previewFocused = true
		return m, nil
	case "shift+tab":
		if m.kind == "conversation" {
			m.cycleConversationPreviewMode(true)
			return m, nil
		}
		return m, nil

	case "/":
		m.searching = true
		ti := textinput.New()
		ti.Prompt = "/"
		ti.Width = 40
		ti.SetValue(m.searchTerm)
		ti.Focus()
		m.searchInput = ti
		return m, nil

	case " ":
		if m.cursor >= 0 && m.cursor < len(m.items) {
			idx := m.realIndex(m.cursor)
			if m.selected[idx] {
				delete(m.selected, idx)
			} else {
				m.selected[idx] = true
			}
			if m.cursor < len(m.items)-1 {
				m.cursor++
				m.updatePreview()
			}
		}
		return m, nil

	case "a":
		for i := range m.items {
			m.selected[m.realIndex(i)] = true
		}
		return m, nil

	case "A":
		clear(m.selected)
		return m, nil

	case "enter":
		if len(m.selected) > 0 {
			// Multi-select: open all
			m.openItems(m.selectedURLs())
			return m, nil
		}
		if m.cursor < 0 || m.cursor >= len(m.items) {
			return m, nil
		}
		item := m.items[m.cursor]
		if len(item.Refs) == 1 {
			// Single ref: jump directly
			m.result = &PickerResult{SessionID: item.SessionID, EntryUUID: item.Refs[0].EntryUUID}
			return m, tea.Quit
		}
		// Multiple refs: enter ref selection mode
		m.refPicking = true
		m.refCursor = 0
		m.updatePreview()
		return m, nil

	case "o":
		if m.kind == "conversation" {
			targets := m.conversationArtifactTargets(false)
			if len(targets) > 0 {
				m.openItems(targets)
			}
			return m, nil
		}
		m.openItems(m.selectedURLs())
		return m, nil

	case "e":
		if m.kind == "conversation" {
			targets := m.conversationArtifactTargets(true)
			if len(targets) > 0 {
				m.editItems(targets)
			}
			return m, nil
		}
		targets := m.selectedURLs()
		if len(targets) > 0 {
			m.editItems(targets)
		}
		return m, nil

	case "y":
		targets := m.selectedURLs()
		if len(targets) > 0 {
			copyToClipboard(strings.Join(targets, "\n"))
		}
		return m, nil
	}
	return m, nil
}

// --- Preview ---

func (m *pickerModel) updatePreview() {
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return
	}
	item := m.items[m.cursor]
	pw := m.previewWidth()

	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	accent := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#38BDF8"))
	highlight := lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))

	var sb strings.Builder

	if m.kind == "conversation" {
		m.preview.SetContent(m.conversationPreview())
		m.preview.GotoTop()
		return
	}

	// Header
	sb.WriteString(accent.Render(strings.ToUpper(item.Item.Category)))
	sb.WriteString("  ")
	sb.WriteString(highlight.Render(item.Item.Label))
	if len(item.Refs) > 1 {
		sb.WriteString(dim.Render(fmt.Sprintf("  (%d refs)", len(item.Refs))))
	}
	sb.WriteString("\n")
	if item.Item.URL != item.Item.Label {
		url := item.Item.URL
		if len(url) > pw-2 {
			url = url[:pw-5] + "..."
		}
		sb.WriteString(dim.Render(url))
		sb.WriteString("\n")
	}
	if m.kind == "changes" {
		// Render actual diffs for all refs
		hasDiff := false
		for _, ref := range item.Refs {
			if ref.ToolName != "" && ref.ToolInput != "" {
				diff := extract.FormatDiff(ref.ToolName, ref.ToolInput, pw, pickerDiffStyles)
				if diff != "" {
					sb.WriteString(diff)
					hasDiff = true
				}
			}
		}
		if !hasDiff {
			sb.WriteString(dim.Render("(no diff data)"))
		}
		sb.WriteString("\n")
		sb.WriteString(dim.Render("↵:jump to message  e:open in $EDITOR"))
		sb.WriteString("\n\n")
	} else {
		sb.WriteString("\n")
	}

	// Refs with context
	if m.refPicking {
		sb.WriteString(accent.Render("Select reference to jump to:"))
		sb.WriteString("\n\n")
	}

	for i, ref := range item.Refs {
		// Ref header
		cursor := "  "
		numStyle := dim
		if m.refPicking && i == m.refCursor {
			cursor = accent.Render("> ")
			numStyle = accent
		}
		refHeader := fmt.Sprintf("%s%s %s  %s",
			cursor,
			numStyle.Render(fmt.Sprintf("[%d]", i+1)),
			highlight.Render(strings.ToUpper(ref.Role)),
			dim.Render(ref.Timestamp.Format("15:04:05")),
		)
		sb.WriteString(refHeader + "\n")

		// Context lines (indented)
		if ref.Preview != "" {
			for _, line := range strings.Split(ref.Preview, "\n") {
				if line == "" {
					continue
				}
				// Skip the role+timestamp header line from entryContext since we show it above
				if strings.HasPrefix(line, "USER ") || strings.HasPrefix(line, "ASSISTANT ") || strings.HasPrefix(line, "ENTRY ") {
					continue
				}
				if len(line) > pw-6 {
					line = line[:pw-9] + "..."
				}
				sb.WriteString("    " + dim.Render(line) + "\n")
			}
		}
		sb.WriteString("\n")
	}

	if m.refPicking {
		sb.WriteString(dim.Render("↵:jump  1-9:quick select  esc:cancel"))
	}

	m.preview.SetContent(sb.String())
	m.preview.GotoTop()
}

// --- View ---

func renderConversationListRow(item PickerItem, selected bool, selectedMark, plainMark, badge string, listW int, selStyle, dimStyle lipgloss.Style) []string {
	plainBadge := strings.ToUpper(item.Item.Category)
	plainBadge = truncateWidth(plainBadge, 5)
	plainBadge = padRightWidth(plainBadge, 5)
	badgeStyled := lipgloss.NewStyle().Foreground(lipgloss.Color("#6366F1")).Bold(true).Render(plainBadge)

	cursorPlain := " "
	cursorStyled := " "
	markPlain := plainMark
	markStyled := plainMark
	if selected {
		cursorPlain = ">"
		cursorStyled = selStyle.Render(">")
		markPlain = "  "
		markStyled = selectedMark
	}

	plainPrefix := cursorPlain + markPlain + plainBadge + " "
	styledPrefix := cursorStyled + markStyled + badgeStyled + " "
	textPrefix := strings.Repeat(" ", lipgloss.Width(plainPrefix))

	label := item.Item.Label
	maxHeader := max(listW-lipgloss.Width(plainPrefix), 12)
	if runewidth.StringWidth(label) > maxHeader {
		label = truncateWidth(label, maxHeader)
	}
	lines := []string{styledPrefix + func() string {
		if selected {
			return selStyle.Render(label)
		}
		return dimStyle.Render(label)
	}()}

	textLines := strings.Split(item.ConversationText, "\n")
	if len(textLines) == 0 {
		textLines = []string{"(no visible content)"}
	}
	for i, line := range textLines {
		if i >= 2 {
			break
		}
		maxBody := max(listW-lipgloss.Width(textPrefix), 12)
		if runewidth.StringWidth(line) > maxBody {
			line = truncateWidth(line, maxBody)
		}
		if selected {
			lines = append(lines, textPrefix+selStyle.Render(line))
		} else {
			lines = append(lines, textPrefix+dimStyle.Render(line))
		}
	}
	// The 12-cell floors above can exceed listW on a very narrow pane, so clamp
	// every produced line to the pane width as a last step.
	clamp := lipgloss.NewStyle().MaxWidth(listW)
	for i := range lines {
		lines[i] = clamp.Render(lines[i])
	}
	return lines
}

// renderListRow renders one non-conversation list row clamped to exactly listW
// cells. Everything after the label is optional detail: it is added only while
// the budget allows, so a resolved PR status can never push the row past the
// pane width and make lipgloss wrap it onto a second line (which would blow the
// height budget and scramble the whole layout).
func (m pickerModel) renderListRow(item PickerItem, cursored bool, check string, listW int, sel, dim, cat lipgloss.Style) string {
	prefix := " "
	if cursored {
		prefix = ">"
	}
	badgePlain := padRightWidth(strings.ToUpper(item.Item.Category), 5)
	// Widths are computed on the plain text; styling adds no cells.
	used := runewidth.StringWidth(prefix+check+badgePlain) + 1 // +1 for the space after the badge

	refBadgePlain := ""
	if len(item.Refs) > 1 {
		refBadgePlain = fmt.Sprintf(" ×%d", len(item.Refs))
	}
	refBadgeW := runewidth.StringWidth(refBadgePlain)
	statusPlain := m.refStatusPlain(item.Item.URL, max(listW-used-minRowLabelWidth-refBadgeW, 0))

	// The label yields space to the ref count and status, but never shrinks below
	// minRowLabelWidth — an unreadable label is worse than a dropped badge. The
	// status carries a 2-cell separator that must be budgeted here too, otherwise
	// the final clamp eats the tail of the status ("MERGED" → "MERG").
	statusW := 0
	if statusPlain != "" {
		statusW = runewidth.StringWidth(statusPlain) + 2
	}
	labelBudget := max(listW-used-refBadgeW-statusW, minRowLabelWidth)
	label := item.Item.Label
	if runewidth.StringWidth(label) > labelBudget {
		label = truncateWidth(label, labelBudget)
	}

	labelStyle := dim
	prefixStyled := " "
	if cursored {
		labelStyle = sel
		prefixStyled = sel.Render(">")
	}
	row := prefixStyled + check + cat.Render(badgePlain) + " " + labelStyle.Render(label) +
		dim.Render(refBadgePlain)
	if statusPlain != "" {
		row += "  " + dim.Render(statusPlain)
	}
	// Final clamp: styling and rounding can still leave a cell of slack, and one
	// overflowing cell is enough to wrap the row.
	return lipgloss.NewStyle().MaxWidth(listW).Render(row)
}

func (m pickerModel) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}

	listW := m.listWidth()
	pw := m.previewWidth()
	contentH := m.height - 2

	sel := lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8")).Bold(true)
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	cat := lipgloss.NewStyle().Foreground(lipgloss.Color("#6366F1")).Bold(true)

	maxListLines := contentH - 2 // reserve for scrollInfo
	if maxListLines < 3 {
		maxListLines = 3
	}

	// For conversation mode, estimate items per page from actual line budget
	var visMax int
	if m.kind == "conversation" {
		visMax = max(maxListLines/3, 1)
	} else {
		visMax = maxListLines
	}
	start := 0
	if m.cursor >= start+visMax {
		start = m.cursor - visMax + 1
	}

	var listLines []string
	for i := start; i < len(m.items); i++ {
		item := m.items[i]
		ri := m.realIndex(i)
		check := "  "
		if m.selected[ri] {
			check = sel.Render("* ")
		}
		if m.kind == "conversation" {
			rows := renderConversationListRow(item, i == m.cursor, check, "  ", "", listW, sel, dim)
			if len(listLines)+len(rows) > maxListLines && len(listLines) > 0 {
				break
			}
			listLines = append(listLines, rows...)
			continue
		}
		if len(listLines) >= maxListLines {
			break
		}
		listLines = append(listLines, m.renderListRow(item, i == m.cursor, check, listW, sel, dim, cat))
	}

	// The search line and counter share the list pane, so they get the same
	// per-line clamp as the rows: lipgloss wraps before it truncates, so an
	// over-long line costs a row even with MaxHeight set on the box.
	clampLine := lipgloss.NewStyle().MaxWidth(listW)

	searchLine := ""
	if m.searching {
		searchLine = clampLine.Render(m.searchInput.View())
	} else if m.searchTerm != "" {
		searchLine = clampLine.Render(dim.Render("[" + m.searchTerm + "]"))
	}

	scrollInfo := ""
	if len(m.items) > 0 {
		scrollInfo = fmt.Sprintf("[%d/%d]", m.cursor+1, len(m.items))
		if len(m.selected) > 0 {
			scrollInfo += fmt.Sprintf(" %d selected", len(m.selected))
		}
	} else {
		scrollInfo = "no matches"
	}
	scrollInfo = clampLine.Render(dim.Render(scrollInfo))

	listContent := strings.Join(listLines, "\n")
	if searchLine != "" {
		listContent = searchLine + "\n" + listContent
	}
	listContent += "\n" + scrollInfo

	// MaxWidth/MaxHeight, not just Width/Height: the latter pad but do not
	// truncate, so one over-long line would wrap and push the layout past the
	// terminal's last row.
	listBox := lipgloss.NewStyle().Width(listW).Height(contentH).
		MaxWidth(listW).MaxHeight(contentH).Render(listContent)
	borderColor := lipgloss.Color("#374151")
	if m.previewFocused {
		borderColor = lipgloss.Color("#38BDF8")
	}
	previewBox := lipgloss.NewStyle().
		Width(pw).Height(contentH).
		BorderLeft(true).
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(borderColor).
		PaddingLeft(1).
		MaxWidth(pw + 1). // +1 for the border column
		MaxHeight(contentH).
		Render(m.preview.View())

	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#38BDF8")).
		MaxWidth(m.width).
		Render(fmt.Sprintf(" %s (%d)", m.kind, len(m.allItems)))

	actions := "↵:jump"
	switch m.kind {
	case "urls":
		actions = "↵:jump  o:open  e:$EDITOR"
	case "refs":
		actions = "↵:jump  o:open  M:show all"
	case "files":
		actions = "↵:jump  e:$EDITOR"
	case "changes":
		actions = "↵:jump  e:$EDITOR"
	case "images":
		actions = "↵:jump  o:open  e:$EDITOR"
	case "conversation":
		actions = "↵:jump  o:open artifacts  e:edit local artifacts"
	}
	var footer string
	if m.searching {
		// Show filter hints when search is active
		hint := lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8"))
		scopes := ""
		switch m.kind {
		case "urls":
			scopes = "pr gh github jira slack other"
		case "refs":
			scopes = "pr jira open merged closed"
		case "files":
			scopes = "read write edit glob grep tool"
		case "changes":
			scopes = "change"
		case "images":
			scopes = "image"
		case "conversation":
			scopes = "conversation"
		}
		if scopes != "" {
			// Built from plain text first so the width check sees real cells,
			// then styled — "is:<scopes>  role:user asst" clipped to one row.
			plain := "is:" + scopes + "  role:user asst"
			if runewidth.StringWidth(plain) <= m.width {
				footer = hint.Render("is:") + dim.Render(scopes) + "  " + hint.Render("role:") + dim.Render("user asst")
			} else {
				footer = hint.Render("is:") + dim.Render(truncateWidth(scopes, max(m.width-3, 0)))
			}
		}
	} else if m.previewFocused {
		footer = dim.Render(fitHint(m.width,
			"j/k:scroll  ^d/^u:page  g/G:top/bottom  ←/esc:back",
			"j/k:scroll  ^d/^u:page  ←/esc:back",
			"j/k  ←:back",
		))
	} else {
		footer = dim.Render(fitHint(m.width,
			actions+"  y:copy  sp:select  a:all  A:none  →:preview  /:search  R:refresh  esc:quit",
			actions+"  y:copy  sp:select  →:preview  /:search  esc:quit",
			actions+"  /:search  esc:quit",
			"↵:jump  /:search  esc:quit",
		))
	}

	return title + "\n" + lipgloss.JoinHorizontal(lipgloss.Top, listBox, previewBox) + "\n" + footer + m.kittyImageLayer(contentH, listW, pw)
}

// fitHint returns the first hint variant that fits in width cells, falling back
// to a hard truncation of the last (shortest) one. Footer hints wrap rather
// than clip when they overflow, which costs a row the layout has not budgeted
// for — dropping hints keeps the remaining ones readable.
func fitHint(width int, variants ...string) string {
	for _, v := range variants {
		if runewidth.StringWidth(v) <= width {
			return v
		}
	}
	last := variants[len(variants)-1]
	return truncateWidth(last, width)
}

// kittyImageLayer returns Kitty graphics escape sequences to render the
// focused image into the preview pane. Returns empty string (or a clear
// sequence) when no image should be drawn.
func (m pickerModel) kittyImageLayer(contentH, listW, previewW int) string {
	if !kitty.Supported() {
		return ""
	}
	// Don't draw while the terminal / tmux window is out of focus — the
	// image would linger on top of whatever the user switched to.
	if !m.termFocused {
		return kitty.ClearImages()
	}
	if m.kind != "images" || m.previewFocused {
		return kitty.ClearImages()
	}
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return kitty.ClearImages()
	}
	item := m.items[m.cursor]
	// Image items use Item.URL as the cached file path (absolute) set by
	// extractImagesWithContext. Only render when that points at a real file.
	if item.Item.Category != "image" || item.Item.URL == "" {
		return kitty.ClearImages()
	}
	cachePath := item.Item.URL
	if _, err := os.Stat(cachePath); err != nil {
		return kitty.ClearImages()
	}

	// Preview pane layout: title row (1) + preview box starts at row 2.
	// Preview has a 1-col left border and 1-col left padding → image starts
	// at column listW + 3 (1-based). Reserve a couple of rows for the preview
	// header (category/label/path/metadata) before drawing the image.
	headerRows := 5
	maxCols := max(previewW-3, 10)
	maxRows := max(contentH-headerRows-1, 4)
	if maxCols <= 0 || maxRows <= 0 {
		return kitty.ClearImages()
	}
	imgW, imgH := kitty.ImageSize(cachePath)
	cols, rows := kitty.FitSize(imgW, imgH, maxCols, maxRows)
	// Title row is row 1 → image row starts below that + headerRows.
	imageY := 1 + headerRows + max((maxRows-rows)/2, 0)
	imageX := listW + 3
	return kitty.ClearImages() + kitty.PlaceImage(cachePath, imageY, imageX, cols, rows)
}

// --- Helpers ---

func (m pickerModel) listWidth() int    { return m.width * 40 / 100 }
func (m pickerModel) previewWidth() int { return m.width - m.listWidth() - 2 }

func (m *pickerModel) filterItems() {
	term := strings.ToLower(m.searchTerm)
	terms := strings.Fields(term)

	// Split explicit ref-state tags from plain search terms. The refs picker
	// hides merged/closed PRs and done Jira issues by default so the list
	// focuses on active work. An explicit `is:merged`/`is:closed`/`is:all`
	// tag (or the M toggle) lifts the default filter AND, for `is:merged`/
	// `is:closed`/`is:open`, narrows to only that state.
	statusFilter := refStatusDefault
	if m.showAllRefs {
		statusFilter = refStatusAll
	}
	var wantedStates map[string]bool // nil = no state narrowing
	addState := func(s string) {
		if wantedStates == nil {
			wantedStates = map[string]bool{}
		}
		wantedStates[s] = true
	}
	var plainTerms []string
	for _, t := range terms {
		switch t {
		case "is:merged":
			statusFilter = refStatusAll
			addState("MERGED")
		case "is:closed":
			statusFilter = refStatusAll
			addState("CLOSED")
		case "is:open":
			addState("OPEN")
			addState("DRAFT")
		case "is:all":
			statusFilter = refStatusAll
		default:
			plainTerms = append(plainTerms, t)
		}
	}

	var filtered []PickerItem
	for _, item := range m.allItems {
		if statusFilter == refStatusDefault && m.kind == "refs" {
			if !m.refIsOpen(item) {
				continue
			}
		}
		if wantedStates != nil && m.kind == "refs" {
			if !m.refHasState(item, wantedStates) {
				continue
			}
		}
		if len(plainTerms) > 0 {
			text := strings.ToLower(item.FilterValue())
			match := true
			for _, t := range plainTerms {
				if !strings.Contains(text, t) {
					match = false
					break
				}
			}
			if !match {
				continue
			}
		}
		filtered = append(filtered, item)
	}
	m.items = filtered
	if m.cursor >= len(m.items) {
		m.cursor = max(len(m.items)-1, 0)
	}
}

// refStatusDefault hides merged/closed PRs and done Jira issues; refStatusAll
// shows every ref regardless of state.
type refStatusFilter int

const (
	refStatusDefault refStatusFilter = iota
	refStatusAll
)

// refIsOpen reports whether a ref item is "active" (open/draft PR, or a Jira
// issue that is not done). Unresolved refs are treated as open so they stay
// visible until their status lands. Non-ref items (urls picker rows that are
// not pr/jira) are always considered open — the filter only applies to refs.
func (m pickerModel) refIsOpen(item PickerItem) bool {
	if item.Item.Category != "pr" && item.Item.Category != "jira" {
		return true
	}
	if m.refStatus == nil {
		return true // unresolved → surface until status lands
	}
	ref, ok := m.refStatus[item.Item.URL]
	if !ok {
		return true // not yet resolved → keep visible
	}
	return ref.IsOpen()
}

// refHasState reports whether a ref item's resolved state matches any of the
// wanted states (compared against SessionRef.State string values, e.g.
// "MERGED", "CLOSED", "OPEN", "DRAFT"). Unresolved refs match nothing — an
// explicit `is:merged` search shouldn't surface refs whose status hasn't
// landed yet. Non-ref items never match a state query.
func (m pickerModel) refHasState(item PickerItem, wanted map[string]bool) bool {
	if item.Item.Category != "pr" && item.Item.Category != "jira" {
		return false
	}
	if m.refStatus == nil {
		return false
	}
	ref, ok := m.refStatus[item.Item.URL]
	if !ok || !ref.Resolved {
		return false
	}
	return wanted[string(ref.State)]
}

func (m pickerModel) realIndex(filteredIdx int) int {
	if filteredIdx < 0 || filteredIdx >= len(m.items) {
		return -1
	}
	target := m.items[filteredIdx]
	for i, item := range m.allItems {
		if item.Item.URL == target.Item.URL {
			return i
		}
	}
	return filteredIdx
}

func (m pickerModel) selectedURLs() []string {
	if len(m.selected) > 0 {
		var urls []string
		for i, item := range m.allItems {
			if m.selected[i] {
				urls = append(urls, item.Item.URL)
			}
		}
		return urls
	}
	if m.cursor >= 0 && m.cursor < len(m.items) {
		return []string{m.items[m.cursor].Item.URL}
	}
	return nil
}

// refStatusPlain returns the widest plain-text PR/Jira status for a URL that
// fits in budget cells, or "" when the URL has no resolved status or even the
// shortest form does not fit. Plain text (no styling) so callers can measure it.
func (m pickerModel) refStatusPlain(url string, budget int) string {
	if m.refStatus == nil || budget <= 0 {
		return ""
	}
	ref, ok := m.refStatus[url]
	if !ok {
		return ""
	}
	// budget covers the two-space separator the caller prepends.
	for _, v := range session.RefStatusVariants(ref) {
		if runewidth.StringWidth(v)+2 <= budget {
			return v
		}
	}
	return ""
}

func (m pickerModel) openItems(urls []string) {
	for _, u := range urls {
		_ = opener.Open(m.opener, u)
	}
}

func (m pickerModel) editItems(paths []string) {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.Command(editor, paths...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()
}

func copyToClipboard(text string) {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	cmd.Run()
}

func padRightWidth(s string, n int) string {
	w := runewidth.StringWidth(s)
	if w >= n {
		return s
	}
	return s + strings.Repeat(" ", n-w)
}

func truncateWidth(s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= maxW {
		return s
	}
	if maxW <= 3 {
		return strings.Repeat(".", maxW)
	}
	out := ""
	w := 0
	for _, r := range s {
		rw := runewidth.RuneWidth(r)
		if w+rw > maxW-3 {
			break
		}
		out += string(r)
		w += rw
	}
	return out + "..."
}

func padRight(s string, n int) string {
	if len(s) >= n {
		return s[:n]
	}
	return s + strings.Repeat(" ", n-len(s))
}

func (m pickerModel) conversationArtifactTargets(editableOnly bool) []string {
	if m.kind != "conversation" || m.cursor < 0 || m.cursor >= len(m.items) {
		return nil
	}
	item := m.items[m.cursor]
	var targets []string
	if len(m.artifactSelected) > 0 {
		for i, artifact := range item.ConversationArtifacts {
			if !m.artifactSelected[i] {
				continue
			}
			if editableOnly && artifact.Category == "url" {
				continue
			}
			targets = append(targets, artifact.URL)
		}
		return targets
	}
	if m.artifactCursor >= 0 && m.artifactCursor < len(item.ConversationArtifacts) {
		artifact := item.ConversationArtifacts[m.artifactCursor]
		if editableOnly && artifact.Category == "url" {
			return nil
		}
		return []string{artifact.URL}
	}
	for _, artifact := range item.ConversationArtifacts {
		if editableOnly && artifact.Category == "url" {
			continue
		}
		targets = append(targets, artifact.URL)
	}
	return targets
}

func (m pickerModel) conversationPreview() string {
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return ""
	}
	item := m.items[m.cursor]
	ref := item.FirstRef()

	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	accent := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#38BDF8"))
	highlight := lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))
	selectedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#38BDF8")).Bold(true)

	var sb strings.Builder
	modeLabel := "conversation"
	if m.previewMode == pickerPreviewArtifacts {
		modeLabel = "artifacts"
	}
	sb.WriteString(accent.Render("CONVERSATION") + "  " + highlight.Render(item.Item.Label) + "  " + dim.Render("["+modeLabel+"]") + "\n\n")
	if ref.Preview != "" {
		for _, line := range strings.Split(ref.Preview, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			sb.WriteString(line + "\n")
		}
	}
	if len(item.ConversationArtifacts) == 0 {
		sb.WriteString("\n" + dim.Render("(no urls/files/images/changes in this turn)"))
		return sb.String()
	}

	if m.previewMode == pickerPreviewConversation {
		sb.WriteString("\n" + accent.Render("ARTIFACT SUMMARY") + "\n")
		for _, artifact := range item.ConversationArtifacts {
			label := artifact.Label
			if label == "" {
				label = artifact.URL
			}
			if len(label) > max(m.previewWidth()-10, 20) {
				label = label[:max(m.previewWidth()-13, 17)] + "..."
			}
			sb.WriteString(dim.Render(fmt.Sprintf("  [%s] %s", strings.ToUpper(artifact.Category), label)) + "\n")
		}
		sb.WriteString("\n" + dim.Render("tab:artifacts mode  ↵:jump to turn"))
		return sb.String()
	}

	sb.WriteString("\n" + accent.Render("ARTIFACTS") + "\n")
	for i, artifact := range item.ConversationArtifacts {
		label := artifact.Label
		if label == "" {
			label = artifact.URL
		}
		if len(label) > max(m.previewWidth()-10, 20) {
			label = label[:max(m.previewWidth()-13, 17)] + "..."
		}
		cursor := "  "
		if i == m.artifactCursor {
			cursor = "> "
		}
		mark := "  "
		if m.artifactSelected[i] {
			mark = "* "
		}
		line := fmt.Sprintf("%s%s[%s] %s", cursor, mark, strings.ToUpper(artifact.Category), label)
		if i == m.artifactCursor {
			sb.WriteString(selectedStyle.Render(line) + "\n")
		} else {
			sb.WriteString(dim.Render(line) + "\n")
		}
	}
	selectedCount := len(m.artifactSelected)
	if selectedCount > 0 {
		sb.WriteString("\n" + dim.Render(fmt.Sprintf("%d selected", selectedCount)))
	}
	sb.WriteString("\n" + dim.Render("tab:conversation mode  j/k:artifact nav  sp:select  o:open  e:edit local  ↵:jump to turn"))
	return sb.String()
}

// RunPicker launches the interactive picker and returns the result. openerCfg
// controls how selected URLs open (shared with the TUI via open.command_template).
// ctx lets the picker re-extract items when the user presses `R` (refresh).
func RunPicker(kind string, items []PickerItem, openerCfg opener.Config, ctx pickerContext) (*PickerResult, error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("no %s found in session", kind)
	}
	model := newPickerModel(kind, items, openerCfg, ctx)
	p := tea.NewProgram(model, tea.WithAltScreen())
	finalModel, err := p.Run()
	// Clear any Kitty inline images before returning so they don't linger
	// in the main shell screen.
	if kitty.Supported() {
		fmt.Print(kitty.ClearImages())
	}
	if err != nil {
		return nil, err
	}
	m := finalModel.(pickerModel)
	return m.result, nil
}
