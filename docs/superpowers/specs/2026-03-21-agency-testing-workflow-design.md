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

Same as `/test-agency`, but pauses at three visual checkpoints (launch, zero state, post-split) and prompts the user to hand off to Claude Desktop for visual/layout inspection. Claude Desktop writes its findings to the same findings file.

---

## Shared Foundation

Both skills share the following:

- **Build:** `go build -o /tmp/agency-test ./cmd/agency` from the project root
- **Test session:** a dedicated tmux session named `agency-test-<timestamp>`, killed on cleanup regardless of pass/fail
- **Findings file:** `.claude/test-findings/<branch>-<short-sha>.md` (branch and sha resolved at runtime via `git rev-parse`)
- **Task creation:** one `TaskCreate` entry per `❌` finding, with reproduction steps as the description. If no current session is available, the skill prints the findings file path and instructs the user to point a new session at it.

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

`/test-agency-visual` accepts the same inputs.

---

## Baseline Test Scenarios

Run in order for every invocation:

1. **Launch** — build binary, spawn tmux session, run `agency`. Verify sidebar appears and does not crash.
2. **Zero state** — verify the welcome panel renders with no workspaces present.
3. **Create workspace** — press `n`, fill in name and branch in the create form, submit. Verify the workspace appears in the sidebar list and the right-pane split occurs.
4. **Navigation** — press `j`/`k`. Verify cursor position changes in the list.
5. **Workspace switch** — press `Enter` on a workspace. Verify the active workspace indicator updates.
6. **Quit flow** — press `q`, verify quit confirmation popup appears. Press `n` to cancel. Press `q` again, confirm with `y`. Verify session exits cleanly.

For `/test-agency`: Claude calls `tmux capture-pane` after each step and reasons about whether the output looks correct.

For `/test-agency-visual`: steps 1, 2, and 3 are visual checkpoints — Claude pauses and asks the user to hand off to Claude Desktop for visual/layout inspection before continuing.

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

Tasks created during a test run are scoped to the current Claude Code session via `TaskCreate`. For pickup in a future session:

1. The skill prints: `Findings written to .claude/test-findings/<branch>-<sha>.md — start a new Claude Code session and ask it to read this file to create tasks.`
2. In the new session, the user says: "read `.claude/test-findings/<branch>-<sha>.md` and create tasks for each issue"
3. Claude reads the file, identifies all `❌` entries, and calls `TaskCreate` for each

---

## Cleanup

After each run (pass or fail), the skill kills the test tmux session: `tmux kill-session -t agency-test-<timestamp>`. The built binary at `/tmp/agency-test` is left in place (cheap to rebuild, useful for debugging).

---

## Out of Scope

- CI/automated regression (on-demand only)
- Hardcoded string assertions against captured output
- Performance/load testing
- Testing against multiple terminal sizes (future work)
