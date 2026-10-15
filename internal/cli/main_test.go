package cli

import (
	"context"
	"os"
	"testing"

	tk "github.com/keyolk/tuikit/tmux"

	"github.com/sendbird/ccx/internal/tmux"
)

// TestMain keeps the package off the real tmux server and process table,
// even when the suite runs inside tmux.
func TestMain(m *testing.M) {
	tmux.SetClient(tk.Client{Runner: tk.NewFake()})
	tmux.SetProcSource(func(context.Context) ([]byte, error) { return nil, nil })
	os.Exit(m.Run())
}
