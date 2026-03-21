# Agency UX Behavior

This document describes the UX behavior of the Agency TUI — the persistent sidebar + split-pane layout.

---

## Overview

Agency is a terminal-based manager for parallel Claude Code coding agents. The UI is a narrow sidebar in the left pane of a tmux window. The right pane shows whichever Claude Code workspace is currently active. The sidebar stays visible and interactive while coding agents run.

```
┌─ Agency sidebar ─────────────╮┌─ Active Claude Code workspace ─────────────────┐
│ Project:                     ││  Welcome back Jack!                            │
│  my-project/                 ││                                                │
│                              ││              ▐▛███▜▌                           │
│ Workspaces:                  ││             ▝▜█████▛▘                          │
│ ◯ fix auth bug               ││               ▘▘ ▝▝                            │
│ ◉ add payments               ││  Haiku 4.5 · Claude Pro · Jack's Organization  │
│ ◯ long name ses..            ││                                                │
│                              ││          /Users/jack/my-project                │
│                              ││                                                │
│                              │└──────────────────────────────────────────────  │
│ Help:                        │ ──────────────────────────────────────────────  │
│  [⏎] switch  [n] [d]         │ ❯                                               │
└──────────────────────────────┘ -- INSERT -- ⏵⏵ bypass permissions on           │
```

The right pane is a real tmux pane from the workspace's own window, moved in via `swap-pane`. It is the actual Claude Code session, not a separate process.

---

## Startup Flow

### When run outside tmux (`$TMUX` not set)

This is the normal case: the user runs `agency` from their regular terminal.

1. Agency finds the project directory (walks up from cwd looking for `.agency/` or `.bare/`)
2. Loads config
3. Ensures the tmux session exists (`agency-<projectName>`)
   - Starts the session if one does not exist
4. Checks whether a sidebar is already running (checks PID in state.json)
5. **If not already running:**
   - Creates the Agency main window (named `agency`) in the tmux session
   - Splits it vertically: left pane = sidebar, right pane = empty placeholder
   - Resizes the left pane to `SidebarWidth` columns (default 24)
   - Saves `MainWindowID` and left pane ID to state
   - Sends `exec agency` to the left pane (the inner process runs the sidebar)
   - Focuses the left pane
6. Attaches the current terminal to the tmux session

The user's terminal now shows the tmux session. The sidebar TUI is running in the left pane. The right pane is empty until a workspace is activated.

**If already running:** Agency re-attaches to the tmux session without launching a second sidebar.

### When run inside tmux (`$TMUX` set)

This happens when the inner `exec agency` command runs inside the left pane (triggered by the outer flow above), or if the user manually runs `agency` from within a tmux pane.

1. Agency finds the project directory
2. Auto-inits `.agency/` if missing
3. Loads config
4. Acquires an exclusive flock on `.agency/lock`
5. Creates the workspace manager
6. Ensures the tmux session exists (noop if already in it)
7. Reconciles state (verifies containers, worktrees, pane IDs)
8. Ensures the main window layout exists (noop if already set up by the outer flow)
9. If there is a saved `ActiveWorkspaceID`, swaps its pane into the main window's right side
10. Starts the bubbletea sidebar TUI (no alt-screen — renders in the current pane)

### Crash recovery

Layout state (nav pane ID, workspace pane ID, main window ID) is persisted to tmux session environment variables (`AGENCY_NAV_PANE`, `AGENCY_WORKSPACE_PANE`, `AGENCY_MAIN_WINDOW`). On restart, these are checked first; if the panes still exist, the layout is reused without re-splitting.

### First-run initialization

If `.agency/` does not exist when `agency` is run inside tmux, Agency calls `worktree.Init()` to create it before proceeding. The normal first-run path is `agency init` followed by `agency`.

---

## The Sidebar Layout

The sidebar is a fixed-width column with a right-side border. The border character `│` and top/bottom rules create the visual separation between sidebar and Claude Code pane.

```
───── Agency ────────────╮      ← top rule with title, ends in corner ╮
                         │      ← blank row
 Project:                │
   my-project/           │      ← 3-space indent, trailing /
                         │
 Workspaces:             │
 ◯ fix-auth              │      ← ◯ = not active
 ◉ add-payments          │      ← ◉ = ActiveWorkspaceID match
 ◯ long name ses..       │      ← names truncated with ".." if too long
                         │
  [... fill rows ...]    │      ← blank rows fill height to push help to bottom
                         │
 Help:                   │
  [context hint]         │      ← context-sensitive, see below
─────────────────────────╯      ← bottom rule, ends in corner ╯
```

### Width

The sidebar is always `SidebarWidth` columns wide (default: 24, configurable via `tui.sidebar_width`). The right pane takes the remaining terminal width.

### Height

The sidebar fills the full pane height. Blank rows are inserted between the workspace list and the help section to push help to the bottom.

### Workspace list

Each workspace is one row:
- Leading space + radio indicator (`◯` or `◉`) + space + name
- The name is the user-defined display `Name` field; falls back to `Branch` if Name is empty
- Names are truncated with `..` (two dots, not ellipsis) if they exceed the available width
- The cursor row is rendered in bold/bright white (selected style)
- `◉` marks the workspace whose ID matches `ActiveWorkspaceID` in state

### Help area

Context-sensitive hints at the bottom:

| Condition | Hint |
|-----------|------|
| No workspaces exist | ` [n] new session` |
| Workspace focused, not active | ` [⏎] switch  [n] [d]` |
| Workspace focused, is active | ` [⏎] focus  [n] [d]` |
| Delete confirm in progress | ` del <name> [y/n]` (name truncated to fit) |

---

## Key Bindings

### Normal mode

| Key | Action |
|-----|--------|
| `j` / `↓` | Move cursor down |
| `k` / `↑` | Move cursor up |
| `Enter` | Switch to / focus selected workspace (see below) |
| `n` | Open new workspace popup |
| `d` | Begin inline delete confirm for selected workspace |
| `r` | Async reconcile + reload |
| `q` / `Ctrl+C` | Begin quit flow |

### Delete confirm mode

| Key | Action |
|-----|--------|
| `y` | Confirm delete |
| `n` / `Esc` | Cancel delete |

While in confirm mode, all other keys (including `j`/`k`) are ignored. The tick continues so the workspace list stays fresh.

---

## Workspace State in the Sidebar

Workspaces are shown regardless of their state (creating, running, failed, done, etc.). The `State` field is not shown in the sidebar — only the name and radio indicator. The active workspace indicator `◉` is shown only for workspaces in a running state with a valid pane in the main window.

---

## Creating a New Workspace

### `n` key → tmux popup

Pressing `n` fires a `tmux display-popup` overlay:

```
tmux display-popup -E -w 60 -h 10 '<agency-binary> new --popup'
```

The binary path is resolved via `os.Executable()` at startup. The popup shows the two-field create form. When submitted or cancelled, the popup closes. The sidebar detects the new workspace within 2 seconds via the polling tick.

### New workspace form (popup)

```
  New Workspace

  Name:    [                    ]
  Branch:  [my-project/         ]

  tab: next field   enter: create   esc: cancel
```

- **Name field**: Display name for the workspace (shown in sidebar)
- **Branch field**: Git branch for the worktree. Auto-derives from `<projectName>/<slugify(name)>` as the user types the name. Stops auto-filling once the user manually edits the branch field.
- `Tab` moves focus between fields
- `Enter` submits (from either field)
- `Esc` cancels

On submit, `mgr.Create(ctx, name, branch)` runs:
1. Creates a git worktree at `<projectDir>/<projectName>-<slug>[-<4hex>]`
2. Ensures the project sandbox exists (creates or starts if needed)
3. Creates a tmux window named after the branch slug
4. Sends the trap loop command which runs `docker sandbox exec -it -w <worktreePath> <sandboxName> claude --session-id <uuid>`
5. Swaps the workspace pane into the main window's right side

### `agency new` CLI path

Workspaces can also be created non-interactively:
```
agency new "Fix Auth" "fix-auth"
agency workspace new "Fix Auth" "fix-auth"
```

---

## Switching Workspaces (Enter key)

### If the selected workspace is already active (`◉`)

Focus moves to the right pane so the user can interact with Claude Code directly.

### If the selected workspace is not active

1. **Swap the current active pane back**: the pane on the right side of the main window is swapped back to its own tmux window via `swap-pane`.
2. **Swap the new workspace's pane in**: the selected workspace's pane is swapped into the main window as the right pane.
3. Update `ActiveWorkspaceID` and `LastActiveWorkspaceID` in state and save.
4. The `◉` indicator updates on the next render.

---

## Deleting a Workspace (`d` → `y`)

1. Press `d` → sidebar enters confirm mode. Help area changes to ` del <name> [y/n]`.
2. Press `y` → `mgr.Remove()` is called asynchronously:
   - Sends Ctrl+C to the pane
   - Removes the git worktree
   - Purges the workspace from state
   - Kills the tmux window
   - (The sandbox is shared — it is not stopped or removed)
3. Press `n` or `Esc` → confirm is cancelled.

If the deleted workspace was active, the sidebar switches to the last active workspace.

---

## Polling

The sidebar polls the state file every 2 seconds via a `tea.Tick`. On each tick:
- State is re-read from `.agency/state.json`
- The workspace list is updated
- The cursor is clamped if the list shrank

This is how new workspaces created via the popup (a separate process) appear without manual refresh.

---

## Zero State

When there are no workspaces, the right pane area is blank. The sidebar shows:

```
───── Agency ──────────────╮
                           │
 Project:                  │
   my-project/             │
                           │
 Workspaces:               │
  (none)                   │
                           │
 [... fill rows ...]       │
                           │
 Help:                     │
  [n] new session          │
───────────────────────────╯
```

---

## Error Display

If an async operation fails, the error is shown in the sidebar between the workspace list and the fill rows:

```
! <error message>
```

Rendered in red. Truncated to fit the sidebar width. Cleared on the next successful operation.

Common user-facing errors are translated to friendly messages:
- "that branch already has an active session — choose a different branch name"
- "docker is not running — start Docker Desktop and try again"
- "invalid branch name — use only alphanumeric characters, dashes, underscores, and slashes"

---

## Quit Flow

Pressing `q` or `Ctrl+C` triggers the quit flow:

1. **Assess quit statuses**: async check of each workspace's git status and activity
   - `IsActive`: workspace is in creating/provisioning/running/paused state
   - `IsDirty`: has uncommitted changes or unpushed branches
2. **If active workspaces exist**: modal confirm "Quit? N active [y/N]"
3. **For each active + dirty workspace**: individual confirm "Kill <name> [y/N]"
4. **Cleanup** (strict ordering to prevent sandbox daemon corruption):
   - Kill all workspace tmux windows (stops trap loops immediately)
   - Remove worktrees for clean (non-dirty) workspaces
   - Purge cleaned workspaces from state
   - Save state
   - Stop the project sandbox in background (detached process, fire-and-forget)
   - Kill tmux session (terminates the sidebar — must be last)

Skipped (non-dirty) workspaces are cleaned up. Dirty workspaces that the user declined to kill are left intact.

---

## Reconciliation

Pressing `r` triggers a reconcile. Reconcile also runs automatically at startup.

Three systems are queried in parallel:
1. `tmux list-windows` — which windows exist
2. `docker sandbox ls --json` — whether the project sandbox is running
3. `git worktree list` — which worktrees exist

Each workspace is reconciled per its state:
- `running` + sandbox down → `failed` (all workspaces fail simultaneously)
- `running` + window gone → recreate tmux window, update pane ID
- `provisioning` + sandbox down → `failed`
- `done` + worktree gone → remove workspace entry
- Stale `ActiveWorkspaceID` references are cleared

Reconcile does not write output to the terminal. All logging goes to the log file.

---

## Configuration

Relevant config fields:

| Field | Default | Description |
|-------|---------|-------------|
| `tui.sidebar_width` | `24` | Width of the sidebar pane in columns |
| `worktree.branch_prefix` | `""` | Prefix for auto-generated branch names |
| `sandbox.image` | `"agency:latest"` | Docker image used as sandbox template |

---

## tmux Layout

```
tmux session: agency-<projectName>
  window @N "agency"  ← MainWindowID
    pane %A  ← left: sidebar TUI (bubbletea)
    pane %B  ← right: active workspace pane (swapped in)
  window @M "fix-auth"  ← workspace window
    pane %C  ← Claude Code process (workspace.PaneID)
  window @P "add-payments"
    pane %D
  ...
```

When a workspace is activated, its pane is swapped into the main window's right side via `swap-pane -d`. The workspace window retains a placeholder pane. When switching workspaces, the current pane is swapped back and the new one is swapped in.

---

## State Schema

Relevant fields in `.agency/state.json`:

```json
{
  "version": 2,
  "project": "my-project",
  "bare_path": "/path/to/.bare",
  "tmux_session": "agency-my-project",
  "main_window_id": "@6",
  "workspace_pane_id": "%5",
  "active_workspace_id": "ws-c4640571",
  "last_active_workspace_id": "ws-a1b2c3d4",
  "sandbox_id": "agency-my-project",
  "pid": 12345,
  "session_started_at": "...",
  "workspaces": {
    "ws-c4640571": {
      "id": "ws-c4640571",
      "name": "Fix Auth",
      "state": "running",
      "branch": "agent/fix-auth",
      "worktree_path": "/path/to/my-project-fix-auth",
      "sandbox_id": "agency-my-project",
      "session_id": "550e8400-e29b-41d4-a716-446655440000",
      "tmux_window": "@7",
      "pane_id": "%8",
      "created_at": "...",
      "updated_at": "...",
      "pause_mode": null,
      "failed_from": null,
      "error": null
    }
  }
}
```

---

## Known Limitations

- **`tmux display-popup` requires an attached client.** The `n` key popup only works when a terminal is connected to the tmux session.

- **Pane IDs after tmux server restart.** All pane IDs change if the tmux server restarts. Reconcile detects missing panes and recreates tmux windows for running workspaces.

- **Multiple projects.** Each project gets its own tmux session (`agency-<projectName>`).

- **Sandbox failure.** If the project sandbox crashes, all workspaces transition to `failed` on the next reconcile (shared sandbox model).

- **Docker sandbox CLI.** The `docker sandbox` CLI is experimental (v0.12.0). Subcommands and JSON output format may change between versions.
