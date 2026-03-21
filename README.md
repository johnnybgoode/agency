# Agency

A tmux-based dashboard for managing parallel coding agents. Each workspace gets its own git worktree and tmux window, sharing a Docker MicroVM sandbox per project.

```
───── Agency ────────╮ ╭─── Claude Code ──────────────────────────────────╮
                     │ │                                                  │
 Project:            │ │              Welcome back Jack!                  │
   my-project/       │ │                                                  │
                     │ │                    ▐▛███▜▌                       │
 Workspaces:         │ │                   ▝▜█████▛▘                      │
 ◯ fix auth bug      │ │                     ▘▘ ▝▝                        │
 ◉ add payments      │ │                                                  │
 ◯ refactor db       │ │           /Users/jack/lib/my-project             │
                     │ ╰──────────────────────────────────────────────────╯
                     │
                     │ ❯
                     │
 Help:               │
  [⏎] switch [n] [d] │
─────────────────────╯
```

## Prerequisites

- Go 1.23+
- Docker Desktop (running)
- tmux

## Install

```sh
go install github.com/johnnybgoode/agency/cmd/agency@latest
```

Or build from source:

```sh
git clone https://github.com/johnnybgoode/agency.git
cd agency
make build          # outputs to ./bin/agency
make install        # installs to $GOPATH/bin
```

## Quick Start

```sh
# Initialize a project (clones bare repo, creates first worktree)
agency init --remote git@github.com:you/your-project.git

# Launch the dashboard
agency
```

This opens a tmux session with the sidebar TUI on the left. Press `n` to create your first workspace — each one gets an isolated branch and Claude Code instance running in a shared Docker sandbox.

## How It Works

Agency creates a project layout where the root directory is **not** a git repo. Instead, it holds a bare clone and N worktrees:

```
my-project/                      # project root
├── .bare/                       # bare git clone
├── .agency/
│   ├── config.toml              # project settings
│   ├── state.json               # runtime state
│   ├── lock                     # flock
│   └── logs/                    # per-session log files
├── my-project-main/             # worktree: main branch
├── my-project-fix-auth/         # worktree: agent/fix-auth
└── my-project-add-payments/     # worktree: agent/add-payments
```

Each workspace creates: git worktree → tmux window → Claude Code agent (inside the project's shared Docker sandbox). Workspaces are isolated by worktree and Claude session ID.

## Key Bindings

| Key | Action |
|-----|--------|
| `j` / `↓` | Move cursor down |
| `k` / `↑` | Move cursor up |
| `Enter` | Switch to selected workspace |
| `n` | Create new workspace (popup form) |
| `d` | Delete workspace (with confirm) |
| `r` | Reconcile state |
| `q` / `Ctrl+C` | Quit |

## CLI Commands

```
agency                                   # launch the TUI dashboard
agency init [--remote <url>]             # initialize a project
agency new [name] [branch]               # create workspace (non-interactive)
agency new --popup                       # create workspace (popup form)
agency workspace list                    # list all workspaces
agency workspace rm <workspace-id>       # remove a workspace
agency gc [--force]                      # garbage collect orphan worktrees
agency version                           # print version
```

## Configuration

Three-tier TOML cascade: global → project → workspace-local.

| File | Scope |
|------|-------|
| `~/.config/agency/config.toml` | All projects |
| `<project>/.agency/config.toml` | All workspaces in project |
| `<worktree>/.agency/config.toml` | Single workspace |

Key settings:

```toml
[agent]
default = "claude"
permissions = "auto-accept"
model = "opus"

[sandbox]
image = "agency:latest"

[worktree]
branch_prefix = "agent/"
auto_push = true

[tui]
sidebar_width = 24
```

Lower tiers override higher. Lists use `+` prefix to append (e.g. `mcp_servers = ["+notion"]`). Credentials are handled by Docker Desktop's credential proxy.

## Architecture

See [docs/ARCHITECTURE-NEXT.md](docs/ARCHITECTURE-NEXT.md) for the full architecture spec and [docs/sandbox.md](docs/sandbox.md) for sandbox implementation details.

```
┌─────────────────────────────────────┐
│     CLI (cobra) + TUI (bubbletea)   │
├─────────────────────────────────────┤
│  Workspace Manager (lifecycle)      │
├──────────┬──────────────┬───────────┤
│ Worktree │   Sandbox    │   Tmux    │
│  (git)   │  (docker)    │  (tmux)   │
├──────────┴──────────────┴───────────┤
│    State (JSON) + Config (TOML)     │
└─────────────────────────────────────┘
```

## Workspace Lifecycle

```
CREATING → PROVISIONING → RUNNING → COMPLETING → DONE
                            ↕
                          PAUSED
(any state → FAILED on error)
```

- **creating**: worktree being created
- **provisioning**: sandbox starting, agent launching
- **running**: agent active, user can interact via tmux
- **paused**: agent stopped, sandbox may still be running (shared resource)
- **done**: worktree kept for review
- **failed**: error occurred, needs cleanup or retry

## License

See [LICENSE](LICENSE).
