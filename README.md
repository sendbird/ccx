# ccx — Claude Code Explorer

A terminal UI for browsing, inspecting, and managing [Claude Code](https://docs.anthropic.com/en/docs/claude-code) sessions.

ccx gives you a full-cycle view of what Claude Code has been doing across all your projects — browse sessions, read conversations, inspect tool calls, view agent hierarchies, and get aggregated stats.

![demo](demo.gif)

## Install

```bash
go install github.com/sendbird/ccx@latest
```

Or build from source:

```bash
git clone https://github.com/sendbird/ccx.git
cd ccx
make build      # -> bin/ccx
make install    # -> ~/.local/bin/ccx
```

## Usage

```bash
ccx              # launch TUI
ccx --version    # print version
```

## Features

### Session Browser

Browse all Claude Code sessions across projects, sorted by recency.

- **Live/Busy badges** — see which sessions are actively running
- **Search** (`/`) — filter by project, branch, prompt, or tags (`is:live`, `is:team`, `team:name`)
- **Search highlighting** — matched terms highlighted in session list and conversation view
- **Group modes** (`G` to cycle):
  - **Flat** — simple list sorted by time
  - **Project** — clustered by project path, children show branch name
  - **Tree** — team hierarchy with leader/teammate nesting
- **Directory filter** (`g`) — scope to a single project directory
- **Session preview** (`Tab`) — inline conversation preview with fold/unfold
- **Global stats** (`S`) — aggregated metrics across all sessions

### Conversation View

Drill into any session to read the full conversation as a split-pane view.

- **Message list** with role, timestamp, index range, and text preview
- **Split-pane preview** (`Tab`/`→`) — foldable message detail on the right
- **Full conversation** (`c`) — scrollable view of all messages concatenated
- **Agent drill-down** (`Enter` on agent) — opens agent sub-session in the same split view, with recursive navigation
- **Agent preview** — fold/unfold agent content blocks in the preview pane
- **Search** (`/`) — filter messages with highlighted matches
- **Live tail** (`L`) — auto-follow active sessions in real-time
- **Edit** (`e`) — open session file in editor

### Detail View

Full-screen message viewer with block-level navigation.

- **Block cursor** (`↑`/`↓`) — navigate between text, tool calls, and results
- **Fold/unfold** (`←`/`→`) — collapse/expand individual blocks
- **Bulk fold** (`f`/`F`) — fold/unfold all blocks at once
- **Message navigation** (`n`/`N`) — step through messages
- **Copy mode** (`v`) — select and copy text ranges
- **Pager** (`o`) — open in external pager

### Live Session Integration

- **Live modal** (`L` in session view) — real-time pane capture of active sessions
- **Send input** (`I`) — inline prompt to send text to a running Claude Code session via tmux
- **Jump to pane** (`J`) — switch to the tmux pane running the session

### Session Actions (`x` menu)

Press `x` to open the actions menu, then pick an action:

- **d** — delete session
- **m** — move session to a different project directory
- **w** — create a git worktree from session
- **r** — resume session in Claude Code

## Keybindings

### Sessions

| Key | Action |
|-----|--------|
| `Enter` | Open conversation view |
| `/` | Search/filter sessions |
| `g` | Filter by project directory |
| `G` | Cycle group mode (flat/project/tree) |
| `Tab` | Toggle/cycle session preview |
| `[` / `]` | Adjust split pane ratio |
| `x` | Actions menu (delete/move/worktree/resume) |
| `L` | Open live modal for active session |
| `I` | Send input to live session (tmux) |
| `J` | Jump to tmux pane |
| `R` | Manual refresh |
| `S` | Global stats |

### Conversation

| Key | Action |
|-----|--------|
| `Enter` | Open message detail / drill into agent |
| `c` | Full conversation view (all messages) |
| `/` | Search/filter messages |
| `Tab` / `→` | Toggle/focus preview pane |
| `↑` / `↓` | Navigate blocks (when preview focused) |
| `←` / `→` | Fold/unfold blocks |
| `[` / `]` | Adjust split pane ratio |
| `L` | Toggle live tail |
| `R` | Manual refresh |
| `e` | Open in editor |
| `I` | Send input to live session (tmux) |
| `J` | Jump to tmux pane |

### Detail View

| Key | Action |
|-----|--------|
| `↑` / `↓` | Navigate blocks |
| `←` / `→` | Fold/unfold block |
| `f` / `F` | Fold/unfold all |
| `n` / `N` | Next/prev message |
| `v` | Copy mode |
| `y` | Copy all to clipboard |
| `o` | Open in pager |

### Global

| Key | Action |
|-----|--------|
| `Esc` | Go back / close |
| `q` | Quit |

## How It Works

ccx reads Claude Code's session files from `~/.claude/projects/`. Each session is a JSONL file containing the full conversation history — user prompts, assistant responses, tool calls, and results.

The TUI is built with [Bubble Tea](https://github.com/charmbracelet/bubbletea) and [Lip Gloss](https://github.com/charmbracelet/lipgloss).

## Requirements

- Go 1.25+
- Claude Code sessions in `~/.claude/projects/`

## License

MIT
