//go:build race

package tui

// raceEnabled: the race detector slows the package several-fold, so
// wall-clock budget tests skip themselves under it.
const raceEnabled = true
