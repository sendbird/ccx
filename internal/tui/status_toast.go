package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/keyolk/tuikit/toast"
)

// statusSource is the one toast source ccx uses, so a newer status message
// replaces the shown one instead of stacking (the footer shows one line).
const statusSource = "status"

// toastStyles colors the footer toast. Each line also carries a level prefix
// (tuikit/toast), so an error still reads as one under NO_COLOR.
var toastStyles = toast.Styles{
	Info:     lipgloss.NewStyle().Foreground(colorAccent).Bold(true),
	Success:  lipgloss.NewStyle().Foreground(colorAccent).Bold(true),
	Warn:     lipgloss.NewStyle().Foreground(colorAccent).Bold(true),
	Error:    lipgloss.NewStyle().Foreground(colorError).Bold(true),
	Progress: lipgloss.NewStyle().Foreground(colorAccent),
}

// flash shows text as the status toast; "" clears it. The level is read off
// the text, since the call sites predate levels: a failure stays until a key
// acknowledges it, an ellipsis-terminated "doing X…" stays until the next
// status replaces it, and anything else expires on its own.
func (a *App) flash(text string) {
	if text == "" {
		a.toasts = toast.Dismiss(a.toasts, statusSource)
		return
	}
	a.toasts = toast.Add(a.toasts, toast.Toast{Source: statusSource, Text: text, Level: flashLevel(text)})
}

// flashAppend extends the shown status ("Copied" → "Copied 2 paths").
func (a *App) flashAppend(suffix string) {
	a.flash(a.flashText() + suffix)
}

// flashText is the shown status text, "" when none.
func (a *App) flashText() string {
	t, _ := a.statusToast()
	return t.Text
}

func (a *App) statusToast() (toast.Toast, bool) {
	items := a.toasts.Items()
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Source == statusSource {
			return items[i], true
		}
	}
	return toast.Toast{}, false
}

func flashLevel(text string) toast.Level {
	lower := strings.ToLower(text)
	switch {
	case strings.HasPrefix(lower, "error"), strings.Contains(lower, "failed"), strings.Contains(lower, "error:"):
		return toast.Error
	case strings.HasSuffix(text, "…"), strings.HasSuffix(text, "..."):
		return toast.Progress
	}
	return toast.Info
}
