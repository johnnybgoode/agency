# Docker Sandbox Architecture

Agency uses Docker MicroVM sandboxes (`docker sandbox`) to isolate coding agents. This document describes the sandbox model, lifecycle, and implementation details.

## Overview

Each project gets **one shared sandbox** (MicroVM) that all workspaces use. This replaces the previous model of one Docker container per workspace.

```
Project: my-project
  Sandbox: agency-my-project (single MicroVM)
    Workspace A: docker sandbox exec -it -w /path/to/worktree-a agency-my-project claude --session-id <uuid-a>
    Workspace B: docker sandbox exec -it -w /path/to/worktree-b agency-my-project claude --session-id <uuid-b>
    Workspace C: ...
```

Workspaces are isolated by worktree path and Claude session ID, not by VM boundary. The sandbox provides OS-level isolation from the host.

## Sandbox Naming

Sandbox names follow Docker's naming convention: `^[a-zA-Z0-9][a-zA-Z0-9._+-]{0,127}$` (max 128 chars). Agency uses the deterministic name `agency-<projectName>`.

## Lifecycle

### Creation

Sandboxes are created lazily on first workspace creation:

1. `EnsureProjectSandbox` checks if a sandbox already exists via `docker sandbox ls --json`
2. If the image doesn't exist locally, it's built from the embedded Dockerfile
3. `docker sandbox create --name agency-<project> -t <image> claude <projectDir>` creates the MicroVM
4. The sandbox name is stored in `state.json` as `sandbox_id`

### Three-State Ensure

On each workspace creation or resume, `Ensure` handles three states:

| State | Detection | Action |
|-------|-----------|--------|
| Running | `socket_path` present in `ls --json` output | Return immediately |
| Stopped | Listed in `ls --json` but no `socket_path` | `docker sandbox run -d <name>` |
| Absent | Not in `ls --json` output | `docker sandbox create ...` |

The `status` field in the `ls --json` output is unreliable (can report "running" for stopped VMs). The presence of `socket_path` is the reliable indicator.

### Session Resumption

Each workspace gets a UUID v4 session ID (stored in `Workspace.SessionID`):

- First launch: `claude --session-id <uuid>` (creates a session with a known ID)
- Subsequent launches: `claude --resume <uuid>` (resumes the existing session)

### Quit / Shutdown

On quit, cleanup follows a strict ordering to prevent racing against the sandbox daemon:

1. **Kill workspace tmux windows** — stops trap loops immediately so they can't call `docker sandbox ls/exec` during teardown
2. **Clean up worktrees** — remove git worktrees for non-dirty workspaces, update state
3. **Save state**
4. **Stop sandbox in background** — `docker sandbox stop` via a detached process (`Setpgid`) so it survives the parent exit
5. **Kill tmux session** — terminates the sidebar process (runs inside tmux)

The background stop uses `Setpgid: true` and `exec.Command` (not `CommandContext`) so the child process is in its own process group and isn't killed when the sidebar exits.

### Workspace Deletion

Deleting a workspace does **not** stop or remove the sandbox — it's a shared resource. Only the worktree, tmux window, and state entry are removed.

## Trap Loop

Each workspace runs in a tmux pane with this shell command:

```bash
bash -c 'clear; \
  trap "cd \"$PROJECT_DIR\" && agency gc --workspace-id $WS_ID >/dev/null 2>&1" EXIT; \
  CMD="--session-id $SESSION_UUID"; \
  while docker sandbox ls -q | grep -qx $SANDBOX_NAME; do \
    docker sandbox exec -it -w "$WORKTREE_PATH" $SANDBOX_NAME claude $CMD || true; \
    CMD="--resume $SESSION_UUID"; \
    sleep 1; \
  done'
```

Key design points:
- `clear` hides the echoed command from the user
- `EXIT` trap runs `agency gc` for workspace cleanup (output suppressed)
- `while` loop checks sandbox existence before each iteration via `docker sandbox ls -q | grep -qx`
- First iteration uses `--session-id`, subsequent use `--resume`
- `|| true` prevents exit on Claude non-zero exit codes
- `sleep 1` prevents tight loops if Claude exits immediately

## Reconciliation

Reconciliation queries sandbox status via `docker sandbox ls --json` (one call, not per-workspace). If the sandbox is down, all workspaces are marked as failed simultaneously.

## Retry Logic

`FindByName` retries once (after `ListRetryDelay`, default 2s) if `docker sandbox ls --json` fails. This handles transient daemon errors during state transitions.

## Docker Sandbox CLI Reference

Agency uses these `docker sandbox` subcommands (v0.12.0):

| Command | Purpose |
|---------|---------|
| `docker sandbox version` | Verify sandbox support on startup |
| `docker sandbox ls --json` | List VMs with status and socket_path |
| `docker sandbox ls -q` | List VM names only (used in trap loop) |
| `docker sandbox create --name <n> -t <img> claude <dir>` | Create a new sandbox |
| `docker sandbox run -d <name>` | Start a stopped sandbox (detached) |
| `docker sandbox exec -it -w <path> <name> <cmd>` | Execute command in sandbox |
| `docker sandbox stop <name>` | Stop a running sandbox |
| `docker sandbox rm <name>` | Remove a sandbox |

### JSON Output Format

```json
{
  "vms": [
    {
      "name": "agency-my-project",
      "agent": "claude",
      "status": "running",
      "socket_path": "/path/to/.docker/sandboxes/vm/agency-my-project/docker.sock",
      "workspaces": ["/path/to/my-project"]
    }
  ]
}
```

## Configuration

```toml
[sandbox]
image = "agency:latest"        # sandbox template image
dockerfile_dir = ""            # optional custom Dockerfile location
```

Credentials are handled by Docker Desktop's credential proxy — no credential management in Agency.

## State Schema

Project-level (in `state.json`):
```json
{
  "version": 2,
  "sandbox_id": "agency-my-project"
}
```

Per-workspace:
```json
{
  "sandbox_id": "agency-my-project",
  "session_id": "550e8400-e29b-41d4-a716-446655440000"
}
```

## Known Limitations

- **Docker sandbox CLI is experimental** — JSON output format and subcommands may change between versions.
- **Shared sandbox failure** — if the sandbox crashes, all workspaces fail simultaneously.
- **Mount immutability** — mounts are set at creation time. If the project directory moves, the sandbox must be recreated manually (`docker sandbox rm` + re-launch).
- **No `docker sandbox inspect`** — this subcommand does not exist in v0.12.0 despite appearing in some Docker documentation.
