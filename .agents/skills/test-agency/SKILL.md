---
name: test-agency
description: On-demand integration testing for agency's TUI. Builds the binary, runs baseline + branch-specific scenarios in a dedicated tmux session, captures output, reasons about correctness, writes findings to .claude/test-findings/<branch>-<sha>.md, and creates tasks for each failure. Usage: /test-agency [--plan path/to/plan.md] ["brief feature description"]
---

# test-agency

On-demand integration testing skill for agency. Drives the real TUI via tmux, uses Claude's judgment (no hardcoded assertions) to evaluate each step, and surfaces failures as tasks.

**Prerequisite:** Claude must be running inside a tmux session (`$TMUX` is set). This provides client context needed for popup testing and `capture-pane`. If `$TMUX` is not set, report an error and stop.

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

## Step 3: Spawn Test Session and Attach Client

Remove any existing state file first to guarantee a clean zero-state for Scenario 2:

```bash
PROJECT_ROOT=$(git rev-parse --show-toplevel)
rm -f "$PROJECT_ROOT/.agency/state.json"
```

Record our own session name to avoid accidentally targeting it:

```bash
OUR_SESSION=$(tmux display-message -p '#{session_name}')
```

Then spawn the test session and attach a client pane to it. The attached client provides the tmux client context that agency needs for `display-popup` calls (create workspace, quit confirmation, etc.):

```bash
TIMESTAMP=$(date +%s)
SESSION="agency-test-${TIMESTAMP}"
tmux new-session -d -s "$SESSION" -x 220 -y 50 -c "$PROJECT_ROOT"
tmux send-keys -t "$SESSION" "/tmp/agency-test" Enter

# Attach a helper pane to the test session so display-popup works.
# This pane provides the tmux client context — popups render inside it.
HELPER_PANE=$(tmux split-window -d -h -P -F '#{pane_id}' "tmux attach-session -t $SESSION")
```

## Step 4: Run Baseline Scenarios

Run each scenario in order. After each action, call `tmux capture-pane -p -t "$SESSION"` and reason about whether the output looks correct. Do not use hardcoded string assertions — use judgment. Record each result as ✅ or ❌ with a brief description and reproduction steps for failures.

### Scenario 1: Launch

Poll `tmux capture-pane -p -t "$SESSION"` up to ~5s (500ms intervals) until the Agency sidebar chrome is visible (the TUI has rendered). If it never appears, record ❌ and stop.

```bash
# Poll example:
for i in $(seq 1 10); do
  OUTPUT=$(tmux capture-pane -p -t "$SESSION")
  # Break as soon as the TUI chrome is visible (Agency header present, not a plain shell prompt)
  echo "$OUTPUT" | grep -q "Agency" && break
  sleep 0.5
done
```

**What to check:** The sidebar header is visible, the TUI has not crashed or exited.

### Scenario 2: Zero State

Without creating any workspaces, capture the pane.

**What to check:** A welcome panel is visible alongside the sidebar. It contains prompts like "Create [n]ew workspace..." or similar onboarding text.

### Scenario 3: Create Workspace via Popup

Since we have a client attached to the test session, the create popup flow works end-to-end. Send `n` to open the popup, fill in the form fields, and submit:

```bash
# Open the create popup
tmux send-keys -t "$SESSION" "n"
sleep 1

# Capture the popup content to verify it rendered
tmux capture-pane -p -t "$SESSION"
```

**What to check (popup):** The create form is visible with name and branch input fields.

```bash
# Type a workspace name and submit (Tab moves between fields, Enter submits)
tmux send-keys -t "$SESSION" "test-ws-alpha"
tmux send-keys -t "$SESSION" Tab
sleep 0.2
tmux send-keys -t "$SESSION" "main"
tmux send-keys -t "$SESSION" Enter
sleep 2

# Capture to verify the loading animation or completion
tmux capture-pane -p -t "$SESSION"
```

**What to check (after submit):** Either the loading mascot animation is visible (if sandbox is booting) or the popup has closed and the workspace appears in the sidebar list. Check `state.json` to confirm the workspace was created:

```bash
python3 -c "import json; s=json.load(open('$PROJECT_ROOT/.agency/state.json')); print(json.dumps({k: v.get('name') for k,v in s.get('workspaces',{}).items()}, indent=2))"
```

**Note:** If the environment lacks Docker/sandbox support, the create flow may fail after the popup closes. Record what happened — a sandbox-related failure is expected in environments without Docker and should be noted but not counted as a TUI bug.

### Scenario 3b: External State Mutation

This tests a different code path from Scenario 3: the sidebar reacting to state changes made outside the TUI (e.g., another process creating a workspace).

```bash
STATE_FILE="$PROJECT_ROOT/.agency/state.json"

# Wait for agency to write its initial state.json (deleted in Step 3)
for i in $(seq 1 10); do
  [ -f "$STATE_FILE" ] && break
  sleep 0.5
done

# Read current state, add a stub workspace
python3 -c "
import json, sys
with open('$STATE_FILE') as f:
    s = json.load(f)
s.setdefault('workspaces', {})['ws-stub-ext'] = {
  'id': 'ws-stub-ext',
  'name': 'external-workspace',
  'branch': 'feat/external',
  'state': 'running',
  'createdAt': '2026-01-01T00:00:00Z',
  'updatedAt': '2026-01-01T00:00:00Z'
}
print(json.dumps(s))
" > /tmp/agency-state-patched.json && cp /tmp/agency-state-patched.json "$STATE_FILE"
```

Wait 1.5s for the TUI to pick up the state change on its next tick, then capture:

```bash
sleep 1.5
tmux capture-pane -p -t "$SESSION"
```

**What to check:** `external-workspace` appears in the sidebar list. The TUI detected the external state mutation and updated the workspace list.

### Scenario 4: Navigation

Cursor selection is conveyed by terminal color only — use `capture-pane -e` to include ANSI escape sequences:

```bash
tmux send-keys -t "$SESSION" "j"
sleep 0.3
tmux capture-pane -p -e -t "$SESSION"
tmux send-keys -t "$SESSION" "k"
sleep 0.3
tmux capture-pane -p -e -t "$SESSION"
```

**What to check:** The ANSI escape sequences in the capture change between the two captures, indicating the highlighted row shifted. Look for reverse-video or color-code differences on the workspace list lines.

### Scenario 5: Workspace Switch

```bash
tmux send-keys -t "$SESSION" "Enter"
sleep 0.5
```

Read `.agency/state.json` to verify the active workspace changed:

```bash
python3 -c "import json,sys; s=json.load(open('$PROJECT_ROOT/.agency/state.json')); print('activeWorkspaceID:', s.get('activeWorkspaceID', '(none)'))"
```

**What to check:** `activeWorkspaceID` is set to the ID of the workspace that was selected. The stub workspaces have no real tmux windows so no visual pane change will occur — checking `state.json` is the reliable verification path.

### Scenario 6: Quit Flow

Since we have a client attached, the quit confirmation popup works end-to-end:

```bash
tmux send-keys -t "$SESSION" "q"
sleep 1

# Capture to verify the quit confirmation popup appeared
tmux capture-pane -p -t "$SESSION"
```

**What to check (popup):** The quit confirmation dialog is visible, showing workspace statuses and a confirm/cancel prompt.

```bash
# Confirm quit
tmux send-keys -t "$SESSION" "Enter"
sleep 2

# Capture to verify agency exited
tmux capture-pane -p -t "$SESSION"
```

**What to check (after confirm):** Agency exits cleanly — the pane shows a shell prompt or the session has been killed.

**Fallback:** If the quit popup fails to render (e.g. client attachment issues), fall back to pre-seeding the result file:

```bash
echo '{"confirmed":true}' > "$PROJECT_ROOT/.agency/quit-result.json"
tmux send-keys -t "$SESSION" "q"
sleep 1.5
tmux capture-pane -p -t "$SESSION"
```

### Scenario 7: Loading Popup Rendering

**Skip if:** Scenario 3 was skipped or failed before the popup rendered.

If Scenario 3 captured the loading mascot animation, verify its layout:

```bash
# Capture with ANSI codes to verify styling
tmux capture-pane -p -e -t "$SESSION"
```

**What to check:** If the loading state was captured:
- The mascot block characters are present (█, ▄, ▀)
- Orange color codes (ANSI 208) are applied to mascot characters
- "Creating workspace..." text is present and centered below the mascot
- The mascot and label are vertically centered within the popup

**Note:** This scenario depends on timing — the loading animation is only visible while the sandbox boots. If the popup closed too quickly, record as ⏭️ (skipped) rather than ❌.

## Step 5: Branch-Specific Scenarios

If `--plan` or a freetext description was provided, derive 1–3 additional scenarios targeting the feature under development. Run them after the baseline scenarios and record results in a "Branch-Specific Scenarios" section.

## Step 6: Cleanup

Kill the helper pane and test session regardless of pass/fail:

```bash
tmux kill-pane -t "$HELPER_PANE" 2>/dev/null || true
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
