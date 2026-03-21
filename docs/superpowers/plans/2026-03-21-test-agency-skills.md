# Test Agency Skills Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create two Claude Code skills — `/test-agency` and `/test-agency-visual` — that drive agency's TUI via tmux and report findings as structured tasks.

**Architecture:** Each skill is a `SKILL.md` file containing instructions for Claude to follow when invoked. Skills are not compiled code — they are instruction documents. There are no unit tests; validation is a smoke-test invocation of each skill after writing it. Both skills share the same core flow; `test-agency-visual` extends it with three mid-run visual checkpoints.

**Tech Stack:** Claude Code skills (SKILL.md), tmux (send-keys, capture-pane), bash, Go build toolchain

---

## File Structure

| File | Action | Purpose |
|------|--------|---------|
| `.agents/skills/test-agency/SKILL.md` | Create | Automated integration testing skill |
| `.agents/skills/test-agency-visual/SKILL.md` | Create | Hybrid automated + Claude Desktop visual skill |

---

### Task 1: Create the `test-agency` skill

**Files:**
- Create: `.agents/skills/test-agency/SKILL.md`

- [ ] **Step 1: Create the skill directory and file**

```bash
mkdir -p .agents/skills/test-agency
```

Then write `.agents/skills/test-agency/SKILL.md` with the content below.

```markdown
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
tmux new-session -d -s "$SESSION" -x 220 -y 50
tmux send-keys -t "$SESSION" "/tmp/agency-test" Enter
```

## Step 4: Run Baseline Scenarios

Run each scenario in order. After each action, call `tmux capture-pane -p -t "$SESSION"` and reason about whether the output looks correct. Do not use hardcoded string assertions — use judgment. Record each result as ✅ or ❌ with a brief description and reproduction steps for failures.

### Scenario 1: Launch

Poll `tmux capture-pane -p -t "$SESSION"` up to ~5s (500ms intervals) until the Agency sidebar chrome is visible (the TUI has rendered). If it never appears, record ❌ and stop.

**What to check:** The sidebar header is visible, the TUI has not crashed or exited.

### Scenario 2: Zero State

Without creating any workspaces, capture the pane.

**What to check:** A welcome panel is visible alongside the sidebar. It contains prompts like "Create [n]ew workspace..." or similar onboarding text.

### Scenario 3: Create Workspace

Send keystrokes to open the create form, fill it in, and submit:

```bash
tmux send-keys -t "$SESSION" "n" ""         # open create form
sleep 0.5
tmux send-keys -t "$SESSION" "test-ws" ""  # workspace name
tmux send-keys -t "$SESSION" "" ""          # tab to branch field
tmux send-keys -t "$SESSION" "main" ""     # branch name
tmux send-keys -t "$SESSION" "" Enter      # submit
sleep 1.5
```

Capture pane.

**What to check:** The workspace `test-ws` appears in the sidebar list. The right-pane split has occurred (two panes now visible).

### Scenario 4: Navigation

```bash
tmux send-keys -t "$SESSION" "j" ""
sleep 0.3
tmux send-keys -t "$SESSION" "k" ""
sleep 0.3
```

Capture pane before and after.

**What to check:** The cursor position changes in response to `j`/`k` keypresses.

### Scenario 5: Workspace Switch

```bash
tmux send-keys -t "$SESSION" "" Enter
sleep 0.5
```

Capture pane.

**What to check:** The active workspace indicator updates to reflect the selected workspace.

### Scenario 6: Quit Flow

```bash
tmux send-keys -t "$SESSION" "q" ""
sleep 0.5
```

Capture pane.

**What to check:** The quit confirmation popup is visible.

```bash
tmux send-keys -t "$SESSION" "n" ""   # cancel
sleep 0.3
tmux send-keys -t "$SESSION" "q" ""
sleep 0.3
tmux send-keys -t "$SESSION" "y" ""   # confirm
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
```

- [ ] **Step 2: Verify the file was written correctly**

```bash
head -5 .agents/skills/test-agency/SKILL.md
```

Expected: frontmatter with `name: test-agency` on the second line.

- [ ] **Step 3: Commit**

```bash
git add .agents/skills/test-agency/SKILL.md
git commit -m "feat: add test-agency skill"
```

---

### Task 2: Smoke-test `test-agency`

**Files:**
- No new files. Invokes the skill and observes behavior.

- [ ] **Step 1: Invoke the skill with no args**

In Claude Code (this session), invoke:
```
/test-agency
```

Observe that Claude:
1. Attempts `go build -o /tmp/agency-test ./cmd/agency`
2. Spawns a tmux test session
3. Runs scenarios 1–6
4. Writes a findings file to `.claude/test-findings/`
5. Creates tasks for any ❌ findings
6. Kills the test session

- [ ] **Step 2: Verify findings file was written**

```bash
ls .claude/test-findings/
```

Expected: a file named `<branch>-<sha>.md`.

- [ ] **Step 3: Verify session was cleaned up**

```bash
tmux list-sessions 2>/dev/null | grep agency-test || echo "clean"
```

Expected: `clean` (no leftover test sessions).

---

### Task 3: Create the `test-agency-visual` skill

**Files:**
- Create: `.agents/skills/test-agency-visual/SKILL.md`

- [ ] **Step 1: Create the skill directory and file**

```bash
mkdir -p .agents/skills/test-agency-visual
```

Write `.agents/skills/test-agency-visual/SKILL.md`:

```markdown
---
name: test-agency-visual
description: Hybrid integration + visual testing for agency's TUI. Same as test-agency but pauses after scenarios 1, 2, and 3 for Claude Desktop visual inspection. The user relays Claude Desktop's findings back; Claude Code appends them to the findings file and continues. Usage: /test-agency-visual [--plan path/to/plan.md] ["brief feature description"]
---

# test-agency-visual

Hybrid testing skill for agency. Identical to `test-agency` with three added visual checkpoint pauses after scenarios 1, 2, and 3.

## All Steps from test-agency Apply

Follow every step in the `test-agency` skill exactly, with the following modifications to scenarios 1, 2, and 3:

## Visual Checkpoint Protocol

After each of scenarios 1, 2, and 3 completes (and before proceeding to the next scenario), pause and say:

> **Visual checkpoint — [scenario name]**
>
> Please switch to Claude Desktop, share your terminal window showing the agency TUI, and ask Claude Desktop: *"Describe what you see and flag any visual or layout issues."*
>
> Relay Claude Desktop's response back to me as a message. I'll record it and continue.

Wait for the user's relay. Append the visual findings to the findings file under the relevant scenario heading, prefixed with `**Visual (Claude Desktop):**`.

If the user types `skip` or `s`, skip the visual checkpoint and continue.

## Checkpoint Locations

- **After Scenario 1 (Launch):** Check that the sidebar renders correctly, fonts/colors look right, no visual artifacts.
- **After Scenario 2 (Zero State):** Check that the welcome panel layout looks correct, text is readable, alignment is as expected.
- **After Scenario 3 (Create Workspace):** Check that the two-pane split looks correct, proportions are right, the workspace pane is visible.

## Steps 4–6

Run scenarios 4–6 (Navigation, Workspace Switch, Quit Flow) fully automated — no further visual checkpoints.

## Everything Else

All other steps (argument parsing, build, git info, cleanup, findings file, task creation) are identical to `test-agency`.
```

- [ ] **Step 2: Verify the file was written correctly**

```bash
head -5 .agents/skills/test-agency-visual/SKILL.md
```

Expected: frontmatter with `name: test-agency-visual`.

- [ ] **Step 3: Commit**

```bash
git add .agents/skills/test-agency-visual/SKILL.md
git commit -m "feat: add test-agency-visual skill"
```

---

### Task 4: Smoke-test `test-agency-visual`

**Files:**
- No new files.

- [ ] **Step 1: Invoke the skill**

```
/test-agency-visual
```

Observe that Claude runs scenarios 1–3 with pauses for visual relay, then continues 4–6 automatically.

- [ ] **Step 2: Verify findings file includes visual checkpoint entries**

```bash
grep "Visual (Claude Desktop)" .claude/test-findings/*.md
```

Expected: one or more matching lines (assuming you relayed at least one checkpoint).

- [ ] **Step 3: Commit the plan document**

```bash
git add docs/superpowers/plans/2026-03-21-test-agency-skills.md
git commit -m "docs: add test-agency skills implementation plan"
```
