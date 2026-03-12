# Coding Agent Manager

This doc reflects the evolution of the project from a simple sandbox environment manager to a full-fledged tool for managing multiple, parallel agentic coding sessions for a given project. The goal is a single tool for creating and managing N+1 agentic coding sessions for any project, rather than a suite of scripts for simply automating the setup of an agentic coding environment per-project/worktree.


## Core Principles

 - **Security:** coding agents must be able to run autonomously without the possibility of compromising the host system, other agents working in parallel, or processes outside of the agent's environment.
 - **Performance:** The tool must support multiple concurrent agentic coding sessions running in parallel. To that end the tool must have absolute minimal runtime overhead, and the number of concurrent sessions should only be limited by the size and complexity of the codebase, coding tasks, and system itself.

## Core Requirements

This section outlines fundamental requirements of the system that are essential to the user and coding agents working seamlessly across multiple concurrent sessions.

 - TUI: a Go TUI (bubbletea/tview) that wraps tmux. The TUI runs as a full-screen modal view in the primary tmux window. Agent sessions run in background tmux windows. The user switches between the TUI management view and individual agent windows. A keybinding (e.g. `prefix + m`) returns to the TUI management view. Detach/reattach is handled natively by tmux.
 - CLI: cli to invoke the TUI in a given project. Provides flags/options for project-level configuration
 - Sandboxes: each coding agent session takes place in a Docker container managed via Docker Desktop. On macOS, Docker Desktop's Linux VM provides sufficient host OS isolation — container code cannot reach the macOS host without a hypervisor exploit. Sessions have r/w access to their worktree. Project and global config files are mounted read-only into the sandbox (e.g. at `/etc/tool/config.toml`). Credentials are injected via environment variables (never mounted as files). 1:1 relationship between sessions and sandboxes.
 - Worktrees: each coding session operates on an independent branch and copy of the codebase. 1:1 relationship between sessions and worktrees
 - Agent communication: agents can create epics, issues, plans, and tasks to track their work via beads. Beads live in the project repo and are available per-worktree via git. Task updates propagate at commit-level latency (not real-time). Agent-to-agent communication is deferred to a future version.
 - Agent and project settings: Three-tier settings cascade (global → project → session-local) using TOML config files. Users can manage settings (environment vars, credentials, sandbox settings, ...) and agent defaults (permissions, mcp, subagents, ...) per-project. Agent settings may also be managed locally per-session (e.g. adding an MCP, enabling a subagent) without affecting other sessions.

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
                            │  │  Worktrees  │   │  Sandboxes  │   │  Sessions  │  │
                            │  │             │   │             │   │            │  │
                            │  └─────────────┘   └─────────────┘   └────────────┘  │
                            │                                                      │
                            └───────────────────────────┬──────────────────────────┘
                                                        │
                                                        │
                    ┌───────────────────────────────────▼────────────────────────────────────┐
                    │                                                                        │
                    │                  Sandbox Layer (Docker Container)│
                    │                                                                        │
                    └────────────────┬───────────────────┬───────────────────┬───────────────┘
                                     │                   │                   │
                      ┌──────────────┘                   │                   └──────────────┐
                      │                                  │                                  │
       ┌──────────────▼──────────────┐    ┌──────────────▼──────────────┐    ┌──────────────▼──────────────┐
       │                             │    │                             │    │                             │
       │        Coding Session       │    │        Coding Session       │    │        Coding Session       │
       │                             │    │                             │    │                             │
       │ ┌─────────────────────────┐ │    │ ┌─────────────────────────┐ │    │ ┌─────────────────────────┐ │
       │ │Agent & subagents        │ │    │ │Agent & subagents        │ │    │ │Agent & subagents        │ │
       │ │Epics/Issues (beads)     │ │    │ │Epics/Issues (beads)     │ │    │ │Epics/Issues (beads)     │ │
       │ │Worktree                 │ │    │ │Worktree                 │ │    │ │Worktree                 │ │
       │ │Session local settings   │ │    │ │Session local settings   │ │    │ │Session local settings   │ │
       │ │                         │ │    │ │                         │ │    │ │                         │ │
       │ └─────────────────────────┘ │    │ └─────────────────────────┘ │    │ └─────────────────────────┘ │
       └─────────────────────────────┘    └─────────────────────────────┘    └─────────────────────────────┘


### User Interface

The tool is a Go binary that owns a tmux session for the project. On launch, it renders the TUI as a full-screen modal view in the active tmux window. Agent sessions run in background tmux windows. Creating or selecting a session switches to that session's tmux window. A keybinding (e.g. `prefix + m`) returns to the TUI management view. This is simpler than sidebar/popup approaches and works with any tmux version.

**Orchestration model:** The Go TUI creates and attaches to a tmux session for the project. It interacts with tmux programatically ([japiotr123/go-tmux][2], [owenthereal/tmux][3]) to manage coding session windows. Each new session triggers the sequence: create worktree → start sandbox → create tmux window → launch agent. Sessions are interactive: the tool creates the worktree, sandbox, and tmux window, launches the agent CLI, and switches to that window. The user types instructions directly into the agent. Headless/scripted session creation is deferred to a future version.

**Detach/reattach:** tmux handles this natively. The user detaches (`<prefix> d`), all agents continue running in their windows, and the user reattaches later. On reattach (or after a TUI crash), the Go binary reconnects to the existing tmux session and re-discovers running sessions via state reconciliation (see State Recovery).

A lightweight CLI exists to invoke the tool within a project (e.g. `tool`, `tool gc [project]`) and set project-level settings.


### Sandboxing

Security is a first principle; sandboxed session runtimes are essential. Sandboxing protects the host system — if the host is compromised no work can be done. Sandboxing protects agents — if one agent is compromised all agents are compromised.

**Sandbox Requirements**

 - v1 uses standard Docker containers via Docker Desktop. On macOS, Docker Desktop's Linux VM provides sufficient host OS isolation. Per-session microVM isolation (gVisor on Linux, Apple Containers on macOS 26+) is deferred to a future version via a pluggable provider interface.
 - Each session gets its own container by default, i.e. only the session worktree is mounted.
 - Sandbox network access is governed by Docker network policies. No custom network filtering layer is needed. The network policy can be configured per project.
 - The sandbox image is configurable via `sandbox.image` (default provided by the tool, overridable per-project). Example: `sandbox.image = "claude-sandbox:latest"`.

**Volume Mounts**

Each sandbox has the following mounts:

 - **Worktree** (r/w): the session's worktree directory, mounted at the sandbox working directory.
 - **Config files** (r/o): project and global config TOML files are mounted read-only into the sandbox (e.g. at `/etc/tool/config.toml`) so the agent can read settings.
 - **Agent home** (r/o + writable overlay): a shared named volume created and populated on first `tool init` (installs agent tools, configures claude, etc.). Mounted read-only at `/home/agent`. Each sandbox gets a writable overlay (via Docker tmpfs or similar) for session-specific state.
 - **Credentials**: injected via environment variables passed to `docker run`. Never mounted as files.


### Worktrees

Git worktrees enable fast ad-hoc copies of the codebase for each agentic coding session.

The tool is opinionated about the project filesystem and worktree layout. The project root is **not** a git repo — it is a plain directory that contains the bare clone and all worktrees:

```
my-project/                          # Project root (NOT a git repo)
├── .bare/                           # Bare git clone
├── .tool/
│   ├── config.toml                  # Project settings
│   ├── state.json                   # Runtime state (machine-managed)
│   └── lock                         # flock lockfile
├── my-project-main/                 # Worktree: main branch
│   ├── .tool/
│   │   └── config.toml              # Session-local settings (optional)
│   ├── .beads/
│   └── (project files)
├── my-project-feature-add-auth/     # Worktree: agent/feature-add-auth
│   └── (project files)
└── ...
```

Note: `.tool/` at the project root is outside all worktrees. Each worktree may optionally contain its own `.tool/config.toml` for session-local overrides.

#### Worktree Lifecycle

**Initialization (`tool init`):**

Three starting conditions:

 1. **Normal clone exists** (`.git/` is a directory): refuse if the working tree is dirty (user must commit or stash first). Record remote URL, `git clone --bare <remote> .bare`, create initial worktree from bare clone.
 2. **Fresh setup** (no local repo): `git clone --bare <remote> <project>/.bare`, then `git worktree add ../<project>-main main`.
 3. **Already using bare + worktrees**: detect `.bare` and existing worktrees via `git worktree list`, validate layout, write project config, register existing worktrees in state file.

**Naming strategy:**

Worktree directories: `<project>-<slug>[-<short>]`
 - `<slug>`: branch name slugified (lowercase, `/` → `-`, truncated to 40 chars)
 - `<short>`: 4-char hex suffix, appended only on collision

Session IDs are separate: 8-char hex (e.g., `sess-a1b2c3d4`). The mapping from session ID to worktree path lives in the state file.

Branch names for agent-created worktrees use a configurable prefix (default `agent/`).

**Cleanup:**

 1. **Explicit command** (`tool session rm <id>`): stop agent → destroy sandbox → verify branch pushed (warn if not) → `git worktree remove` → optionally delete merged branch.
 2. **Session completion**: worktree is NOT auto-removed. User reviews, merges, then explicitly removes. This prevents accidental work loss.
 3. **Orphan GC** (`tool gc`): enumerate worktrees via `git -C .bare worktree list`, cross-reference with state file. Orphans flagged for user to adopt or remove. Also runs `git worktree prune` for stale lock files.

**Git lock contention:**

Most lock surfaces are per-worktree (each has its own index). The primary contention point is `git fetch`. Mitigations:
 - Orchestrator owns fetch scheduling — one centralized `git -C .bare fetch --all` on a configurable interval (default 60s) rather than per-agent fetches.
 - Auto-gc disabled in `.bare` (`gc.auto 0`). GC runs explicitly during idle periods via `tool gc`.
 - Pushes to distinct branches don't contend. Git ref-update atomicity handles the rare same-branch case.

At 10+ concurrent agents, worst case is occasional 100-200ms delays during the global fetch. Acceptable.


### Session State Machine

A session = worktree + sandbox + tmux window + coding agent.

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

The orchestrator persists session state to `<project-root>/.tool/state.json` and reconciles against runtime state on every startup.

#### State File

```json
{
  "version": 1,
  "project": "my-project",
  "bare_path": "/home/user/my-project/.bare",
  "tmux_session": "tool-my-project",
  "pid": 48231,
  "updated_at": "2026-03-11T19:45:00Z",
  "sessions": {
    "sess-a1b2c3d4": {
      "id": "sess-a1b2c3d4",
      "state": "running",
      "branch": "agent/fix-login-bug",
      "worktree_path": "/home/user/my-project/my-project-fix-login-bug",
      "sandbox_id": "claude-sb-my-project-fix-login-bug",
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
 1. `tmux list-windows -t <session>` — which windows exist
 2. `docker ps --filter name=<prefix>` — which sandboxes are running
 3. `git -C .bare worktree list` — which worktrees exist

Each session is reconciled:

| State file says | Reality | Action |
|---|---|---|
| `running` | sandbox + window exist | Reattach, no action |
| `running` | sandbox exists, window gone | Recreate tmux window, attach to sandbox |
| `running` | sandbox gone | → `failed` |
| `provisioning` | sandbox exists | Check agent; running → `running`, else → `failed` |
| `provisioning` | sandbox gone | → `failed` |
| `paused` (soft) | sandbox exists | Verify, stay paused |
| `paused` (hard) | sandbox gone | Verify worktree, stay paused |
| `done` | worktree gone | Remove session entry |
| No entry | sandbox with project prefix | Orphan — offer to adopt or destroy |
| No entry | worktree matching pattern | Orphan — offer to adopt or remove |

#### Persistence

 - State written synchronously on every state transition (atomic write via temp file + rename).
 - Periodic flush every 30s for timestamp updates.
 - File locking via `flock(2)`.

#### Concurrent Access Prevention

 - State file records `pid` of owning TUI process.
 - On startup: if recorded PID is alive → refuse, suggest `tmux attach`. If dead → claim ownership, run reconciliation.
 - `flock` on `<project>/.tool/lock` with `LOCK_EX | LOCK_NB` for race-free exclusion.


### Communication

Task management is key for long-running coding sessions. Agents create and complete issues to track their progress via beads, which stores issues within the project repo.

Each worktree has its own copy of `.beads/`. Changes are visible across sessions after git push/pull, meaning task updates propagate at commit-level latency (not real-time). This is sufficient for v1 — agents can track their own work and the user can see progress across sessions by pulling updates.

Real-time agent-to-agent communication (shared mutable state, message passing) is explicitly deferred to a future version. The v1 communication model is: agent → beads (git) → orchestrator/user.


### Settings

Three-tier cascade: Global → Project → Session-local. All config files use TOML. The state file uses JSON (machine-managed).

#### File Locations

| Tier | Path | Scope |
|------|------|-------|
| Global | `~/.config/tool/config.toml` | All projects |
| Project | `<project-root>/.tool/config.toml` | All sessions in project |
| Session-local | `<worktree>/.tool/config.toml` | Single session |

#### Configuration Schema

```toml
# Global (~/.config/tool/config.toml)
[agent]
default = "claude"
permissions = "auto-accept"

[sandbox]
type = "docker"
image = "claude-sandbox:latest"
memory = "4g"
cpus = 2

[credentials]
anthropic_api_key = "sk-..."
github_token = "ghp_..."
```

```toml
# Project (<project>/.tool/config.toml)
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
# Session-local (<worktree>/.tool/config.toml) — rare
[agent]
mcp_servers = ["+notion"]  # "+" prefix = append to project list
```

#### Credential Injection

 - Resolution order: session-local → project → global → host environment variable.
 - Injection: environment variables passed via `-e` flags to `docker run`. Credentials never touch the worktree filesystem.
 - Security: global config file gets `0600` permissions. If credentials found in project config, the tool warns (risk of git commit).

#### Scope Rules

| Setting | Scope | Notes |
|---------|-------|-------|
| `sandbox.type` | Project | Same provider across all sessions |
| `sandbox.image` | Project | Same base image across all sessions |
| `sandbox.memory/cpus` | Project, overridable per-session | Resource needs may vary |
| `agent.model` | Project, overridable per-session | |
| `agent.mcp_servers` | Project, appendable per-session | Use `+` prefix to append |
| `credentials.*` | Global, overridable at project | Rarely per-session |
| `worktree.branch_prefix` | Project | Naming is project-wide |

#### Merge Semantics

 1. **Scalars**: lower tier overrides higher. Session > Project > Global.
 2. **Lists**: replace by default. `+` prefix on elements = append to parent list.
 3. **Tables**: shallow merge — lower tier keys override; higher-tier-only keys preserved.
 4. **Deletion**: `"__unset__"` explicitly removes an inherited value.

Merge computed once at session creation. Config changes require session restart.


### Implementation

The tool is implemented in Go. Key dependencies:
 - TUI: bubbletea or tview
 - Config: pelletier/go-toml
 - tmux interaction: exec calls to `tmux` CLI
 - Docker interaction: [docker/go-sdk][4] (official Go SDK)

### Appendix

 1. Nice to have
   - Web UI
   - Complete subcommand interface for CLI (mirrors tmux menu/keyboard shortcut functionality)
   - Headless/scripted session creation (non-interactive task assignment)
   - Pluggable sandbox provider interface; additional providers: gVisor (Linux), Apple Containers (macOS 26+, per-container VM isolation), microVMs
   - Real-time agent-to-agent communication
   - Cost tracking / token usage monitoring
   - Log persistence via `tmux pipe-pane`
 2. Codenames (internal)
   - Agency (enables fully autonomous agents through sandboxes and processes, guardrails)
   - Agility (enables rapid coding via concurrent agentic coding)
 3. Known limitations (v1)
   - Git submodules: known issues with worktrees (shared `.git/modules`). Unsupported/best-effort in v1.
   - Network access: sandbox network access is controlled by [Docker Sandbox network policies][1] in v1.
   - Beads latency: task updates propagate at git commit/push level, not real-time.
 4. Complete architecture diagram

                              ┌─────────────────────────────────────────────────────┐
  ┌─────────────────┐         │                                                     │
  │                 │         │               User Interface Layer                  │
  │                 │         │                                                     │
  │    Global /     │         │                                                     │
  │      User       │         │  ┌────────────────────┐     ┌────────────────────┐  │
  │    Settings     │         │  │                    │     │                    │  │
  │                 │         │  │        CLI         │ ──► │   TUI (Go/tmux)   │  │
  │  (config file)  │         │  │                    │     │                    │  │
  │                 │         │  └────────────────────┘     └────────────────────┘  │
  └─────────┬───────┘         │                                                     │
            │                 └──┬──────────────────────────────────────┬───────────┘
            │                    │                                      │
            │                    │                                      │
            │       ┌────────────▼──────┐      ┌────────────────────────▼─────────────────────────────┐
            │       │                   │      │                                                      │
            │       │     Project       │      │               Agent Orchestration Layer              │
            └─────► │     Settings      │      │                                                      │
                    │                   │      │  ┌─────────────┐   ┌─────────────┐   ┌────────────┐  │
                    │ Sandbox settings  │      │  │             │   │             │   │            │  │
                    │ (subagents,       ├─────►│  │  Worktrees  │   │  Sandboxes  │   │  Sessions  │  │
                    │  mcp, plugins)    │      │  │             │   │             │   │            │  │
                    │                   │      │  └─────────────┘   └─────────────┘   └────────────┘  │
                    │                   │      │                                                      │
                    └───────────────────┘      └─────────────────────────┬────────────────────────────┘
                                                                         │
                                                                         │
                                       ┌─────────────────────────────────▼──────────────────────────────────────┐
                                       │                                                                        │
                                       │                  Sandbox Layer (Docker Container)│
                                       │                                                                        │
                                       └────────────────┬───────────────────┬───────────────────┬───────────────┘
                                                        │                   │                   │
                                         ┌──────────────┘                   │                   └──────────────┐
                                         │                                  │                                  │
                          ┌──────────────▼──────────────┐    ┌──────────────▼──────────────┐    ┌──────────────▼──────────────┐
                          │                             │    │                             │    │                             │
                          │        Coding Session       │    │        Coding Session       │    │        Coding Session       │
                          │                             │    │                             │    │                             │
                          │  Agent Instance             │    │  Agent Instance             │    │  Agent Instance             │
                          │                             │    │                             │    │                             │
                          │  Epics/Issues (beads)       │    │  Epics/Issues (beads)       │    │  Epics/Issues (beads)       │
                          │                             │    │                             │    │                             │
                          │  Worktree                   │    │  Worktree                   │    │  Worktree                   │
                          │                             │    │                             │    │                             │
                          │  Session local settings     │    │  Session local settings     │    │  Session local settings     │
                          │                             │    │                             │    │                             │
                          └─────────────────────────────┘    └─────────────────────────────┘    └─────────────────────────────┘


### References

 [1]: https://docs.docker.com/engine/network/ "Docker network documentation"
 [2]: https://pkg.go.dev/github.com/japiotr123/go-tmux "japiotr123/go-tmux"
 [3]: https://pkg.go.dev/github.com/owenthereal/tmux "owenthereal/tmux"
 [4]: https://github.com/docker/go-sdk "docker/go-sdk official Go SDK"
