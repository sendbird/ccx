#!/bin/bash
# Fully automated ccx demo GIF recording via tmux + asciinema + agg
#
# How it works:
#   1. Start asciinema recording a new tmux session running ccx
#   2. Send keystrokes via `tmux send-keys` with delays
#   3. Kill the tmux session to stop recording
#   4. Convert .cast → .gif with agg
#
# Usage:
#   ./docs/record-demos.sh [browse|conversation|command|views|actions|sandbox|all]

set -e
cd "$(dirname "$0")/.."

OUTDIR="docs/gifs"
mkdir -p "$OUTDIR"

make build 2>/dev/null || true
CCX="$(pwd)/bin/ccx"
[ -x "$CCX" ] || CCX="$(which ccx 2>/dev/null || true)"
[ -x "$CCX" ] || { echo "Error: ccx not found"; exit 1; }

SESS="ccx-demo"

# Key constants for tmux send-keys
DOWN=Down
UP=Up
RIGHT=Right
LEFT=Left

send() { tmux send-keys -t "$SESS" "$@"; }
wait_ms() { sleep "$(echo "scale=3; $1/1000" | bc)"; }

record() {
    local name="$1" desc="$2"
    local cast="$OUTDIR/${name}.cast" gif="$OUTDIR/${name}.gif"

    echo "=== $name: $desc ==="

    # Kill any leftover session
    tmux kill-session -t "$SESS" 2>/dev/null || true

    # Start tmux session with ccx, recording via asciinema
    tmux new-session -d -s "$SESS" -x 120 -y 35 \
        "TERM=xterm-256color asciinema rec '$cast' -c '$CCX' --cols 120 --rows 35 --overwrite; exit"

    # Wait for ccx to load
    sleep 4
}

finish() {
    local name="$1"
    local cast="$OUTDIR/${name}.cast" gif="$OUTDIR/${name}.gif"

    # Quit ccx
    send q
    sleep 1

    # Kill tmux session
    tmux kill-session -t "$SESS" 2>/dev/null || true
    sleep 0.5

    # Convert to GIF
    if [ -f "$cast" ] && [ "$(wc -l < "$cast")" -gt 5 ]; then
        agg "$cast" "$gif" --theme dracula --font-size 16 --speed 1.5 --last-frame-duration 1
        echo "  → $gif ($(du -h "$gif" | cut -f1))"
    else
        echo "  ✗ Recording failed (cast too small)"
    fi
}

# ─── Scenario 1: Session browsing with preview ───
do_browse() {
    record "01-browse" "Session browsing with preview"

    # Navigate sessions
    send $DOWN; wait_ms 400
    send $DOWN; wait_ms 400
    send $DOWN; wait_ms 400
    send $DOWN; wait_ms 400

    # Open preview
    send $RIGHT; wait_ms 1500

    # Scroll preview
    send $DOWN; wait_ms 300
    send $DOWN; wait_ms 300
    send $DOWN; wait_ms 300

    # Close preview
    send $LEFT; wait_ms 500

    # Cycle group modes (Tab)
    send Tab; wait_ms 1200
    send Tab; wait_ms 1200
    send Tab; wait_ms 1200

    # Search
    send /; wait_ms 300
    send -l "ccx"; wait_ms 800
    send Enter; wait_ms 1000
    send Escape; wait_ms 500

    finish "01-browse"
}

# ─── Scenario 2: Conversation drill-down ───
do_conversation() {
    record "02-conversation" "Conversation drill-down"

    # Open session
    send Enter; wait_ms 2000

    # Navigate messages
    send $DOWN; wait_ms 400
    send $DOWN; wait_ms 400
    send $DOWN; wait_ms 400

    # Focus preview
    send $RIGHT; wait_ms 800

    # Navigate and fold blocks
    send $DOWN; wait_ms 300
    send $DOWN; wait_ms 300
    send $DOWN; wait_ms 300
    send $LEFT; wait_ms 500
    send $RIGHT; wait_ms 500

    # Switch preview mode
    send Tab; wait_ms 1000
    send Tab; wait_ms 1000

    # Back
    send Escape; wait_ms 500

    finish "02-conversation"
}

# ─── Scenario 3: Command mode ───
do_command() {
    record "03-command" "Command mode navigation"

    # Command mode → stats
    send -l ":"; wait_ms 800
    send -l "view:stats"; wait_ms 600
    send Enter; wait_ms 2000

    # Stats page jump
    send -l ":"; wait_ms 500
    send -l "page:tools"; wait_ms 600
    send Enter; wait_ms 1500

    # Command mode → config
    send -l ":"; wait_ms 500
    send -l "view:config"; wait_ms 600
    send Enter; wait_ms 2000

    # Filter config
    send -l ":"; wait_ms 500
    send -l "page:hooks"; wait_ms 600
    send Enter; wait_ms 1500

    # Back to sessions
    send -l ":"; wait_ms 500
    send -l "view:sess"; wait_ms 600
    send Enter; wait_ms 1000

    finish "03-command"
}

# ─── Scenario 4: Views tour (stats, config, plugins) ───
do_views() {
    record "04-views" "Stats, config, and plugins views"

    # Open views menu
    send v; wait_ms 800
    # Go to stats
    send s; wait_ms 2000

    # Stats detail page
    send p; wait_ms 500
    send t; wait_ms 1500
    # Back to overview
    send Escape; wait_ms 800

    # Views → config
    send v; wait_ms 500
    send c; wait_ms 2000

    # Navigate config items
    send $DOWN; wait_ms 300
    send $DOWN; wait_ms 300
    send $DOWN; wait_ms 300

    # Open preview
    send $RIGHT; wait_ms 1500

    # Back
    send Escape; wait_ms 500

    # Views → plugins
    send v; wait_ms 500
    send p; wait_ms 2000

    # Navigate plugins
    send $DOWN; wait_ms 400
    send $DOWN; wait_ms 400
    send $DOWN; wait_ms 400
    wait_ms 1000

    # Back to sessions
    send v; wait_ms 500
    send Enter; wait_ms 1000

    finish "04-views"
}

# ─── Scenario 5: Actions — URLs and files ───
do_actions() {
    record "05-actions" "URL and file extraction"

    # Actions menu
    send x; wait_ms 800

    # URLs
    send u; wait_ms 2000

    # Search
    send /; wait_ms 300
    send -l "github"; wait_ms 800
    send Escape; wait_ms 500

    # Multi-select
    send Space; wait_ms 300
    send $DOWN; wait_ms 200
    send Space; wait_ms 300

    # Copy
    send y; wait_ms 1000

    # Actions → files
    send x; wait_ms 800
    send f; wait_ms 2000

    # Close
    send Escape; wait_ms 500

    finish "05-actions"
}

# ─── Scenario 6: Config/plugin sandbox testing ───
# NOTE: The sandbox uses tmux display-popup which asciinema cannot capture.
# This scenario records the selection + launch steps only.
# For the full popup interaction, record manually:
#   asciinema rec docs/gifs/06-sandbox.cast -c ccx --cols 120 --rows 35
#   agg docs/gifs/06-sandbox.cast docs/gifs/06-sandbox.gif --theme dracula --font-size 16
do_sandbox() {
    record "06-sandbox" "Config sandbox — select and launch"

    # Go to config view
    send v; wait_ms 800
    send c; wait_ms 2000

    # Navigate config items
    send $DOWN; wait_ms 300
    send $DOWN; wait_ms 300
    send $DOWN; wait_ms 300

    # Multi-select with Space
    send Space; wait_ms 400
    send $DOWN; wait_ms 200
    send Space; wait_ms 400
    send $DOWN; wait_ms 200
    send Space; wait_ms 600

    # Open actions — shows t:test option
    send x; wait_ms 1500

    # Cancel (don't actually launch popup — it can't be captured)
    send Escape; wait_ms 500

    # Clear selection
    send Escape; wait_ms 500

    # Show plugins view too
    send v; wait_ms 800
    send p; wait_ms 2000

    # Navigate and select plugin
    send $DOWN; wait_ms 300
    send $DOWN; wait_ms 300
    send Space; wait_ms 400

    # Show actions with t:test
    send x; wait_ms 1500
    send Escape; wait_ms 500

    finish "06-sandbox"
}

case "${1:-all}" in
    browse)       do_browse ;;
    conversation) do_conversation ;;
    command)      do_command ;;
    views)        do_views ;;
    actions)      do_actions ;;
    sandbox)      do_sandbox ;;
    all)
        do_browse
        do_conversation
        do_command
        do_views
        do_actions
        do_sandbox
        echo ""
        echo "=== All done ==="
        ls -lh "$OUTDIR"/*.gif 2>/dev/null
        ;;
    *)
        echo "Usage: $0 [browse|conversation|command|views|actions|all]"
        exit 1
        ;;
esac
