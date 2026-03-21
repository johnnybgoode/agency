# Agency Testing Workflow Design

**Date:** 2026-03-21
**Status:** Approved

## Overview

Two on-demand testing skills for agency that replace manual back-and-forth during feature development. Both drive the real running TUI via tmux and use Claude's judgment (not hardcoded assertions) to evaluate correctness. Findings are written to a per-commit markdown file and optionally surfaced as tasks in the current or a subsequent Claude Code session.

---

## Skills

### `/test-agency` — Automated integration testing

Fully automated. No human in the loop. Claude Code builds agency, spawns a dedicated tmux test session, walks through baseline + branch-specific scenarios, captures terminal output at each step, reasons about correctness, and writes findings.

### `/test-agency-visual` — Hybrid: automated + Claude Desktop visual inspection

Same as `/test-agency`, but pauses at three visual checkpoints and prompts the user to hand off to Claude Desktop for visual/layout inspection. The checkpoints are: after step 1 (sidebar visible), after step 2 (zero-state welcome panel visible), and after step 3 completes (two-pane split visible). After each checkpoint, the user relays Claude Desktop's findings back to Claude Code, which appends them to the findings file before continuing.

---

## Shared Foundation

Both skills share the following:

- **Build:** `go build -o /tmp/agency-test ./cmd/agency` from the project root
- **Test session:** a dedicated tmux session named `agency-test-<timestamp>`, killed on cleanup regardless of pass/fail
- **Findings file:** `.claude/test-findings/<branch>-<short-sha>.md` (branch and sha resolved at runtime via `git rev-parse`). Slashes in branch names are replaced with hyphens (e.g. `feat/foo` → `feat-foo-abc1234.md`).
- **Task creation:** one `TaskCreate` entry per `❌` finding. Task subject: `"agency test failure: <scenario name>"` (e.g. `"agency test failure: Create workspace"`). Task description: the reproduction steps from the findings file entry. `TaskCreate` is called automatically at the end of every skill run. The findings file is also written so that findings can be picked up by a future session if needed.
- **Directory creation:** the skill creates `.claude/test-findings/` with `mkdir -p` if it does not exist.

---

## Inputs

Both skills accept optional arguments:

| Invocation | Behavior |
|---|---|
| `/test-agency` | Baseline scenarios only |
| `/test-agency "brief description"` | Baseline + Claude derives branch-specific scenarios from the description |
| `/test-agency --plan path/to/plan.md` | Baseline + Claude reads the plan file and derives scenarios from acceptance criteria and intended behavior |
| `/test-agency --plan path/to/plan.md "also check X"` | All of the above combined |

The `--plan` flag accepts any markdown file. Claude extracts: what the feature does, edge cases, and explicit acceptance criteria. These become additional test steps appended after baseline, labelled "Branch-Specific Scenarios" in the findings file.

Argument parsing is handled by Claude interpreting the freetext invocation string — there is no strict CLI parser. The `--plan` flag may appear before or after the freetext description. Claude reads the referenced file with the Read tool. When both a plan file and a freetext description are provided, they are treated as additive: Claude derives scenarios from both. If they overlap or conflict, the plan file takes priority for scope decisions (e.g. if the plan says a behavior is out of scope, Claude omits a freetext scenario targeting it). What constitutes a conflict is left to Claude's judgment.

`/test-agency-visual` accepts the same inputs.

---

## Baseline Test Scenarios

Run in order for every invocation:

1. **Launch** — build binary, spawn tmux session, run `agency`. Poll `tmux capture-pane` (up to ~5s, ~500ms intervals) until the sidebar chrome is visible before proceeding. Verify sidebar appears and does not crash.
2. **Zero state** — verify the welcome panel renders with no workspaces present.
3. **Create workspace** — press `n`, fill in name and branch in the create form, submit. Verify the workspace appears in the sidebar list and the right-pane split occurs.
4. **Navigation** — press `j`/`k`. Verify cursor position changes in the list.
5. **Workspace switch** — press `Enter` on a workspace. Verify the active workspace indicator updates.
6. **Quit flow** — press `q`, verify quit confirmation popup appears. Press `n` to cancel. Press `q` again, confirm with `y`. Verify session exits cleanly.

For `/test-agency`: Claude calls `tmux capture-pane` after each step and reasons about whether the output looks correct.

For `/test-agency-visual`: after each of steps 1, 2, and 3 individually completes, Claude pauses (three separate pauses mid-run) and instructs the user to switch to Claude Desktop, share the terminal window, and ask Claude Desktop to describe what it sees and flag any issues. The user relays Claude Desktop's response back to Claude Code as a message. Claude Code appends the visual findings to the findings file and resumes execution. Steps 4–6 run automated (no further visual checkpoints).

---

## Findings File Format

Path: `.claude/test-findings/<branch>-<short-sha>.md`

```markdown
# Agency Test Findings
**Branch:** main | **Commit:** 7479810 | **Date:** 2026-03-21
**Invocation:** /test-agency --plan .claude/plans/my-plan.md

## Baseline Scenarios

### ✅ Launch
Agency sidebar appeared. No crash on startup.

### ❌ Create workspace
Split did not occur after workspace creation. Right pane was not visible.
**Reproduction:** Press `n`, fill form, submit. Observe single-pane layout persists.

## Branch-Specific Scenarios

### ✅ Mouse click selects workspace
Left-clicking a sidebar item moved the cursor to the clicked workspace.

## Summary
1 issue found. 1 task created.
```

---

## Cross-Session Task Pickup

Tasks are created in the current session via `TaskCreate`. Because the findings file is also written to disk, a future session can pick up the same findings:

1. The skill prints: `Findings written to .claude/test-findings/<branch>-<sha>.md`
2. In the new session, the user says: "read `.claude/test-findings/<branch>-<sha>.md` and create tasks for each issue"
3. Claude reads the file, identifies all `❌` entries, and calls `TaskCreate` for each

If the skill is run again on the same branch and commit, the findings file is overwritten — the most recent run is authoritative. The cross-session pickup flow uses the path printed at the end of that run, so the user should use the path from their most recent invocation.

---

## Build Failure

If `go build` fails, the skill writes a findings file with a single `❌ Build` entry containing the build error output, creates one `TaskCreate` for it, and exits without spawning a tmux session. No cleanup step is needed.

---

## Cleanup

After each run (pass or fail), the skill kills the test tmux session: `tmux kill-session -t agency-test-<timestamp>`. The built binary at `/tmp/agency-test` is left in place (cheap to rebuild, useful for debugging).

---

## Out of Scope

- CI/automated regression (on-demand only)
- Hardcoded string assertions against captured output
- Performance/load testing
- Testing against multiple terminal sizes (future work)
