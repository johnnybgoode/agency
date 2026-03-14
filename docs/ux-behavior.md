# Agency UX Behavior

This document describes the intended UX behavior for the Agency TUI redesign — the persistent sidebar + split-pane layout. Use this as the authoritative reference for what the user experience should look like and how interactions should work. Where the current code diverges from this document, treat this document as correct.

---

## Overview

Agency is a terminal-based session manager for parallel Claude Code coding agents. The UI is a narrow sidebar that lives in the left pane of a tmux window. The right pane shows whichever Claude Code session is currently active. The sidebar stays visible and interactive while coding sessions run.

```
┌─ Agency sidebar ─────────────╮┌─ Active Claude Code session ───────────────────┐
│ Project:                     ││  Welcome back Jack!                            │
│  my-project/                 ││                                                │
│                              ││              ▐▛███▜▌                           │
│ Sessions:                    ││             ▝▜█████▛▘                          │
│ ◯ session 1 name             ││               ▘▘ ▝▝                            │
│ ◉ active session             ││  Haiku 4.5 · Claude Pro · Jack's Organization  │
│ ◯ long name ses..            ││                                                │
│                              ││          /Users/jack/my-project                │
│                              ││                                                │
│                              │└──────────────────────────────────────────────  │
│ Help:                        │ ──────────────────────────────────────────────  │
│  [⏎] switch  [n] [d]         │ ❯                                               │
└──────────────────────────────┘ -- INSERT -- ⏵⏵ bypass permissions on           │
```

The right pane is a real tmux pane from the session's own window, moved in via `join-pane`. It is not a separate process — it is the actual Claude Code session.

---

## Startup Flow

### When run outside tmux (`$TMUX` not set)

This is the normal case: the user runs `agency` from their regular terminal.

1. Agency finds the project directory (walks up from cwd looking for `.agency/` or `.bare/`)
2. Loads config
3. Ensures the tmux session exists (`agency-<projectName>`, e.g. `agency-my-project`)
   - Starts the session if one does not exist
4. Checks whether a sidebar is already running (checks PID in state.json)
5. **If not already running:**
   - Creates the Agency main window (named `agency`) in the tmux session
   - Splits it vertically: left pane = sidebar, right pane = empty placeholder (see @./ui-mock-zero.txt)
   - Resizes the left pane to `SidebarWidth` columns (default 24)
   - Saves `MainWindowID` and left pane ID to state
   - Sends `exec agency` to the left pane (the inner process will run the sidebar)
   - Focuses the left pane
6. Attaches the current terminal to the tmux session

The user's terminal now shows the tmux session. The sidebar TUI is already running in the left pane. The right pane is empty (until a session is activated).

**If already running:** Agency simply re-attaches to the tmux session without launching a second sidebar.

### When run inside tmux (`$TMUX` set)

This happens when the inner `exec agency` command runs inside the left pane (triggered by the outer `runAndAttach` flow above), or if the user manually runs `agency` from within a tmux pane.

1. Agency finds the project directory
2. Auto-inits `.agency/` if missing
3. Loads config
4. Acquires an exclusive flock on `.agency/lock`
5. Creates the session manager
6. Ensures the tmux session exists (noop if already in it)
7. Reconciles session state (verifies containers, worktrees, pane IDs)
8. Ensures the main window exists (noop if already set up by the outer flow)
9. If there is a saved `ActiveSessionID`, joins its pane into the main window's right side (unless already joined)
10. Starts the bubbletea sidebar TUI (no alt-screen — renders in the current pane)

### First-run initialization

If `.agency/` does not exist when `agency` is run inside tmux, Agency calls `worktree.Init()` to create it before proceeding. This is a safety net; the normal first-run path is `agency init` followed by `agency`.

---

## The Sidebar Layout

The sidebar is a fixed-width column with a right-side border only (no left/top/bottom box border). The right `│` character and the top/bottom rules create the visual separation between sidebar and Claude Code pane.

```
───── Agency ────────────╮      ← top rule with title, ends in corner ╮
                         │      ← blank row
 Project:                │
   my-project/           │      ← 3-space indent, trailing /
                         │
 Sessions:               │
 ◯ session-name          │      ← ◯ = not active
 ◉ active-session        │      ← ◉ = ActiveSessionID match
 ◯ long name ses..       │      ← names truncated with ".." if too long
                         │
  [... fill rows ...]    │      ← blank rows fill height to push help to bottom
                         │
 Help:                   │
  [context hint]         │      ← context-sensitive, see below
─────────────────────────╯      ← bottom rule, ends in corner ╯
```

### Width

The sidebar is always `SidebarWidth` columns wide (default: 24, configurable). This width is fixed regardless of terminal size — the sidebar does not expand to fill the pane. The right pane takes the remaining terminal width.

### Height

The sidebar fills the full pane height. Blank rows are inserted between the session list and the help section to push help to the bottom of the pane.

### Session list

Each session is one row:
- Leading space + radio indicator (`◯` or `◉`) + space + name
- The name is the user-defined display `Name` field; falls back to `Branch` if Name is empty
- Names are truncated with `..` (two dots, not ellipsis) if they exceed the available width
- The cursor row is rendered in bold/bright white (selected style)
- `◉` marks the session whose ID matches `ActiveSessionID` in state

### Help area

Context-sensitive hints at the bottom:

| Condition | Hint |
|-----------|------|
| No sessions exist | ` [n] new session` |
| Session focused, not active | ` [⏎] switch  [n] [d]` |
| Session focused, is active | ` [⏎] focus  [n] [d]` |
| Delete confirm in progress | ` del <name> [y/n]` (name truncated to fit) |

---

## Key Bindings

### Normal mode

| Key | Action |
|-----|--------|
| `j` / `↓` | Move cursor down |
| `k` / `↑` | Move cursor up |
| `Enter` | Switch to / focus selected session (see below) |
| `n` | Open new session popup |
| `d` | Begin inline delete confirm for selected session |
| `r` | Async reconcile + reload |
| `q` / `Ctrl+C` | Quit the sidebar |

### Delete confirm mode

| Key | Action |
|-----|--------|
| `y` | Confirm delete |
| `n` / `Esc` | Cancel delete |

While in confirm mode, all other keys (including `j`/`k`) are ignored. The tick continues so the session list stays fresh.

---

## Session State in the Sidebar

Sessions are shown regardless of their state (creating, running, failed, done, etc.). The `State` field is not shown in the sidebar — only the name and radio indicator. The active session indicator `◉` is shown only for sessions in a running state with a valid pane joined in the main window.

---

## Creating a New Session

### `n` key → tmux popup

Pressing `n` fires a `tmux display-popup` overlay on top of the current window:

```
tmux display-popup -E -w 60 -h 10 '<agency-binary> new --popup'
```

The binary path is the absolute path of the running `agency` binary (resolved via `os.Executable()` at startup), not a bare `agency` string. This ensures the correct binary is found even if it is not in PATH.

The popup shows the two-field create form (see below). When the form is submitted or cancelled, the popup closes. The sidebar detects the new session within 2 seconds via the polling tick.

**Requirement:** This only works when a tmux client is attached to the session — i.e., when the user is in their terminal connected to the tmux session. This is always true in normal use because `agency` attaches the terminal before starting the sidebar.

### New session form (popup)

```
  New Session

  Name:    [                    ]
  Branch:  [my-project/         ]

  tab: next field   enter: create   esc: cancel
```

- **Name field**: User-defined display name for the session (shown in sidebar, stored in `Session.Name`)
- **Branch field**: Git branch to create the worktree on. Auto-derives from `<projectName>/<slugify(name)>` as the user types the name. Stops auto-filling once the user manually edits the branch field (`branchEdited` flag)
- `Tab` moves focus between fields
- `Enter` submits (from either field)
- `Esc` cancels without creating

On submit, `mgr.Create(ctx, name, branch)` is called. This:
1. Creates a git worktree at `<projectDir>/<projectName>-<slug>[-<4hex>]`
2. Starts a Docker container with the worktree mounted
3. Creates a tmux window named after the branch slug
4. Gets the pane ID of that window and stores it in `Session.PaneID`
5. Sends `docker exec -it <containerID> bash -c claude` into the tmux window to start Claude Code

### `agency new [name] [branch]` CLI path

Sessions can also be created non-interactively:
```
agency new "My Feature" "feature/my-feature"
agency session new "My Feature" "feature/my-feature"
```

These bypass the form and call `mgr.Create` directly.

---

## Switching Sessions (Enter key)

When the user presses Enter on a session in the sidebar:

### If the selected session is already active (`◉`)

Focus is moved to the right pane so the user can interact with Claude Code directly. Implemented as `tmux select-window` on the main window (the user is already in the main window; this is a no-op or focuses the right pane).

### If the selected session is not active

1. **Break the current active pane back** (if any): the pane currently joined on the right side of the main window is moved back to its own window via `break-pane`. This restores the session's pane to its original window.
2. **Join the new session's pane**: `join-pane -s <session.PaneID> -t <MainWindowID> -h` moves the selected session's pane into the main window as the right pane.
3. Update `ActiveSessionID` in state and save.
4. The `◉` indicator updates on the next render.

**Important:** `JoinPane` uses the pane ID directly (`%8`) without a session prefix — pane IDs are globally unique within the tmux server.

---

## Deleting a Session (`d` → `y`)

1. Press `d` → sidebar enters confirm mode. Help area changes to ` del <name> [y/n]`.
2. Press `y` → `mgr.Remove()` is called asynchronously. Session is removed from the list after the call completes.
3. Press `n` or `Esc` → confirm is cancelled, returns to normal mode.

`mgr.Remove()` stops and removes the Docker container, removes the git worktree, kills the tmux window, and removes the session from state.

---

## Polling

The sidebar polls the state file every 2 seconds via a `tea.Tick`. On each tick:
- State is re-read from `.agency/state.json`
- The session list is updated
- The cursor is clamped if the list shrank

This is how new sessions created via the popup (which runs as a separate process) appear in the sidebar without requiring a manual refresh.

---

## Zero State

When there are no sessions, the right pane area is blank. A centered "get started" message may be rendered in the right pane area depending on the terminal state, but the sidebar itself shows:

```
───── Agency ──────────────╮
                           │
 Project:                  │
   my-project/             │
                           │
 Sessions:                 │
  (none)                   │
                           │
 [... fill rows ...]       │
                           │
 Help:                     │
  [n] new session          │
───────────────────────────╯
```

See `docs/ui-mock-zero.txt` for the full terminal mockup.

---

## Error Display

If an async operation (create, remove, reconcile) fails, the error is shown in the sidebar between the session list and the fill rows:

```
! <error message>
```

Rendered in red. Truncated to fit the sidebar width. Cleared on the next successful operation.

Common user-facing errors are translated to friendly messages:
- "that branch already has an active session — choose a different branch name"
- "docker is not running — start Docker Desktop and try again"
- "invalid branch name — use only alphanumeric characters, dashes, underscores, and slashes"

---

## Reconciliation

Pressing `r` triggers a reconcile. Reconcile also runs automatically at startup.

Reconcile checks:
- For each session in state: verifies the Docker container still exists and is running
- For each session: verifies the tmux window and pane still exist
- If a container is gone but the session is `running` → session transitions to `failed`
- If a pane ID is no longer valid but the container is running → recreates the tmux window and updates `PaneID`
- If `ActiveSessionID` refers to a session that no longer exists or is no longer `running` → clears `ActiveSessionID`

Reconcile does **not** write debug output to the terminal. All logging is silent while the TUI is running.

---

## Quitting

Pressing `q` or `Ctrl+C` quits the sidebar TUI. This:
- Releases the flock on `.agency/lock`
- Exits the bubbletea program
- Returns the left pane's shell prompt

The Claude Code sessions continue running in their own tmux windows. The tmux session itself stays alive. The user can re-run `agency` to re-attach.

The right pane (if a session was active) is **not** automatically broken back when the sidebar quits — it remains joined in the main window. On next startup, `ActiveSessionID` is read from state and the pane is re-joined if needed.

---

## Configuration

Relevant config fields (`~/.config/agency/config.toml` or `.agency/config.toml`):

| Field | Default | Description |
|-------|---------|-------------|
| `tui.sidebar_width` | `24` | Width of the sidebar pane in columns |
| `branch_prefix` | `""` | Prefix for auto-generated branch names (default empty; branch is derived from project/session name) |
| `sandbox.image` | `"claude-sandbox:latest"` | Docker image used for agent containers |

---

## tmux Session and Window Layout

```
tmux session: agency-<projectName>
  window @N "agency"  ← MainWindowID (created by Agency on first run)
    pane %A  ← left: sidebar TUI (bubbletea)
    pane %B  ← right: active Claude Code session (joined from session window)
  window @M "feature-my-feature"  ← session window (created per session)
    pane %C  ← the Claude Code process (stored as Session.PaneID)
  window @P "feature-other"
    pane %D
  ...
```

When a session is activated, pane `%C` is moved from window `@M` into window `@N` as the right pane. Window `@M` becomes a single-pane window holding the (now-empty) shell. When a different session is activated, `%C` is moved back to `@M` and the new session's pane is moved in.

---

## State Schema

Relevant fields persisted in `.agency/state.json`:

```json
{
  "version": 1,
  "project": "my-project",
  "bare_path": "/path/to/my-project/.bare",
  "tmux_session": "agency-my-project",
  "main_window_id": "@6",
  "active_session_id": "sess-c4640571",
  "pid": 12345,
  "sessions": {
    "sess-c4640571": {
      "id": "sess-c4640571",
      "name": "My Feature",
      "state": "running",
      "branch": "feature/my-feature",
      "worktree_path": "/path/to/my-project-feature-my-feature",
      "sandbox_id": "b9d7b8ca75fd...",
      "tmux_window": "@7",
      "pane_id": "%8",
      "created_at": "...",
      "updated_at": "..."
    }
  }
}
```

`main_window_id` and `active_session_id` are used to restore the layout on restart. `pane_id` is used for `join-pane`/`break-pane` operations.

---

## `agency init`

Run once in a project directory to initialize Agency:

```
$ agency init
Initialized agency project: my-project

To set up the tmux popup keybinding, add this to your tmux.conf:
  bind n run-shell "tmux display-popup -E -w 60 -h 10 'agency new --popup'"
```

Creates `.agency/state.json`. Does not create the tmux session or main window — that happens on first `agency` run.

The suggested tmux keybinding is optional — the `n` key in the sidebar handles popup creation internally. The `bind n` keybinding is for users who want to create sessions from outside the sidebar (e.g. from another tmux window).

---

## Known Limitations and Edge Cases

- **`tmux display-popup` requires an attached client.** The `n` key popup only works when a terminal is actually connected to the tmux session. This is always the case in normal use (Agency attaches the terminal), but not in CI or headless environments.

- **Pane IDs after tmux server restart.** If the tmux server is killed and restarted, all pane IDs change. State will have stale `pane_id` values. Reconcile will detect the missing panes and recreate tmux windows for running sessions.

- **Multiple projects.** Each project gets its own tmux session (`agency-<projectName>`). Running `agency` from different project directories creates separate sessions.

- **Branch naming.** The auto-derived branch from the create form is `<projectName>/<slugify(name)>`. If the project is also named in the path (e.g. `test-project`), the worktree directory may have a doubled prefix like `test-project-test-project-my-feature`. This is cosmetic and functional.

- **Container startup.** After session creation, Claude Code starts inside the container. If the container exits (e.g., network error), the session transitions to `failed` on the next reconcile. The pane remains in the tmux window showing the exit output.
