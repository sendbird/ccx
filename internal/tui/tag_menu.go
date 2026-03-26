package tui

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
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

	// Get current session badges
	var currentBadges map[string]bool
	if sess, ok := a.sessionByID(a.tagSessID); ok {
		currentBadges = make(map[string]bool)
		for _, b := range sess.CustomBadges {
			currentBadges[b] = true
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
	title := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#7C3AED")).
		Bold(true).
		Render("Manage Tags")

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

func (a *App) handleTagMenuKey(key string) {
	if key == "esc" {
		a.tagMenu = false
		a.tagInput.SetValue("")
		return
	}

	if key == "up" || key == "k" {
		if a.tagCursor > 0 {
			a.tagCursor--
		}
		return
	}

	if key == "down" || key == "j" {
		if a.tagCursor < len(a.tagList)-1 {
			a.tagCursor++
		}
		return
	}

	if key == "enter" {
		inputVal := strings.TrimSpace(a.tagInput.Value())

		// If input field has text, create new badge
		if inputVal != "" {
			if !a.validateBadgeName(inputVal) {
				// Invalid name, ignore
				return
			}

			// Get current session
			sess, ok := a.sessionByID(a.tagSessID)
			if !ok {
				return
			}

			// Check if badge already exists on session
			hasBadge := false
			for _, b := range sess.CustomBadges {
				if b == inputVal {
					hasBadge = true
					break
				}
			}

			if !hasBadge && len(sess.CustomBadges) >= maxBadgesPerSession {
				// Max badges reached, ignore
				return
			}

			// Toggle badge
			var updated []string
			if hasBadge {
				// Remove
				for _, b := range sess.CustomBadges {
					if b != inputVal {
						updated = append(updated, b)
					}
				}
			} else {
				// Add
				updated = append(sess.CustomBadges, inputVal)
			}

			// Sort badges
			sort.Strings(updated)

			// Save to store
			a.badgeStore.Set(a.tagSessID, updated)
			_ = a.badgeStore.Save()

			// Update session in list
			a.updateSessionBadges(a.tagSessID, updated)

			// Refresh tag list
			a.tagList = a.badgeStore.AllBadges()
			a.tagInput.SetValue("")
			a.tagCursor = 0

			return
		}

		// Otherwise, toggle selected badge
		if a.tagCursor >= 0 && a.tagCursor < len(a.tagList) {
			badgeName := a.tagList[a.tagCursor]

			// Get current session
			sess, ok := a.sessionByID(a.tagSessID)
			if !ok {
				return
			}

			// Check if session has this badge
			hasBadge := false
			for _, b := range sess.CustomBadges {
				if b == badgeName {
					hasBadge = true
					break
				}
			}

			// Toggle
			var updated []string
			if hasBadge {
				// Remove
				for _, b := range sess.CustomBadges {
					if b != badgeName {
						updated = append(updated, b)
					}
				}
			} else {
				// Add (check limit)
				if len(sess.CustomBadges) >= maxBadgesPerSession {
					return
				}
				updated = append(sess.CustomBadges, badgeName)
			}

			// Sort badges
			sort.Strings(updated)

			// Save to store
			a.badgeStore.Set(a.tagSessID, updated)
			_ = a.badgeStore.Save()

			// Update session in list
			a.updateSessionBadges(a.tagSessID, updated)
		}

		return
	}

	// Forward other keys to text input
	a.tagInput, _ = a.tagInput.Update(textinput.KeyMsg(key))
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
