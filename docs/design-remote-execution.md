# Remote Execution Mode

## Overview

Execute Claude Code on a remote Kubernetes pod with the user's configuration
and auth credentials, streaming the session back to ccx for viewing.

## Architecture

```
[Local ccx TUI] ──kubectl exec──> [K8s Pod: claude-worker]
     │                                    │
     ├── Sends OAuth token (env var)      ├── Runs Claude Code CLI
     ├── Sends config tarball             ├── Writes JSONL to stdout
     └── Reads JSONL stream               └── Git operations on repo
```

## Components

### Config Sync (`internal/remote/sync.go`)

Package local config for remote execution:
```
~/.claude/CLAUDE.md
~/.claude/settings.json
~/.claude/settings.local.json
~/.claude/projects/<encoded>/CLAUDE.md
~/.claude/projects/<encoded>/memory/
```

Tar + base64 encode, inject via `kubectl exec` stdin.

### Auth Forwarding (`internal/remote/auth.go`)

- Extract OAuth token from `~/.claude/.credentials.json`
- Set `CLAUDE_CODE_OAUTH_TOKEN` env var on the pod
- Token is short-lived, transmitted over encrypted kubectl channel
- Never written to disk on the pod

### Pod Management (`internal/remote/pod.go`)

```go
type RemoteConfig struct {
    Namespace    string
    Image        string   // e.g., "node:22-slim" with claude pre-installed
    Context      string   // kubectl context
    Resources    Resources
    WorkDir      string   // remote working directory (git clone)
    GitRepo      string   // repo URL to clone
    GitBranch    string
}

func CreatePod(cfg RemoteConfig) (*Pod, error)
func DeletePod(ctx, namespace, name string) error
```

Pod spec:
- Init container: clone git repo, install Claude Code CLI
- Main container: run Claude with injected config
- Resource limits from config (CPU, memory)
- Node selector for architecture (arm64/amd64)

### Session Streaming (`internal/remote/stream.go`)

```go
func StreamSession(ctx context.Context, pod *Pod) (<-chan []byte, error)
```

Options:
1. **kubectl exec tail**: `kubectl exec <pod> -- tail -f /root/.claude/projects/<id>/session.jsonl`
2. **JSONL to stdout**: Claude writes JSONL, captured by `kubectl logs -f`
3. **Port forward**: Stream over a TCP connection

Recommended: Option 2 — simplest, no file path coordination needed.

### TUI Integration

New command: `:remote:start`

Flow:
1. User runs `:remote:start` with a git repo URL
2. ccx creates a K8s pod with Claude Code
3. Config and auth synced to pod
4. Claude runs, JSONL streamed back
5. ccx displays as a live session (reuse `sessPreviewLive` pattern)
6. Pod cleaned up on session end or `:remote:stop`

New view state: treat remote session like a special live session with
a different data source (kubectl stream instead of tmux capture).

## Security

- OAuth token: only transmitted via encrypted kubectl channel
- Pod RBAC: runs in user's namespace, no cluster-admin
- Network: pod only needs outbound HTTPS to api.anthropic.com
- Cleanup: pod auto-deleted after session ends (TTL or finalizer)
- No persistent credentials stored on the pod

## Prerequisites

- kubectl configured with appropriate context
- Namespace with pod creation permissions
- Container image with Node.js + Claude Code CLI
- Git credentials for repo access (SSH key or token)
