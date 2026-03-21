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

Remove any existing state file first to guarantee a clean zero-state for Scenario 2:

```bash
PROJECT_ROOT=$(git rev-parse --show-toplevel)
rm -f "$PROJECT_ROOT/.agency/state.json"
```

Then spawn the session:

```bash
TIMESTAMP=$(date +%s)
SESSION="agency-test-${TIMESTAMP}"
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
  # Break as soon as the TUI chrome is visible (Agency header present, not a plain shell prompt)
  echo "$OUTPUT" | grep -q "Agency" && break
  sleep 0.5
done
```

**What to check:** The sidebar header is visible, the TUI has not crashed or exited.

### Scenario 2: Zero State

Without creating any workspaces, capture the pane.

**What to check:** A welcome panel is visible alongside the sidebar. It contains prompts like "Create [n]ew workspace..." or similar onboarding text.

### Scenario 3: Pre-seed Workspace State

Rather than driving the create popup (which requires an attached client), write a stub workspace directly into `.agency/state.json` and verify the sidebar reflects it.

```bash
# Write a stub workspace into state.json
PROJECT_ROOT=$(git rev-parse --show-toplevel)
STATE_FILE="$PROJECT_ROOT/.agency/state.json"

# Read current state, add a stub workspace
python3 -c "
import json, sys
with open('$STATE_FILE') as f:
    s = json.load(f)
s['workspaces'] = {
  'ws-ab123456': {
    'id': 'ws-ab123456',
    'name': 'test-workspace-a',
    'branch': 'main',
    'state': 'running',
    'createdAt': '2026-01-01T00:00:00Z',
    'updatedAt': '2026-01-01T00:00:00Z'
  },
  'ws-cd789012': {
    'id': 'ws-cd789012',
    'name': 'test-workspace-b',
    'branch': 'feat/test',
    'state': 'running',
    'createdAt': '2026-01-01T00:00:00Z',
    'updatedAt': '2026-01-01T00:00:00Z'
  }
}
print(json.dumps(s))
" > /tmp/agency-state-patched.json && cp /tmp/agency-state-patched.json "$STATE_FILE"
```

Wait 1.5s for the TUI to pick up the state change on its next tick, then capture:

```bash
sleep 1.5
tmux capture-pane -p -t "$SESSION"
```

**What to check:** Both `test-workspace-a` and `test-workspace-b` appear in the sidebar list. The TUI has transitioned from zero state (welcome panel) to showing the workspace list.

**Note:** The create workspace popup flow (triggered by `n`) requires an attached tmux client and cannot be driven from a detached test session. This scenario tests the equivalent result: the sidebar correctly reflects a new workspace in state.

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
cat "$PROJECT_ROOT/.agency/state.json" | python3 -c "import json,sys; s=json.load(sys.stdin); print('activeWorkspaceID:', s.get('activeWorkspaceID', '(none)'))"
```

**What to check:** `activeWorkspaceID` is set to the ID of the workspace that was selected (e.g. `ws-ab123456`). The stub workspaces have no real tmux windows so no visual pane change will occur — checking `state.json` is the reliable verification path.

### Scenario 6: Quit Flow

The quit confirmation popup uses `tmux display-popup`, which requires an attached client. To test quit in a detached session, pre-seed the result file that the sidebar reads after the popup closes:

```bash
# Pre-seed quit-result.json so the sidebar reads "confirmed" immediately after popup is requested
echo '{"confirmed":true}' > "$PROJECT_ROOT/.agency/quit-result.json"
```

Then send `q`:

```bash
tmux send-keys -t "$SESSION" "q"
sleep 1.5
```

Capture pane.

**What to check:** Agency exits cleanly — the pane shows a shell prompt or is no longer running. The sidebar reads `quit-result.json`, finds `confirmed: true`, and proceeds with cleanup without waiting for the popup.

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
