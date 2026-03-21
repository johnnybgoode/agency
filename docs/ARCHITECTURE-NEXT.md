# Coding Agent Manager

This doc reflects the evolution of the project from a simple sandbox environment manager to a full-fledged tool for managing multiple, parallel agentic coding workspaces for a given project. The goal is a single tool for creating and managing N+1 agentic coding workspaces for any project, rather than a suite of scripts for simply automating the setup of an agentic coding environment per-project/worktree.


## Core Principles

 - **Security:** coding agents must be able to run autonomously without the possibility of compromising the host system, other agents working in parallel, or processes outside of the agent's environment.
 - **Performance:** The tool must support multiple concurrent workspaces running in parallel. To that end the tool must have absolute minimal runtime overhead, and the number of concurrent workspaces should only be limited by the size and complexity of the codebase, coding tasks, and system itself.

## Core Requirements

This section outlines fundamental requirements of the system that are essential to the user and coding agents working seamlessly across multiple concurrent workspaces.

 - TUI: a Go TUI (bubbletea) that wraps tmux. The TUI runs as a persistent sidebar in the left pane of the main tmux window. The right pane shows the active workspace's Claude Code session. Workspace panes are swapped in/out of the right slot via `swap-pane`. Detach/reattach is handled natively by tmux.
 - CLI: cli to invoke the TUI in a given project. Provides flags/options for project-level configuration
 - Sandboxes: all workspaces in a project share a single Docker MicroVM sandbox (`docker sandbox`). The sandbox provides OS-level isolation from the host via a lightweight VM. Workspaces are isolated within the sandbox by worktree path and Claude session ID. The sandbox is created lazily on first workspace creation and stopped on quit. Credentials are handled by Docker Desktop's credential proxy.
 - Worktrees: each workspace operates on an independent branch and copy of the codebase. 1:1 relationship between workspaces and worktrees
 - Agent communication: (future) agents will be able to create epics, issues, plans, and tasks to track their work via beads. Beads would live in the project repo and be available per-worktree via git. Task updates would propagate at commit-level latency (not real-time). Agent-to-agent communication is deferred to a future version.
 - Agent and project settings: Three-tier settings cascade (global → project → workspace-local) using TOML config files. Users can manage settings (environment vars, credentials, sandbox settings, ...) and agent defaults (permissions, mcp, subagents, ...) per-project. Agent settings may also be managed locally per-workspace (e.g. adding an MCP, enabling a subagent) without affecting other workspaces.

## Architecture



                            ┌─────────────────────────────────────────────────────┐
                            │                                                     │
                            │               User Interface Layer                  │
                            │                                                     │
                            │  ┌────────────────────┐     ┌────────────────────┐  │
                            │  │                    │     │                    │  │
                            │  │        CLI         │────►│    TUI (Go/tmux)   │  │
                            │  │                    │     │                    │  │
                            │  └────────────────────┘     └────────────────────┘  │
                            └───────────────────────────┬─────────────────────────┘
                                                        │
                            ┌───────────────────────────▼──────────────────────────┐
                            │                                                      │
                            │               Agent Orchestration Layer              │
                            │                                                      │
                            │  ┌─────────────┐   ┌─────────────┐   ┌────────────┐  │
                            │  │             │   │             │   │            │  │
                            │  │  Worktrees  │   │  Sandboxes  │   │ Workspaces │  │
                            │  │             │   │             │   │            │  │
                            │  └─────────────┘   └─────────────┘   └────────────┘  │
                            │                                                      │
                            └───────────────────────────┬──────────────────────────┘
                                                        │
                                                        │
                    ┌───────────────────────────────────▼────────────────────────────────────┐
                    │                                                                        │
                    │                  Sandbox Layer (Docker MicroVM Sandbox)│
                    │                                                                        │
                    └────────────────┬───────────────────┬───────────────────┬───────────────┘
                                     │                   │                   │
                      ┌──────────────┘                   │                   └──────────────┐
                      │                                  │                                  │
       ┌──────────────▼──────────────┐    ┌──────────────▼──────────────┐    ┌──────────────▼──────────────┐
       │                             │    │                             │    │                             │
       │          Workspace          │    │          Workspace          │    │          Workspace          │
       │                             │    │                             │    │                             │
       │ ┌─────────────────────────┐ │    │ ┌─────────────────────────┐ │    │ ┌─────────────────────────┐ │
       │ │Agent & subagents        │ │    │ │Agent & subagents        │ │    │ │Agent & subagents        │ │
       │ │                         │ │    │ │Epics/Issues (beads)     │ │    │ │Epics/Issues (beads)     │ │
       │ │Worktree                 │ │    │ │Worktree                 │ │    │ │Worktree                 │ │
       │ │Workspace-local settings │ │    │ │Workspace-local settings │ │    │ │Workspace-local settings │ │
       │ │                         │ │    │ │                         │ │    │ │                         │ │
       │ └─────────────────────────┘ │    │ └─────────────────────────┘ │    │ └─────────────────────────┘ │
       └─────────────────────────────┘    └─────────────────────────────┘    └─────────────────────────────┘


### User Interface

The tool is a Go binary that owns a tmux session for the project. On launch, it renders the TUI as a full-screen modal view in the active tmux window. Workspaces run in background tmux windows. Creating or selecting a workspace switches to that workspace's tmux window. A keybinding (e.g. `prefix + m`) returns to the TUI management view. This is simpler than sidebar/popup approaches and works with any tmux version.

**Orchestration model:** The Go binary creates and attaches to a tmux session for the project. It interacts with tmux via direct `tmux` CLI calls (exec, not a Go library). Each new workspace triggers the sequence: create worktree → start sandbox → create tmux window → launch agent → swap pane into main window. Workspaces are interactive: the user types instructions directly into the agent's Claude Code session. Non-interactive workspace creation is supported via `agency new <name> <branch>`.

**Layout:** The main tmux window is split into two panes. The left pane runs the bubbletea sidebar TUI. The right pane shows the active workspace's Claude Code session, swapped in via `swap-pane`. Layout state is persisted to tmux session environment variables for crash recovery.

**Detach/reattach:** tmux handles this natively. The user detaches (`<prefix> d`), all agents continue running in their windows, and the user reattaches later. On reattach (or after a TUI crash), the Go binary reconnects to the existing tmux session and re-discovers running workspaces via state reconciliation (see State Recovery).

A CLI provides direct access to operations: `agency init`, `agency new`, `agency workspace list`, `agency workspace rm`, `agency gc`, `agency version`.


### Sandboxing

Security is a first principle; sandboxed workspace runtimes are essential. Sandboxing protects the host system — if the host is compromised no work can be done. Sandboxing protects agents — if one agent is compromised all agents are compromised.

**Sandbox Requirements**

 - Uses Docker MicroVM sandboxes (`docker sandbox`) via Docker Desktop. The sandbox provides hypervisor-level isolation via a lightweight VM.
 - One sandbox per project — all workspaces share the same MicroVM. Workspaces are isolated by worktree path and Claude session ID.
 - Sandbox network access is governed by Docker sandbox network policies. No custom network filtering layer is needed.
 - The sandbox template image is configurable via `sandbox.image` (default `agency:latest`, overridable per-project).
 - Credentials are handled by Docker Desktop's credential proxy — no credential management in Agency.

**Mounts**

The project directory is mounted into the sandbox at creation time. Each workspace's worktree is a subdirectory of the project, accessed via `docker sandbox exec -w <worktreePath>`. Mounts are immutable after sandbox creation — if the project directory moves, the sandbox must be recreated.

See [docs/sandbox.md](sandbox.md) for full implementation details.


### Worktrees

Git worktrees enable fast ad-hoc copies of the codebase for each workspace.

The tool is opinionated about the project filesystem and worktree layout. The project root is **not** a git repo — it is a plain directory that contains the bare clone and all worktrees:

```
my-project/                          # Project root (NOT a git repo)
├── .bare/                           # Bare git clone
├── .agency/
│   ├── config.toml                  # Project settings
│   ├── state.json                   # Runtime state (machine-managed)
│   └── lock                         # flock lockfile
├── my-project-main/                 # Worktree: main branch
│   ├── .agency/
│   │   └── config.toml              # Workspace-local settings (optional)
│   ├── .beads/
│   └── (project files)
├── my-project-feature-add-auth/     # Worktree: agent/feature-add-auth
│   └── (project files)
└── ...
```

Note: `.agency/` at the project root is outside all worktrees. Each worktree may optionally contain its own `.agency/config.toml` for workspace-local overrides.

#### Worktree Lifecycle

**Initialization (`agency init`):**

Three starting conditions:

 1. **Normal clone exists** (`.git/` is a directory): refuse if the working tree is dirty (user must commit or stash first). Record remote URL, `git clone --bare <remote> .bare`, create initial worktree from bare clone.
 2. **Fresh setup** (no local repo): `git clone --bare <remote> <project>/.bare`, then `git worktree add ../<project>-main main`.
 3. **Already using bare + worktrees**: detect `.bare` and existing worktrees via `git worktree list`, validate layout, write project config, register existing worktrees in state file.

**Naming strategy:**

Worktree directories: `<project>-<slug>[-<short>]`
 - `<slug>`: branch name slugified (lowercase, `/` → `-`, truncated to 40 chars)
 - `<short>`: 4-char hex suffix, appended only on collision

Workspace IDs are separate: 8-char hex (e.g., `ws-a1b2c3d4`). The mapping from workspace ID to worktree path lives in the state file.

Branch names for agent-created worktrees use a configurable prefix (default `agent/`).

**Cleanup:**

 1. **Explicit command** (`agency workspace rm <id>`): stop agent → destroy sandbox → verify branch pushed (warn if not) → `git worktree remove` → optionally delete merged branch.
 2. **Workspace completion**: worktree is NOT auto-removed. User reviews, merges, then explicitly removes. This prevents accidental work loss.
 3. **Orphan GC** (`agency gc`): enumerate worktrees via `git -C .bare worktree list`, cross-reference with state file. Orphans flagged for user to adopt or remove. Also runs `git worktree prune` for stale lock files.

**Git lock contention:**

Most lock surfaces are per-worktree (each has its own index). The primary contention point is `git fetch`. Mitigations:
 - Orchestrator owns fetch scheduling — one centralized `git -C .bare fetch --all` on a configurable interval (default 60s) rather than per-agent fetches.
 - Auto-gc disabled in `.bare` (`gc.auto 0`). GC runs explicitly during idle periods via `agency gc`.
 - Pushes to distinct branches don't contend. Git ref-update atomicity handles the rare same-branch case.

At 10+ concurrent agents, worst case is occasional 100-200ms delays during the global fetch. Acceptable.


### Workspace State Machine

A workspace = worktree + sandbox + tmux window + coding agent.

#### States

```
CREATING → PROVISIONING → RUNNING → COMPLETING → DONE
    |            |            |           |
    v            v            v           v
  FAILED      FAILED       FAILED     FAILED
                              |
                              v
                           PAUSED → RUNNING (resume)
```

| State | Description |
|-------|-------------|
| `creating` | Worktree being created, branch checked out |
| `provisioning` | Sandbox starting, volumes mounted, agent launching |
| `running` | Agent executing inside sandbox, user can interact via tmux window |
| `paused` | Agent stopped; sandbox may be alive (soft pause) or torn down (hard pause) |
| `completing` | Agent finished or user stopped; sandbox tearing down, final git ops |
| `done` | Sandbox destroyed, worktree intact for review |
| `failed` | Error occurred; partial state may exist, needs cleanup or retry |

#### Key Transitions

 - **creating → provisioning**: Worktree creation succeeds. State file updated.
 - **provisioning → running**: Sandbox healthy, agent process started, tmux window created.
 - **running → completing**: Agent exits with code 0, or user sends stop command. Graceful shutdown (5s timeout → SIGKILL), sandbox teardown begins.
 - **completing → done**: Sandbox destroyed, state file updated.
 - **running → paused**: User command. Two modes:
   - *Soft pause* (default): SIGSTOP to agent. Sandbox stays alive. Fast resume via SIGCONT.
   - *Hard pause*: Stop sandbox entirely. Worktree preserved. Resume requires re-provisioning.
 - **paused → running**: SIGCONT (soft) or re-provision (hard).
 - **Any → failed**: Records `failed_from`, `error`, and `cleanup_needed`.

#### Failure Handling

 - **Sandbox crashes**: detected via Docker event stream or periodic health check (5s poll). Transition to `failed`. Worktree intact on host. User can retry (`failed` → `provisioning`, same worktree).
 - **Agent exits unexpectedly**: non-zero exit → `failed`. Zero exit → `completing`. Sandbox stays running for user inspection.
 - **Git failures**: worktree creation failure → retry with different name suffix. Push failure → report to user, stay in `completing`.


### State Recovery

The orchestrator persists workspace state to `<project-root>/.agency/state.json` and reconciles against runtime state on every startup.

#### State File

```json
{
  "version": 2,
  "project": "my-project",
  "bare_path": "/home/user/my-project/.bare",
  "tmux_session": "agency-my-project",
  "sandbox_id": "agency-my-project",
  "pid": 48231,
  "updated_at": "2026-03-11T19:45:00Z",
  "workspaces": {
    "ws-a1b2c3d4": {
      "id": "ws-a1b2c3d4",
      "state": "running",
      "branch": "agent/fix-login-bug",
      "worktree_path": "/home/user/my-project/my-project-fix-login-bug",
      "sandbox_id": "agency-my-project",
      "session_id": "550e8400-e29b-41d4-a716-446655440000",
      "tmux_window": "@5",
      "created_at": "2026-03-11T19:00:00Z",
      "updated_at": "2026-03-11T19:45:00Z",
      "pause_mode": null,
      "failed_from": null,
      "error": null
    }
  }
}
```

#### Reconciliation

On TUI startup (fresh or reattach), three systems are queried in parallel:
 1. `tmux list-windows -t <tmux-session>` — which windows exist
 2. `docker sandbox ls --json` — whether the project sandbox is running
 3. `git -C .bare worktree list` — which worktrees exist

Each workspace is reconciled:

| State file says | Reality | Action |
|---|---|---|
| `running` | sandbox running + window exist | Reattach, no action |
| `running` | sandbox running, window gone | Recreate tmux window, resume session |
| `running` | sandbox down | → `failed` (all workspaces fail) |
| `provisioning` | sandbox running | Advance to `running` |
| `provisioning` | sandbox down | → `failed` |
| `paused` | sandbox running | Ensure sandbox, resume via tmux |
| `paused` | sandbox down | Ensure sandbox (start/create), resume |
| `done` | worktree gone | Remove workspace entry |
| No entry | worktree matching pattern | Orphan — offer to adopt or remove |

#### Persistence

 - State written synchronously on every state transition (atomic write via temp file + rename).
 - Periodic flush every 30s for timestamp updates.
 - File locking via `flock(2)`.

#### Concurrent Access Prevention

 - State file records `pid` of owning TUI process.
 - On startup: if recorded PID is alive → refuse, suggest `tmux attach`. If dead → claim ownership, run reconciliation.
 - `flock` on `<project>/.agency/lock` with `LOCK_EX | LOCK_NB` for race-free exclusion.


### Communication (Future)

Agent-to-agent communication and task tracking (beads) are not yet implemented. The planned model: agents create and complete issues tracked within the project repo, with updates propagating at commit-level latency via git. Real-time communication is deferred to a future version.


### Settings

Three-tier cascade: Global → Project → Workspace-local. All config files use TOML. The state file uses JSON (machine-managed).

#### File Locations

| Tier | Path | Scope |
|------|------|-------|
| Global | `~/.config/agency/config.toml` | All projects |
| Project | `<project-root>/.agency/config.toml` | All workspaces in project |
| Workspace-local | `<worktree>/.agency/config.toml` | Single workspace |

#### Configuration Schema

```toml
# Global (~/.config/agency/config.toml)
[agent]
default = "claude"
permissions = "auto-accept"

[sandbox]
image = "agency:latest"
```

```toml
# Project (<project>/.agency/config.toml)
[sandbox]
memory = "8g"
cpus = 4

[agent]
model = "opus"
mcp_servers = ["filesystem", "github"]

[worktree]
branch_prefix = "agent/"
auto_push = true
```

```toml
# Workspace-local (<worktree>/.agency/config.toml) — rare
[agent]
mcp_servers = ["+notion"]  # "+" prefix = append to project list
```

#### Credentials

Credentials are handled by Docker Desktop's credential proxy — Agency does not manage credentials directly. Docker Desktop automatically proxies authentication to the host.

#### Scope Rules

| Setting | Scope | Notes |
|---------|-------|-------|
| `sandbox.image` | Project | Same template image across all workspaces |
| `agent.model` | Project, overridable per-workspace | |
| `agent.mcp_servers` | Project, appendable per-workspace | Use `+` prefix to append |
| `worktree.branch_prefix` | Project | Naming is project-wide |

#### Merge Semantics

 1. **Scalars**: lower tier overrides higher. Workspace > Project > Global.
 2. **Lists**: replace by default. `+` prefix on elements = append to parent list.
 3. **Tables**: shallow merge — lower tier keys override; higher-tier-only keys preserved.
 4. **Deletion**: `"__unset__"` explicitly removes an inherited value.

Merge computed once at workspace creation. Config changes require workspace restart.


### Implementation

The tool is implemented in Go. Key dependencies:
 - CLI: spf13/cobra
 - TUI: charmbracelet/bubbletea + lipgloss
 - Config: pelletier/go-toml
 - tmux interaction: exec calls to `tmux` CLI
 - Docker interaction: exec calls to `docker` CLI
 - Logging: stdlib log/slog with per-session log files

### Appendix

 1. Nice to have
   - Web UI
   - Complete subcommand interface for CLI (mirrors tmux menu/keyboard shortcut functionality)
   - Headless/scripted workspace creation (non-interactive task assignment)
   - Pluggable sandbox provider interface; additional providers: gVisor (Linux), Apple Containers (macOS 26+)
   - Real-time agent-to-agent communication
   - Cost tracking / token usage monitoring
   - Log persistence via `tmux pipe-pane`
 2. Codenames (internal)
   - Agency (enables fully autonomous agents through sandboxes and processes, guardrails)
   - Agility (enables rapid coding via concurrent agentic coding)
 3. Known limitations (v1)
   - Git submodules: known issues with worktrees (shared `.git/modules`). Unsupported/best-effort in v1.
   - Network access: sandbox network access is controlled by Docker sandbox network policies.
   - Docker sandbox CLI is experimental (v0.12.0) — subcommands and JSON output may change.
   - Beads (agent task tracking) not yet implemented.
 4. Complete architecture diagram

                              ┌─────────────────────────────────────────────────────┐
  ┌─────────────────┐         │                                                     │
  │                 │         │               User Interface Layer                  │
  │                 │         │                                                     │
  │    Global /     │         │                                                     │
  │      User       │         │  ┌────────────────────┐     ┌────────────────────┐  │
  │    Settings     │         │  │                    │     │                    │  │
  │                 │         │  │        CLI         │ ──► │   TUI (Go/tmux)    │  │
  │  (config file)  │         │  │                    │     │                    │  │
  │                 │         │  └────────────────────┘     └────────────────────┘  │
  └─────────┬───────┘         │                                                     │
            │                 └──┬──────────────────────────────────────┬───────────┘
            │                    │                                      │
            │                    │                                      │
            │       ┌────────────▼──────┐      ┌────────────────────────▼─────────────────────────────┐
            │       │                   │      │                                                      │
            │       │     Project       │      │           Workspace Orchestration Layer              │
            └─────► │     Settings      │      │                                                      │
                    │                   │      │  ┌─────────────┐   ┌─────────────┐   ┌────────────┐  │
                    │ Sandbox settings  │      │  │             │   │             │   │            │  │
                    │ (subagents,       ├─────►│  │  Worktrees  │   │  Sandboxes  │   │  Coding    │  │
                    │  mcp, plugins)    │      │  │             │   │             │   │    Agent   │  │
                    │                   │      │  └─────────────┘   └─────────────┘   └────────────┘  │
                    │                   │      │                                                      │
                    └───────────────────┘      └─────────────────────────┬────────────────────────────┘
                                                                         │
                                                                         │
                                       ┌─────────────────────────────────▼──────────────────────────────────────┐
                                       │                                                                        │
                                       │                  Sandbox Layer (Docker MicroVM Sandbox)                      │
                                       │                                                                        │
                                       └────────────────┬───────────────────┬───────────────────┬───────────────┘
                                                        │                   │                   │
                                         ┌──────────────┘                   │                   └──────────────┐
                                         │                                  │                                  │
                          ┌──────────────▼──────────────┐    ┌──────────────▼──────────────┐    ┌──────────────▼──────────────┐
                          │                             │    │                             │    │                             │
                          │          Workspace          │    │          Workspace          │    │          Workspace          │
                          │                             │    │                             │    │                             │
                          │  Agent Instance             │    │  Agent Instance             │    │  Agent Instance             │
                          │                             │    │                             │    │                             │
                          ││    ││    ││
                          │                             │    │                             │    │                             │
                          │  Worktree                   │    │  Worktree                   │    │  Worktree                   │
                          │                             │    │                             │    │                             │
                          │  Workspace-local settings   │    │  Workspace-local settings   │    │  Workspace-local settings   │
                          │                             │    │                             │    │                             │
                          └─────────────────────────────┘    └─────────────────────────────┘    └─────────────────────────────┘


### References

 [1]: https://docs.docker.com/engine/network/ "Docker network documentation"
