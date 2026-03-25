# Multi-Session Orchestration

## Overview

Run multiple Claude sessions concurrently with coordination. A hub (ccx)
orchestrates spoke sessions, each working on a part of a larger task.

## Architecture

```
                    ┌─────────────────┐
                    │   CCX (hub)     │
                    │   Orchestrator  │
                    └────────┬────────┘
              ┌──────────────┼──────────────┐
              v              v              v
        ┌──────────┐   ┌──────────┐   ┌──────────┐
        │ Session A │   │ Session B │   │ Session C │
        │ (planner) │   │ (backend) │   │ (frontend)│
        └──────────┘   └──────────┘   └──────────┘
              │              │              │
              └──────────────┴──────────────┘
                    Shared task state
```

## Coordination Model

### File-Based IPC

Sessions coordinate via a shared task file:

```
<project>/.claude/orchestration/<run-id>/
├── plan.yaml           # task DAG definition
├── status.yaml         # live status of each task
├── session-a.log       # session output summaries
├── session-b.log
└── session-c.log
```

### Plan Format

```yaml
orchestration:
  id: "run-2026-03-26-001"
  project: /Users/me/src/myapp

tasks:
  - id: plan
    prompt: "Create implementation plan for feature X"
    worktree: null  # uses main repo
    depends_on: []

  - id: backend
    prompt: "Implement the backend API per the plan"
    worktree: backend-impl
    depends_on: [plan]

  - id: frontend
    prompt: "Implement the frontend UI per the plan"
    worktree: frontend-impl
    depends_on: [plan]

  - id: integration
    prompt: "Write integration tests for backend + frontend"
    worktree: integration-tests
    depends_on: [backend, frontend]
```

### Task States

```
pending → running → completed
                  → failed
                  → blocked (dependency failed)
```

## Components

### Plan Parser (`internal/orchestrate/plan.go`)

```go
type Plan struct {
    ID      string
    Project string
    Tasks   []Task
}

type Task struct {
    ID        string
    Prompt    string
    Worktree  string   // empty = main repo
    DependsOn []string
    Status    string   // pending, running, completed, failed
    SessionID string   // assigned Claude session ID
}

func LoadPlan(path string) (*Plan, error)
func ValidatePlan(p *Plan) error  // check DAG is acyclic
```

### DAG Executor (`internal/orchestrate/runner.go`)

```go
type Runner struct {
    Plan       *Plan
    WorkDir    string
    WorktreeDir string
    Sessions   map[string]*RunningSession
}

type RunningSession struct {
    Task    *Task
    Pane    tmux.Pane
    Started time.Time
    Done    bool
}

func (r *Runner) Start(ctx context.Context) error
func (r *Runner) ReadyTasks() []*Task      // tasks with all deps completed
func (r *Runner) SpawnTask(t *Task) error  // create worktree + tmux window
func (r *Runner) MonitorAll() error        // watch for completion
```

Execution loop:
1. Find tasks with all dependencies met
2. For each ready task:
   a. Create git worktree if specified
   b. Open new Claude session in tmux window
   c. Send the task prompt
   d. Mark as running
3. Monitor running sessions (watch JSONL for completion signals)
4. When a session completes, check for newly ready tasks
5. Repeat until all tasks done or failure

### Status Monitor (`internal/orchestrate/status.go`)

```go
func WatchStatus(planDir string) <-chan StatusUpdate
func WriteStatus(planDir string, taskID, status string) error
```

Sessions write their status via Claude's task tool. The orchestrator
watches the status file for changes.

### Session Communication

Sessions don't talk to each other directly. Communication flow:

1. **Orchestrator → Session**: Send prompt via `tmux.SendKeys()`
2. **Session → Orchestrator**: Write to status file, detected by file watcher
3. **Session → Session**: Read shared files (plan output, generated code)

The plan task output is available in the git worktree (committed code).
Later tasks can read earlier tasks' output by examining the repo.

## TUI Integration

### New View: `viewOrchestration`

Shows the task DAG with live status:

```
Orchestration: run-2026-03-26-001
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  [✓] plan          12m  completed
  ├── [●] backend    8m  running...
  ├── [●] frontend   5m  running...
  └── [○] integration     pending (waiting: backend, frontend)
```

Keys:
- `Enter`: open session conversation
- `J`: jump to tmux window
- `K`: kill session
- `R`: retry failed task

### Commands

```
:orch:start <plan.yaml>   Start orchestration
:orch:status              Show orchestration dashboard
:orch:stop                Stop all sessions
:orch:retry <task-id>     Retry a failed task
```

## Future Extensions

- **Smart dependency inference**: Analyze code changes to auto-detect dependencies
- **Resource pooling**: Limit concurrent sessions based on API rate limits
- **Result aggregation**: Auto-merge worktree branches when all tasks complete
- **Remote orchestration**: Spawn sessions on remote K8s pods (combine with remote execution)
- **Plan generation**: Use Claude to generate the orchestration plan from a high-level description
