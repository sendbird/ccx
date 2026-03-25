# ccx — Claude Code Explorer

A terminal UI for browsing, inspecting, and managing [Claude Code](https://docs.anthropic.com/en/docs/claude-code) sessions.

Browse sessions, read conversations, inspect tool calls, view agent hierarchies, explore configs/plugins, and get aggregated stats — all from your terminal.

![ccx demo](docs/gifs/01-browse.gif)

> More demos: [conversation](docs/DEMOS.md#conversation), [command mode](docs/DEMOS.md#command-mode), [views](docs/DEMOS.md#views), [URL/file actions](docs/DEMOS.md#actions), [sandbox testing](docs/DEMOS.md#sandbox)

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
ccx                        # launch TUI
ccx -view config           # start in config explorer
ccx -view stats            # start in global stats
ccx -view plugins          # start in plugin explorer
ccx -group tree            # start with tree grouping
ccx -preview stats         # start with stats preview open
ccx -search "is:live"      # start filtered to live sessions
```

### CLI Flags

| Flag | Description |
|------|-------------|
| `-version`, `-v` | Print version and exit |
| `-dir PATH` | Claude data directory (default: `~/.claude`) |
| `-view MODE` | Initial view: `sessions`, `config`, `plugins`, `stats` |
| `-group MODE` | Initial grouping: `flat`, `proj`, `tree`, `chain`, `fork` |
| `-preview MODE` | Initial preview: `conv`, `stats`, `mem`, `tasks` |
| `-search QUERY` | Start with session filter applied |
| `-tmux` | Enable tmux integration (auto-detected) |
| `-tmux-auto-live` | Auto-enter live session in same tmux window |
| `-worktree-dir NAME` | Worktree subdirectory name (default: `.worktree`) |

The Claude data directory is resolved in order: `--dir` flag → `CLAUDE_CONFIG_DIR` env → `~/.claude`.

## Views

### Session Browser

Browse all Claude Code sessions across projects, sorted by recency.

- **Live/Busy badges** — see which sessions are actively running
- **Search** (`/`) — filter by project, branch, prompt, window name, or tags
- **Group modes** (`G` or `:group:*`):
  - **Flat** — simple list sorted by time
  - **Project** — clustered by project path
  - **Tree** — team hierarchy with leader/teammate nesting
  - **Chain** — resume-chain grouping (parent → child)
  - **Fork** — agent-fork grouping
- **Directory filter** (`g`) — scope to a single project directory
- **Preview pane** (`Tab` to cycle): conversation, stats, memory, tasks/plan, live
- **Multi-select** (`Space`) — bulk delete, copy paths, send input
- **Actions menu** (`x`) — delete, move, resume, copy path, worktree, kill, input, jump, URLs, files
- **Command mode** (`:`) — vim-style commands with fuzzy suggestions

#### Search Filters

| Filter | Matches |
|--------|---------|
| `is:live` | Running Claude process |
| `is:busy` | Actively responding |
| `is:wt` | In a git worktree |
| `is:team` | Part of a team session |
| `is:fork` | Forked from another session |
| `has:mem` | Has memory file |
| `has:todo` | Has todos |
| `has:task` | Has tasks |
| `has:plan` | Has plan |
| `has:agent` | Has subagents |
| `has:compact` | Uses message compaction |
| `has:skill` | Used skills |
| `has:mcp` | Used MCP tools |
| `team:NAME` | Filter by team name |
| `win:NAME` | Filter by tmux window name |

Plain text terms match against project path, name, branch, session ID, first prompt, and teammate name. Multiple terms are AND-matched.

### Cross-Session Search

Search inside conversation content across all sessions (`Ctrl+S` or `:search`).

**Search syntax:**
- `word1 word2` — AND match (all terms must appear)
- `"exact phrase"` — Exact phrase matching
- `-exclude` — Exclude terms from results
- `user:` — Only search user messages
- `assistant:` — Only search assistant responses
- `tool:ToolName` — Only search specific tool calls

**Features:**
- Searches text, tool inputs/outputs, thinking blocks
- Results stream in real-time as they're found
- Matched terms are highlighted in snippets
- Press `Enter` to jump directly to the matching message
- Press `/` to edit the query

**Example queries:**
```
database migration                    # Find both terms
"how do I" API                        # Phrase + term
user: error -test                     # User messages with "error", excluding "test"
assistant: "I recommend" -deprecated  # Complex combination
```

### Conversation View

Drill into any session to read the full conversation.

- **Split-pane preview** (`Tab`/`→`) — foldable message detail with three detail levels:
  - **Text** — text blocks only
  - **Tool** — text + tool blocks, hooks hidden
  - **Hook** — text + tool blocks + full hook details
- **Block navigation** (`↑`/`↓`) — navigate text, tool calls, and results
- **Fold/unfold** (`←`/`→`, `f`/`F`) — collapse/expand content blocks
- **System tag folding** — `<system-reminder>`, `<task-notification>`, `<available-deferred-tools>`, etc. are folded by default, expandable on demand
- **Block filter** (`/`) — filter by `is:tool`, `is:hook`, `is:error`, `is:skill`, `tool:Name`
- **Subagent drill-down** (`Enter` on agent) — recursive navigation into sub-sessions with back-stack
- **Side-question context** — background context from parent sessions is collapsed into a summary; only the actual question/answer is shown
- **Full conversation** (`c`) — scrollable concatenated view with search (`/`) and copy mode
- **Live tail** (`L`) — auto-follow active sessions in real-time
- **Send input** (`I`) — send text to running Claude via tmux
- **Jump to pane** (`J`) — switch to the tmux pane running the session

#### Subagent Support

Subagents are displayed inline in the conversation with type badges:

| Type | Badge | Source |
|------|-------|--------|
| `aside_question` | `?` `:btw` | Side-question (background Q&A) |
| `Explore` | `⊕ Explore` | Codebase exploration agent |
| `general-purpose` | `⊕ general-purpose` | Default agent |
| Custom types | `⊕ {type}` | From `agent-*.meta.json` |

Agent type detection: reads `agent-{id}.meta.json` (preferred) or parses type from filename `agent-{type}-{hash}.jsonl`. Auto-compaction files (`agent-acompact-*.jsonl`) are excluded.

Timestamp ordering uses the **last message** in the subagent file (most recent activity), not the first.

### Detail View

Full-screen message viewer with block-level navigation.

- **Block cursor** (`↑`/`↓`) — navigate between blocks
- **Fold/unfold** (`←`/`→`, `f`/`F`) — collapse/expand blocks
- **Message navigation** (`n`/`N`) — step through messages
- **Copy mode** (`v`) — line-by-line selection with anchor/cursor, vim-style navigation
- **Clipboard** (`y`) — copy selected text/blocks to system clipboard
- **Pager** (`o`) — open in external pager

### Global Stats (`v` → `s`)

Aggregated metrics across all sessions with detail drill-down.

- **Overview** — total sessions, messages, tokens, duration, cost
- **Tools** (`p` → `t`) — built-in tool usage with timeline sparklines
- **MCP Tools** (`p` → `m`) — MCP tool usage with error tracking
- **Agents** (`p` → `a`) — agent type breakdown (Explore, general-purpose, etc.)
- **Skills** (`p` → `s`) — skill usage with per-skill error counts
- **Commands** (`p` → `c`) — command usage with per-command error counts
- **Errors** (`p` → `e`) — error breakdown by tool/skill/command category

Metrics tracked per session: token usage (input/output/cache per model), code activity (write/edit/read/bash counts), files touched, tool call timelines, message timing gaps, model switches, compaction events, hook invocations, and turns per request.

### Config Explorer (`v` → `c`)

Browse and manage all Claude Code configuration files.

- **Category filter** (`Tab`) — global, project, local, skills, agents, commands, MCP, hooks, enterprise
- **Split preview** — file content with syntax awareness
- **Multi-select** (`Space`) — select configs for testing
- **Test env** (`t`) — launch isolated Claude session with only selected configs
- **Edit** (`e` / `Enter`) — open in `$EDITOR`
- **Actions menu** (`x`) — edit, copy path, open shell at path

Categories discovered:
- **Global** — `~/.claude/CLAUDE.md` + memory, contexts, rules (with `@reference` walking)
- **Project** — project-level `CLAUDE.md` + memory from `projects/{encoded}/memory/`
- **Local** — parent CLAUDE.md files found by walking up from project directory
- **Skills/Agents/Commands** — plugin component configs
- **MCP** — MCP server configurations
- **Hooks** — hook definitions
- **Enterprise** — managed enterprise settings

#### Config Test Environment

The test environment (`t` key) creates an isolated Claude Code session with only the selected configs active:

1. Creates a temporary `HOME` directory
2. Symlinks only the selected memory/config files
3. Preserves editor config (`.config/`, shell dotfiles)
4. Extracts OAuth credentials from macOS keychain for connector MCP access
5. Launches `claude` with the isolated environment
6. Supports git worktree detection

This lets you test specific config combinations without affecting your main setup.

### Plugin Explorer (`v` → `p`)

Browse installed Claude Code plugins and their components.

- **Component drill-down** (`Enter`) — view plugin agents, skills, commands, hooks, MCP servers
- **Multi-select** (`Space`) — select components for batch editing
- **Edit** (`e`) — open component files in `$EDITOR`
- **Actions menu** (`x`) — edit, copy path, open shell
- **Component badges** — e.g. `[3a 2s 1c]` = 3 agents, 2 skills, 1 command
- **Status badges** — DISABLED, BLOCKED (with reasons from blocklist)

Plugin discovery reads from:
- `installed_plugins.json` — install paths and versions
- `blocklist.json` — blocked plugins with reasons
- `known_marketplaces.json` — marketplace metadata (git/github sources)
- `settings.json` — `enabledPlugins` list
- `.claude-plugin/` — component directories per plugin

Component types: agents (`.md`), skills (`.md`), commands (`.md`), hooks (`.py`/`.sh`), MCP servers (`.json`), LSP servers, scripts, settings, memory, references.

#### Plugin Test Environment

Multi-select plugin components and press `t` to launch an isolated Claude session with only the selected plugins active. Uses the same isolated HOME mechanism as the config test environment.

## Keybindings

### Sessions

| Key | Action |
|-----|--------|
| `Enter` | Open conversation view |
| `/` | Search/filter sessions |
| `g` | Filter by project directory |
| `G` | Cycle group mode |
| `Tab` | Cycle preview mode |
| `Shift+Tab` | Reverse cycle preview |
| `→` | Open/focus preview |
| `←` | Close/unfocus preview |
| `[` / `]` | Adjust split ratio |
| `Space` | Multi-select toggle |
| `x` | Actions menu (delete, move, resume, URLs, files, ...) |
| `v` | Views menu (stats/config/plugins) |
| `:` | Command mode |
| `Ctrl+S` | Cross-session search |
| `L` | Live preview (tmux) |
| `I` | Send input to live session |
| `J` | Jump to tmux pane |
| `R` | Refresh |
| `S` | Global stats |
| `?` | Help |
| `q` | Quit |

### Conversation

| Key | Action |
|-----|--------|
| `Enter` | Open detail / drill into agent |
| `c` | Full conversation view |
| `/` | Filter blocks |
| `Tab` | Cycle preview detail (text/tool/hook) |
| `↑` / `↓` | Navigate messages/blocks |
| `←` / `→` | Fold/unfold blocks |
| `f` / `F` | Fold/unfold all |
| `[` / `]` | Adjust split ratio |
| `L` | Toggle live tail |
| `I` | Send input |
| `J` | Jump to pane |
| `x` | Actions menu (URLs, files) |
| `e` | Edit menu (session/agent JSONL, text export) |
| `u` | URL extraction (scoped to message/session) |
| `R` | Refresh |
| `Esc` | Back to sessions / close preview |

### Detail View

| Key | Action |
|-----|--------|
| `↑` / `↓` | Navigate blocks |
| `←` / `→` | Fold/unfold block |
| `f` / `F` | Fold/unfold all |
| `n` / `N` | Next/prev message |
| `v` | Copy mode |
| `y` | Copy to clipboard |
| `x` | Actions menu (URLs, files) |
| `o` | Open in pager |

### Command Mode (`:`)

Available from any view. Suggestions are context-aware — only relevant commands appear.

| Command | View | Action |
|---------|------|--------|
| `view:sessions` | All | Switch to session browser |
| `view:stats` | All | Open global stats |
| `view:stats:tools` | All | Stats → tools detail |
| `view:config` | All | Open config explorer |
| `view:config:hooks` | All | Config → hooks filter |
| `view:plugins` | All | Open plugin explorer |
| `group:flat\|proj\|tree\|chain\|fork` | Sessions | Change grouping mode |
| `preview:conv\|stats\|mem\|tasks\|live` | Sessions | Change preview mode |
| `set:ratio N` | Sessions | Set split pane ratio (15-85) |
| `page:memory\|hooks\|mcp\|skills\|...` | Config | Filter config category |
| `page:tools\|errors\|overview` | Stats | Switch stats page |
| `refresh` | Sessions | Reload sessions |
| `search` | All | Cross-session content search |
| `config:edit` | All | Edit config file |
| `detail:text\|tool\|hook` | Conversation | Set detail level |

Short aliases: `g:flat`, `v:stats`, `p:hooks`, `cfg:edit`. Multi-command: `view:config page:hooks`.

### Conversation / Detail

| Key | Action |
|-----|--------|
| `x` | Actions menu (URLs, files) |
| `e` | Edit menu (session, agent, text export) |

### Global

| Key | Action |
|-----|--------|
| `Esc` | Go back / close |
| `q` | Quit |

## Configuration

Config file: `~/.config/ccx/config.yaml` (bootstrap with `:config:edit`)

The config file contains three sections:

### Keybindings

```yaml
session:
  quit: q
  open: enter
  actions: x
  # ... see :config:edit for all options
actions:
  delete: d
  import_mem: M
  remove_mem: X
```

### Preferences (auto-saved on quit)

```yaml
preferences:
  group_mode: flat          # flat|proj|tree|chain|fork
  preview_mode: stats       # conv|stats|mem|tasks|live
  view_mode: sessions       # sessions|config|plugins|stats
  conv_detail_level: 1      # 0=text, 1=tool, 2=hook
  split_ratio: 35           # 15-85
  worktree_dir: .worktree   # git worktree subdirectory name
```

### Number Key Shortcuts

Number keys `1-9` trigger commands based on the active view and split focus side.
Configure in the `shortcuts` section:

```yaml
shortcuts:
  sessions:
    left:                     # session list focused
      "1": "preview:conv"
      "2": "preview:stats"
      "3": "preview:mem"
      "4": "preview:tasks"
      "5": "preview:live"
    right:                    # preview pane focused
      "1": "some:command"
  conversation:
    left:                     # message list focused
      "1": "detail:text"
      "2": "detail:tool"
      "3": "detail:hook"
  config:
    left:
      "1": "page:overview"
      "2": "page:memory"
      "3": "page:project"
      "4": "page:skills"
      "5": "page:hooks"
      "6": "page:mcp"
  stats:
    left:
      "1": "page:overview"
      "2": "page:tools"
      "3": "page:errors"
```

Values are command names from the command registry (`:` command mode).
User config merges over defaults — override specific keys or add new views.

## Development

### Build

```bash
make build      # build binary → bin/ccx
make run        # build + run
make install    # build + install to ~/.local/bin/ccx
make test       # run all tests
make vet        # go vet
make tidy       # go mod tidy
make clean      # remove build artifacts
```

Version is injected via `-ldflags` from `git describe --tags --always --dirty`.

### Debug

```bash
CCX_DEBUG=1 ccx    # enables debug logging to /tmp/ccx-debug.log
```

### Recording Demo GIFs

```bash
# Prerequisites: brew install asciinema agg
./docs/record-demos.sh all       # record all 6 demos
./docs/record-demos.sh browse    # record just one
```

Uses tmux + asciinema + agg for fully automated terminal recording.

### Testing

```bash
go test ./internal/...                                    # run all tests
go test ./internal/tui/ -run TestRender                   # run render snapshot tests
UPDATE_GOLDEN=1 go test ./internal/tui/ -run TestRender   # regenerate golden files
go test ./internal/session/ -run TestSplit                 # run system tag tests
go test -v ./internal/tui/ -run TestConv                  # verbose conversation UX tests
```

#### Test Patterns

**Pure function tests** — parser, merge, filter, fold logic:
- `internal/session/parser_test.go` — JSONL parsing, content blocks, timestamps
- `internal/session/systemtag_test.go` — XML tag splitting, system tag detection
- `internal/tui/merge_test.go` — conversation merging, context filtering, fold defaults
- `internal/tui/blockfilter_test.go` — block filter parsing and matching

**State machine tests** — TUI interactions via `setupConvApp` + `pressKey`:
- `internal/tui/conversation_ux_test.go` — preview updates, live tail, resize, fold state
- `internal/tui/cmdmode_test.go` — command mode parsing and execution
- `internal/tui/resize_test.go` — resize preservation of fold/scroll/cursor state

**Golden file snapshot tests** — render output captured to `testdata/*.golden`:
- `internal/tui/render_test.go` — message rendering with system tags, tools, block cursor
- Regenerate with `UPDATE_GOLDEN=1`

**Integration tests** — config/plugin discovery with temp directories:
- `internal/session/config_test.go` — config file scanning
- `internal/session/plugin_test.go` — plugin and marketplace discovery
- `internal/tui/config_test.go` — config explorer UI
- `internal/tui/plugins_test.go` — plugin explorer UI

### Benchmarks

```bash
go run ./cmd/bench    # run performance benchmarks
```

### Project Structure

```
cmd/bench/              benchmark tool
internal/
  session/              JSONL parsing, scanning, models, stats, config/plugin discovery
  tui/                  Bubble Tea UI (app, sessions, conversation, messages, stats, config, plugins)
  tmux/                 tmux integration (live detection, pane capture, input)
  extract/              URL and file path extraction from sessions
```

## How It Works

ccx reads Claude Code's session files from `~/.claude/projects/`. Each session is a JSONL file containing the full conversation history — user prompts, assistant responses, tool calls, and results. Subagent sessions live under `{sessionID}/subagents/agent-*.jsonl` with optional `*.meta.json` for type metadata.

Session metadata is cached to `~/.claude/sessions.gob` for instant startup (~1ms). A full async scan runs in the background to pick up new sessions.

The TUI is built with [Bubble Tea](https://github.com/charmbracelet/bubbletea) and [Lip Gloss](https://github.com/charmbracelet/lipgloss).

## Requirements

- Go 1.25+
- Claude Code sessions in `~/.claude/projects/`
- tmux (optional, for live session features)

## License

Apache License 2.0
