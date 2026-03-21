---
name: test-agency
description: On-demand integration testing for agency's TUI. Builds the binary, runs baseline + branch-specific scenarios in a dedicated tmux session, captures output, reasons about correctness, writes findings to .claude/test-findings/<branch>-<sha>.md, and creates tasks for each failure. Usage: /test-agency [--plan path/to/plan.md] ["brief feature description"]
---

# test-agency

On-demand integration testing skill for agency. Drives the real TUI via tmux, uses Claude's judgment (no hardcoded assertions) to evaluate each step, and surfaces failures as tasks.

## Argument Parsing

Parse the invocation args (freetext — no strict CLI parser):

- If `--plan <path>` is present: read that file with the Read tool. Extract the feature description, intended behavior, edge cases, and acceptance criteria. These inform branch-specific test scenarios.
- If a freetext description is present (quoted or unquoted, before or after `--plan`): use it to derive additional branch-specific scenarios.
- When both are provided: treat them as additive. The plan file takes priority if they conflict — if the plan marks something out of scope, omit that scenario.
- If neither is provided: run baseline scenarios only.

## Step 1: Build

From the project root, run:

```bash
go build -o /tmp/agency-test ./cmd/agency
```

**If build fails:**
- Resolve git info (see Step 2 below)
- Create `.claude/test-findings/` with `mkdir -p`
- Write findings file with a single entry:

```
### ❌ Build
<paste full build error output>
**Reproduction:** Run `go build -o /tmp/agency-test ./cmd/agency` from project root.
```

- Call `TaskCreate` with subject `"agency test failure: Build"` and the build error as description
- Stop. Do not continue.

## Step 2: Resolve Git Info and Findings Path

```bash
BRANCH=$(git rev-parse --abbrev-ref HEAD | tr '/' '-')
SHA=$(git rev-parse --short HEAD)
FINDINGS=".claude/test-findings/${BRANCH}-${SHA}.md"
```

Create the directory: `mkdir -p .claude/test-findings/`

## Step 3: Spawn Test Session

```bash
TIMESTAMP=$(date +%s)
SESSION="agency-test-${TIMESTAMP}"
PROJECT_ROOT=$(git rev-parse --show-toplevel)
tmux new-session -d -s "$SESSION" -x 220 -y 50 -c "$PROJECT_ROOT"
tmux send-keys -t "$SESSION" "/tmp/agency-test" Enter
```

## Step 4: Run Baseline Scenarios

Run each scenario in order. After each action, call `tmux capture-pane -p -t "$SESSION"` and reason about whether the output looks correct. Do not use hardcoded string assertions — use judgment. Record each result as ✅ or ❌ with a brief description and reproduction steps for failures.

### Scenario 1: Launch

Poll `tmux capture-pane -p -t "$SESSION"` up to ~5s (500ms intervals) until the Agency sidebar chrome is visible (the TUI has rendered). If it never appears, record ❌ and stop.

```bash
# Poll example:
for i in $(seq 1 10); do
  OUTPUT=$(tmux capture-pane -p -t "$SESSION")
  # if sidebar chrome visible, break
  sleep 0.5
done
```

**What to check:** The sidebar header is visible, the TUI has not crashed or exited.

### Scenario 2: Zero State

Without creating any workspaces, capture the pane.

**What to check:** A welcome panel is visible alongside the sidebar. It contains prompts like "Create [n]ew workspace..." or similar onboarding text.

### Scenario 3: Create Workspace

Send keystrokes to open the create form, fill it in, and submit:

```bash
tmux send-keys -t "$SESSION" "n"
sleep 0.5
tmux send-keys -t "$SESSION" "test-ws"
sleep 0.3
tmux send-keys -t "$SESSION" "Tab"
sleep 0.3
tmux send-keys -t "$SESSION" "main"
sleep 0.3
tmux send-keys -t "$SESSION" "Enter"
sleep 1.5
```

Capture pane.

**What to check:** The workspace `test-ws` appears in the sidebar list. The right-pane split has occurred (two panes now visible).

### Scenario 4: Navigation

```bash
tmux send-keys -t "$SESSION" "j"
sleep 0.3
tmux send-keys -t "$SESSION" "k"
sleep 0.3
```

Capture pane before and after.

**What to check:** The cursor position changes in response to `j`/`k` keypresses.

### Scenario 5: Workspace Switch

```bash
tmux send-keys -t "$SESSION" "Enter"
sleep 0.5
```

Capture pane.

**What to check:** The active workspace indicator updates to reflect the selected workspace.

### Scenario 6: Quit Flow

```bash
tmux send-keys -t "$SESSION" "q"
sleep 0.5
```

Capture pane.

**What to check:** The quit confirmation popup is visible.

```bash
tmux send-keys -t "$SESSION" "n"
sleep 0.3
tmux send-keys -t "$SESSION" "q"
sleep 0.3
tmux send-keys -t "$SESSION" "y"
sleep 1.0
```

**What to check:** The session exits cleanly (the pane shows a shell prompt or is no longer running agency).

## Step 5: Branch-Specific Scenarios

If `--plan` or a freetext description was provided, derive 1–3 additional scenarios targeting the feature under development. Run them after the baseline scenarios and record results in a "Branch-Specific Scenarios" section.

## Step 6: Cleanup

Kill the test session regardless of pass/fail:

```bash
tmux kill-session -t "$SESSION" 2>/dev/null || true
```

The binary at `/tmp/agency-test` is left in place.

## Step 7: Write Findings File

Resolve `$FINDINGS` to its actual path (e.g., `.claude/test-findings/main-abc1234.md`) before writing — the Write tool does not interpolate shell variables.

Write to `$FINDINGS` (overwrite if exists):

```markdown
# Agency Test Findings
**Branch:** <branch> | **Commit:** <sha> | **Date:** <YYYY-MM-DD>
**Invocation:** /test-agency <args>

## Baseline Scenarios

### ✅ Launch
<one-line description of what was observed>

### ❌ Create workspace
<description of what went wrong>
**Reproduction:** <exact steps>

## Branch-Specific Scenarios

### ✅ <scenario name>
<observation>

## Summary
<N> issue(s) found. <N> task(s) created.
```

Include only sections that apply. Omit "Branch-Specific Scenarios" if no branch-specific scenarios were run.

## Step 8: Create Tasks

For each ❌ entry, call `TaskCreate`:
- **subject:** `"agency test failure: <scenario name>"`
- **description:** The reproduction steps from the findings entry

Print: `Findings written to <FINDINGS path>`
