package tui

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	maxBadgesPerSession = 10
	maxBadgeNameLen     = 20
)

var badgeNamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

type tagMenuItem struct {
	name   string
	hasTag bool
	count  int // how many sessions use this tag
}

func (a *App) renderTagMenu() string {
	if !a.tagMenu {
		return ""
	}

	isMultiSelect := len(a.tagSessIDs) > 0

	// Get current session badges (union of all selected sessions for multi-select)
	var currentBadges map[string]bool
	if isMultiSelect {
		// For multi-select, show all badges that ANY selected session has
		currentBadges = make(map[string]bool)
		for _, sessID := range a.tagSessIDs {
			if sess, ok := a.sessionByID(sessID); ok {
				for _, b := range sess.sess.CustomBadges {
					currentBadges[b] = true
				}
			}
		}
	} else {
		// Single session
		if sess, ok := a.sessionByID(a.tagSessID); ok {
			currentBadges = make(map[string]bool)
			for _, b := range sess.sess.CustomBadges {
				currentBadges[b] = true
			}
		}
	}

	// Build tag menu items
	var items []tagMenuItem
	for _, badge := range a.tagList {
		items = append(items, tagMenuItem{
			name:   badge,
			hasTag: currentBadges[badge],
			count:  a.badgeStore.CountBadgeUsage(badge),
		})
	}

	// Title
	titleText := "Manage Tags"
	if isMultiSelect {
		titleText = fmt.Sprintf("Manage Tags (%d sessions)", len(a.tagSessIDs))
	}
	title := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#7C3AED")).
		Bold(true).
		Render(titleText)

	// Render list
	var lines []string
	lines = append(lines, title)
	lines = append(lines, "")

	if len(items) == 0 {
		lines = append(lines, dimStyle.Render("  No tags yet. Create one below."))
	} else {
		for i, item := range items {
			cursor := "  "
			if i == a.tagCursor {
				cursor = "> "
			}

			check := "[ ]"
			if item.hasTag {
				check = "[✓]"
			}

			usageInfo := ""
			if item.count > 0 {
				usageInfo = dimStyle.Render(fmt.Sprintf(" (%d)", item.count))
			}

			line := cursor + check + " " + item.name + usageInfo
			if i == a.tagCursor {
				line = selectedStyle.Render(line)
			}
			lines = append(lines, line)
		}
	}

	lines = append(lines, "")
	lines = append(lines, dimStyle.Render("  Create new:"))
	lines = append(lines, "  "+a.tagInput.View())
	lines = append(lines, "")
	lines = append(lines, helpStyle.Render("  enter:toggle  esc:close  ↑↓:navigate"))

	content := strings.Join(lines, "\n")

	// Box styling
	width := 50
	height := len(lines) + 2
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#7C3AED")).
		Padding(1).
		Width(width).
		Height(height)

	return lipgloss.Place(
		a.width,
		a.height,
		lipgloss.Center,
		lipgloss.Center,
		box.Render(content),
	)
}

func (a *App) handleTagMenuKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	if key == "esc" {
		a.tagMenu = false
		a.tagInput.SetValue("")
		// Clear multi-select state
		if len(a.tagSessIDs) > 0 {
			a.clearMultiSelection()
			a.tagSessIDs = nil
		}
		a.tagSessID = ""
		return a, nil
	}

	// If input is focused, handle navigation specially
	if a.tagInput.Focused() {
		switch key {
		case "up":
			// Move focus to list (bottom item)
			a.tagInput.Blur()
			if len(a.tagList) > 0 {
				a.tagCursor = len(a.tagList) - 1
			}
			return a, nil
		case "down":
			// Move focus to list (top item)
			a.tagInput.Blur()
			if len(a.tagList) > 0 {
				a.tagCursor = 0
			}
			return a, nil
		case "enter":
			// Handle enter below (creating badge)
		default:
			// Forward all other keys to input (allows typing j/k/h/l/etc)
			var cmd tea.Cmd
			a.tagInput, cmd = a.tagInput.Update(msg)
			return a, cmd
		}
	} else {
		// List is focused - handle navigation
		switch key {
		case "up", "k":
			if a.tagCursor > 0 {
				a.tagCursor--
			} else {
				// Wrap to bottom
				if len(a.tagList) > 0 {
					a.tagCursor = len(a.tagList) - 1
				}
			}
			return a, nil
		case "down", "j":
			if a.tagCursor < len(a.tagList)-1 {
				a.tagCursor++
			} else {
				// Wrap to top
				a.tagCursor = 0
			}
			return a, nil
		case "enter":
			// Handle enter below (toggling badge)
		default:
			// Other keys do nothing when list is focused
			return a, nil
		}
	}

	if key == "enter" {
		inputVal := strings.TrimSpace(a.tagInput.Value())
		isMultiSelect := len(a.tagSessIDs) > 0

		// Get target session IDs
		var targetSessIDs []string
		if isMultiSelect {
			targetSessIDs = a.tagSessIDs
		} else {
			targetSessIDs = []string{a.tagSessID}
		}

		// If input field has text, create new badge
		if inputVal != "" {
			if !a.validateBadgeName(inputVal) {
				return a, nil
			}

			// Apply to all target sessions
			for _, sessID := range targetSessIDs {
				sess, ok := a.sessionByID(sessID)
				if !ok {
					continue
				}

				// Check if badge already exists
				hasBadge := false
				for _, b := range sess.sess.CustomBadges {
					if b == inputVal {
						hasBadge = true
						break
					}
				}

				// Skip if max limit reached and badge doesn't exist
				if !hasBadge && len(sess.sess.CustomBadges) >= maxBadgesPerSession {
					continue
				}

				// Toggle badge
				var updated []string
				if hasBadge {
					// Remove
					for _, b := range sess.sess.CustomBadges {
						if b != inputVal {
							updated = append(updated, b)
						}
					}
				} else {
					// Add
					updated = append(sess.sess.CustomBadges, inputVal)
				}

				// Sort and save
				sort.Strings(updated)
				a.badgeStore.Set(sessID, updated)
				a.updateSessionBadges(sessID, updated)
			}

			_ = a.badgeStore.Save()
			a.tagList = a.badgeStore.AllBadges()
			a.tagInput.SetValue("")
			a.tagCursor = 0

			return a, nil
		}

		// Otherwise, toggle selected badge from list
		if a.tagCursor >= 0 && a.tagCursor < len(a.tagList) {
			badgeName := a.tagList[a.tagCursor]

			// Apply to all target sessions
			for _, sessID := range targetSessIDs {
				sess, ok := a.sessionByID(sessID)
				if !ok {
					continue
				}

				// Check if session has this badge
				hasBadge := false
				for _, b := range sess.sess.CustomBadges {
					if b == badgeName {
						hasBadge = true
						break
					}
				}

				// Toggle
				var updated []string
				if hasBadge {
					// Remove
					for _, b := range sess.sess.CustomBadges {
						if b != badgeName {
							updated = append(updated, b)
						}
					}
				} else {
					// Add (check limit)
					if len(sess.sess.CustomBadges) >= maxBadgesPerSession {
						continue
					}
					updated = append(sess.sess.CustomBadges, badgeName)
				}

				// Sort and save
				sort.Strings(updated)
				a.badgeStore.Set(sessID, updated)
				a.updateSessionBadges(sessID, updated)
			}

			_ = a.badgeStore.Save()
		}

		return a, nil
	}

	return a, nil
}

func (a *App) validateBadgeName(name string) bool {
	if len(name) == 0 || len(name) > maxBadgeNameLen {
		return false
	}
	return badgeNamePattern.MatchString(name)
}

func (a *App) sessionByID(id string) (*sessionItem, bool) {
	for _, item := range a.sessionList.VisibleItems() {
		if si, ok := item.(sessionItem); ok {
			if si.sess.ID == id {
				return &si, true
			}
		}
	}
	return nil, false
}

func (a *App) updateSessionBadges(sessionID string, badges []string) {
	// Update the session in the visible list
	items := a.sessionList.Items()
	for i, item := range items {
		if si, ok := item.(sessionItem); ok {
			if si.sess.ID == sessionID {
				si.sess.CustomBadges = badges
				items[i] = si
				break
			}
		}
	}
	a.sessionList.SetItems(items)
}
