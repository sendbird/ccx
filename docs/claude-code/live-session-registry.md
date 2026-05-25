---
last_verified: v2.1.150
---

# Claude Live-Session Registry

`$CLAUDE_CONFIG_DIR/sessions/<pid>.json` (defaults to `~/.claude/sessions/`).

CCX reads this directory in [`internal/clauderegistry`](../../internal/clauderegistry/registry.go)
to detect which Claude Code sessions are alive and whether each one is
actively producing a turn. Only the subset of fields ccx actually needs
is decoded in Go — this doc records the full schema for reference.

## What this is (and isn't)

The directory is a **live-process registry**, not durable session
history. A top-level claude writes its PID file at startup and
`updatePidFile`s in place as state changes. Durable transcripts live in
`~/.claude/projects/<cwd-hash>/<sessionId>.jsonl` — a different
mechanism, different lifecycle.

Subagents and agent-team teammates are launched with `--agent-id` and
**skip** registration (`registerSession` returns early when
`getAgentId()` is non-null). So the registry only ever contains
top-level interactive processes; it is not a per-conversation manifest.

Cleanup: no background timer touches this directory. Stale files from
crashed processes are only unlinked at the next claude startup by
`countConcurrentSessions`. Between crashes and next-startup, dead PID
files sit on disk — readers must liveness-check with `kill(pid, 0)`.
WSL skips even the next-startup unlink, so stale entries accumulate
there indefinitely.

## File schema

### Registration-time fields

Written by `registerSession` when the PID file is first created.

| Field          | Type     | Notes                                                                                              |
| -------------- | -------- | -------------------------------------------------------------------------------------------------- |
| `pid`          | int      | `process.pid`                                                                                      |
| `sessionId`    | string   | Current session UUID. Rewritten in place on session-ID rotation.                                   |
| `cwd`          | string   | Process cwd at registration. Updated on chdir.                                                     |
| `startedAt`    | int      | `Date.now()` in ms.                                                                                |
| `procStart`    | string   | `ps -o lstart= -p <pid>` output (UTC, `LC_ALL=C`). Stored for PID-reuse detection but unused on read. |
| `version`      | string   | CLI version at registration.                                                                       |
| `peerProtocol` | int      | Hard-coded `1` in v2.1.150.                                                                        |
| `kind`         | string   | `"interactive"` \| `"bg"` \| `"daemon"` \| `"daemon-worker"`. Defaults to `"interactive"` when `CLAUDE_CODE_SESSION_KIND` is unset. |
| `entrypoint`   | string   | `"cli"` \| `"vscode"` \| ... — value of `CLAUDE_CODE_ENTRYPOINT`.                                  |

Env-gated optional fields, also written at registration:

| Field     | Condition                                                            |
| --------- | -------------------------------------------------------------------- |
| `name`    | `CLAUDE_CODE_SESSION_NAME` is set                                    |
| `logPath` | `CLAUDE_CODE_SESSION_LOG` is set                                     |
| `agent`   | `CLAUDE_CODE_AGENT` is set                                           |
| `jobId`   | `kind === "bg"` and `CLAUDE_JOB_DIR` is set (stored as its basename) |

### Runtime-mutated fields

Added or rewritten after registration via `updatePidFile`. **Absent on
files captured between registration and the first REPL state update —
that is normal, not a missing-field bug.**

| Field             | Trigger                                                                                            |
| ----------------- | -------------------------------------------------------------------------------------------------- |
| `sessionId`       | Session-ID rotation.                                                                               |
| `cwd`             | Working-directory change.                                                                          |
| `name`            | `updateSessionName`. Also bumps `updatedAt`.                                                       |
| `bridgeSessionId` | IDE / VSCode bridge attachment.                                                                    |
| `status`          | REPL ribbon state. One of `"idle"`, `"busy"`, `"waiting"`, `"shell"`. Written on every transition. |
| `waitingFor`      | Reason string, set only when the raw REPL state is `"waiting"`. Omitted from JSON otherwise.       |
| `updatedAt`       | `Date.now()` in ms, written alongside `status`/`waitingFor`/`name` updates.                        |

#### `status` values

| Value       | Meaning                                                                                          |
| ----------- | ------------------------------------------------------------------------------------------------ |
| `"idle"`    | REPL idle, no background work.                                                                   |
| `"busy"`    | Actively processing a turn.                                                                      |
| `"waiting"` | Blocked on user input. See `waitingFor` for which kind.                                          |
| `"shell"`   | REPL is idle but a local `Bash` tool call is still running in the background.                    |

`"shell"` is reported when the raw REPL state is `"idle"` but a Bash
tool hasn't returned yet — the model isn't generating, but work is
still happening. CCX treats only `"busy"` as "responding" for badge
purposes; `"shell"` would otherwise pin a permanent badge on any
session that left a long-running background task.

#### `waitingFor` values

Only set when `status == "waiting"`. Dropped from the JSON otherwise.

| Value                 | Trigger                                                                                          |
| --------------------- | ------------------------------------------------------------------------------------------------ |
| `"permission prompt"` | A tool-approval modal is mounted (`useIsPermissionPromptOpen()` true).                           |
| `"worker request"`    | A background worker raised a request to the main thread.                                         |
| `"sandbox request"`   | Sandbox (Bash / computer-use) is asking for permission escalation.                               |
| `"dialog open"`       | A local-JSX slash command has mounted a dialog.                                                  |
| `"input needed"`      | Fallback for generic user-input wait when none of the above apply.                               |

### `name` is **not** the `/resume` title

Three independent display strings exist; conflating them produces
confusing UI:

| Field          | Storage                          | Writer                                                                       | Purpose                                                          |
| -------------- | -------------------------------- | ---------------------------------------------------------------------------- | ---------------------------------------------------------------- |
| `name`         | `sessions/<pid>.json`            | `registerSession` (from env) + `updateSessionName`                           | Agent / spawn-seed display label for FleetView and `claude agents --json`. |
| `custom-title` | session JSONL transcript         | `setCustomTitle` — appends `{type:"custom-title",customTitle,sessionId}`     | Human-set title in `/resume` (`/rename`).                        |
| `ai-title`     | session JSONL transcript         | `setAiTitle` — appends `{type:"ai-title",aiTitle,sessionId}`                 | Auto-generated title in `/resume`.                               |

`/rename` does not modify `name`. Conversely, a session launched with
`CLAUDE_CODE_SESSION_NAME` has a `name` that `/resume` does not
display. CCX surfaces `name` (when present) as the agent label and
leaves `/resume` titles alone.

## Reading the registry safely

The writer (`updatePidFile`) does **not** use a temp+rename pattern, so
concurrent readers can land mid-write and see truncated JSON. The write
window is microseconds.

`internal/clauderegistry` handles this with a 3-attempt retry on
`json.Unmarshal` failure (5 ms sleep between attempts). A file that
fails after three retries is skipped — the next refresh tick picks it
up. This also covers the truncate-then-write race where the file size
briefly drops to zero.

## Liveness probing

Use `kill(pid, 0)` (no signal sent — existence + permission check
only). The Node implementation in `countConcurrentSessions` returns
false for `pid <= 1`, so init/PID-0 entries are treated as dead. CCX
matches that behavior.

`procStart` is written but not read by claude itself, so PID reuse
across long-dead processes is theoretically possible. The analysis
notes the surface is "if the OS reuses an orphaned PID for an unrelated
process the registry will silently treat the impostor as the original
session." Acceptable risk in practice; revisit only if CCX gets
reports of phantom-live sessions.

## What `claude agents --json` does differently

The CLI's `agents --json` aggregates these files and:

- normalizes `status` to `idle` / `waiting` / `busy` (collapses `shell` into the same bucket as idle from the CLI's perspective);
- **drops `waitingFor`** from its output.

That's why ccx reads the files directly: we keep the full status set
and have access to `waitingFor` if we ever want to surface it.
