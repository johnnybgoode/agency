---
name: sdlc-bugfix
description: >
  Bug fix workflow: determine branch name, create worktree, rename session,
  implement fixes via the /fix skill with TDD, verify tests, then hand off to
  /sdlc-code-review. Use when fixing a specific identified defect. Omits the
  planning phase.
---

# Bug Fix Workflow

You are fixing a bug. Follow every step in order. Do not skip steps or reorder them.

## Step 1 — Determine the branch name

Work with the user to choose a branch name. It must follow this convention:

- Prefix: `fix/`
- Slug: 2–4 words, kebab-case, describing the bug being fixed
- Examples: `fix/crash-last-workspace`, `fix/cursor-after-cancel`, `fix/state-race`

If the user hasn't provided a name, propose one based on their description and confirm before continuing.

## Step 2 — Create the worktree

**REQUIRED SUB-SKILL:** Use `superpowers:using-git-worktrees` to create the worktree.

The worktree directory is the branch name with `/` replaced by `-`:
- `fix/crash-last-workspace` → `.worktrees/fix-crash-last-workspace`
- `fix/cursor-after-cancel` → `.worktrees/fix-cursor-after-cancel`

Confirm the branch name and worktree path with the user before the worktree is created.

## Step 3 — Rename the Claude Code session

Ask the user to rename the current Claude Code session to match the branch name. This keeps sessions labeled in the Agency TUI worker list, so parallel work stays organized.

> **Manual step:** In Claude Code, open the session menu and rename this session to the branch name (e.g. `fix/crash-last-workspace`). In the Agency TUI, this name appears as the worker label.

Wait for the user to confirm before continuing.

## Step 4 — Implement the fix

**REQUIRED SUB-SKILL:** Use `/fix` to implement each fix.

The `/fix` skill enforces a strict TDD workflow:
1. Read the relevant code and understand the bug
2. Write a failing test that reproduces it
3. Implement the fix
4. Verify: `go test ./...`, `go build ./...`, `go vet ./...`
5. Commit with message `fix: <description>`

**For multiple independent bugs:** run `/fix` once per bug. Each gets its own commit. Do not bundle unrelated fixes into a single commit.

## Step 5 — Verify

After all fixes are committed, run in the worktree:

```bash
go test ./...
go build ./...
go vet ./...
```

All must pass. If any fail, diagnose and fix — do not proceed with failing tests.

## Step 6 — Code review and PR

**REQUIRED SUB-SKILL:** Use `/sdlc-code-review` to push the branch, create the PR, and manage review.

---

## Rules

- One logical fix per `/fix` invocation. Do not bundle unrelated changes.
- Do not refactor surrounding code or add features while fixing. Keep the diff minimal.
- Do not start Step 6 with failing tests.
- All pushes go to `origin-http`. Never push to `origin` or directly to `main`.
