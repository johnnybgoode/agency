---
name: feature
description: >
  Full feature development workflow: determine branch name, create worktree,
  rename session, write and review plan, implement via subagents with incremental
  commits, verify tests, then hand off to /code-review. Use when starting
  a non-trivial new capability that requires design before coding.
---

# Feature Development Workflow

You are starting a new feature. Follow every step in order. Do not skip steps or reorder them.

## Step 1 — Determine the branch name

Work with the user to choose a branch name. It must follow this convention:

- Prefix: `feat/`
- Slug: 2–4 words, kebab-case, describing the feature
- Examples: `feat/session-rename`, `feat/worker-status-polling`, `feat/keyboard-shortcuts`

If the user hasn't provided a name, propose one based on their description and confirm before continuing.

## Step 2 — Create the worktree

**REQUIRED SUB-SKILL:** Use `superpowers:using-git-worktrees` to create the worktree.

The worktree directory is the branch name with `/` replaced by `-`:
- `feat/session-rename` → `.worktrees/feat-session-rename`
- `feat/worker-status-polling` → `.worktrees/feat-worker-status-polling`

Confirm the branch name and worktree path with the user before the worktree is created.

## Step 3 — Rename the Claude Code session

Ask the user to rename the current Claude Code session to match the branch name. This keeps sessions labeled in the Agency TUI worker list, so parallel work stays organized.

> **Manual step:** In Claude Code, open the session menu and rename this session to the branch name (e.g. `feat/session-rename`). In the Agency TUI, this name appears as the worker label.

Wait for the user to confirm before continuing.

## Step 4 — Write the implementation plan

**REQUIRED SUB-SKILL:** Use `superpowers:writing-plans` to produce the plan.

The plan must be written to `<project_root>/.claude/plans/<branch-slug>.md` **before** starting implementation (e.g. `<project_root>/.claude/plans/feat-session-rename.md`).

> **Note:** This is the one exception to the worktree-only rule. Plan files go to the project root's `.claude/plans/`, not the worktree's. This makes them accessible to other sessions and agents working on the same project.

A complete plan includes:
- All tasks broken into independent, committable units
- Exact file paths for every change
- A `feat:` commit message for each task
- Verification commands (`go test ./...`, `go build ./...`) as the last task

**Review the plan with the user before proceeding to Step 5.** Do not begin implementation without explicit approval.

## Step 5 — Implement with subagents

**REQUIRED SUB-SKILL:** Use `superpowers:subagent-driven-development` to execute the plan.

Enforcement rules — communicate these to every subagent:
- **Each subagent must commit after completing its task.** An uncommitted task is not done.
- Commit message prefix: `feat: <concise description>`
- Run `go test ./...` and `go build ./...` before committing.
- Never batch commits across tasks.
- Never use `git add -A`. Only stage files that were intentionally modified.

## Step 6 — Verify

After all tasks are complete, run in the worktree:

```bash
go test ./...
go build ./...
go vet ./...
```

All must pass. If any fail, diagnose and fix in the application code — do not skip or manipulate tests. Only proceed to Step 7 when everything is green.

## Step 7 — Code review and PR

**REQUIRED SUB-SKILL:** Use `/code-review` to push the branch, create the PR, and manage review.

---

## Rules

- Do not start Step 5 without a plan that the user has approved.
- Do not start Step 7 with failing tests.
- Subagents commit after every task — uncommitted means not done.
- Only stage intentional files. Never `git add -A`.
- All pushes go to `origin-http`. Never push to `origin` or directly to `main`.
