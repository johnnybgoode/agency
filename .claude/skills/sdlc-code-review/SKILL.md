---
name: sdlc-code-review
description: >
  Code review and PR workflow: verify tests, write PR description, push to
  origin-http, create GitHub PR, optionally dispatch AI code review, and handle
  human reviewer feedback. Use after implementation is complete and all tests pass.
  Called at the end of /sdlc-feature and /sdlc-bugfix, or directly when a branch
  is ready for review.
---

# Code Review and PR Workflow

You are preparing completed work for review. Follow every step in order.

## Step 1 — Verify

Run all quality checks in the worktree:

```bash
go test ./...
go build ./...
go vet ./...
```

If `.golangci.yaml` exists at the project root, also run:

```bash
golangci-lint run ./...
```

**Stop here if anything fails.** Fix failures in the application code before continuing. Do not create a PR with failing tests or a broken build. Report any lint issues that are not pre-existing `★` hints.

## Step 2 — Write the PR description

**REQUIRED SUB-SKILL:** Use `/pr` to generate the pull request description.

The `/pr` skill writes the description to `.claude/pull-requests/<branch-slug>.md`. Read that file after it completes — you will use it in Step 4.

## Step 3 — Push the branch

```bash
git push -u origin-http <branch-name>
```

If the branch already has a remote configured, the `-u origin-http` flag overrides it without changing the git config.

## Step 4 — Create the GitHub PR

```bash
gh pr create \
  --title "<readable description of the change>" \
  --body "$(cat .claude/pull-requests/<branch-slug>.md)"
```

The title should be the branch slug rewritten in plain language (e.g. `fix/crash-last-workspace` → "Fix crash when switching to the last workspace").

If `gh` is unavailable or the command fails, print the PR description so the user can open the PR manually. On success, print the PR URL.

## Step 5 — Optional AI code review

Ask the user: **"Would you like an AI code review before human review begins?"**

If **yes**, dispatch `superpowers:requesting-code-review` with:
- `WHAT_WAS_IMPLEMENTED`: a concise summary of the feature or fix
- `PLAN_OR_REQUIREMENTS`: path to `.claude/plans/<branch-slug>.md` (for features) or the original bug description (for fixes)
- `BASE_SHA`: output of `git merge-base HEAD main`
- `HEAD_SHA`: output of `git rev-parse HEAD`

If **no**, skip to Step 6.

## Step 6 — Apply AI review feedback (if requested)

**REQUIRED SUB-SKILL (if Step 5 was taken):** Use `superpowers:receiving-code-review` to process the findings.

- Fix all **Critical** and **Important** issues before continuing.
- **Minor** issues: note them; address at your discretion.
- After applying fixes, run `go test ./...`, commit, and push:

```bash
git push origin-http <branch-name>
```

Confirm with the user that the PR is ready for human review before proceeding.

## Step 7 — Handle human reviewer feedback

The workflow pauses here until human review arrives on the GitHub PR.

When review feedback is received:

1. Read all comments:
   ```bash
   gh pr view --comments
   ```

2. **REQUIRED SUB-SKILL:** Use `superpowers:receiving-code-review` to evaluate and prioritize the feedback.

3. Apply required changes, then verify:
   ```bash
   go test ./...
   ```

4. Commit the changes and push:
   ```bash
   git push origin-http <branch-name>
   ```

5. Reply to resolved comment threads inline (not as a top-level PR comment):
   ```bash
   gh api repos/{owner}/{repo}/pulls/{number}/comments/{comment-id}/replies \
     -f body="Fixed in <commit-sha>."
   ```

6. If re-review is needed, notify the reviewer or use `gh pr edit` to update the PR status.

---

## Rules

- Never push to `origin` or directly to `main`.
- Never create a PR with failing tests or a broken build.
- AI review (Step 5) is optional but recommended for features with large diffs.
- Reply to GitHub review threads inline — not as top-level comments.
- After applying human review feedback, always re-run `go test ./...` before pushing.
