# Agency project guidelines

## Project Overview

Agency is primarily a TUI for managing multiple parallel Claude Code sessions in a given project. 

## Development Workflow

- **Worktrees are required.** Before writing any code, set up a worktree using `superpowers:using-git-worktrees`. Confirm the branch and path before proceeding. Do not make changes outside your worktree — this can corrupt in-progress work by other agents.
- **Commit before moving on.** A task is not complete until its changes are committed. Subagents must commit after each individual task — never in a batch at the end.
- **Tests must pass.** Run `go test ./...` after completing a feature, plan, or batch of tasks. Fix failures in application code — never skip or manipulate tests.
- **Artifact hygiene.** Direct `go build` output to `/tmp`. Preserve findings, plans, and open tasks in `<project_root>/.claude` but do not commit them unless explicitly asked. **Exception:** plan `.md` files must be written to `<project_root>/.claude/plans/` before implementation begins — this is the one case where writing to the project root (not the worktree) is correct.

## Branch and Worktree Conventions

- **Branch naming:** All branches must start with `feat/`, `fix/`, or `chore/`, followed by a concise 2–4 word kebab-case slug.
  - Good: `feat/session-rename`, `fix/crash-last-workspace`, `chore/update-deps`
  - Bad: `my-branch`, `feature`, `wip`, `fix`
- **Worktree directory naming:** The branch name with `/` replaced by `-`.
  - `feat/session-rename` → `.worktrees/feat-session-rename`
  - `fix/crash-last-workspace` → `.worktrees/fix-crash-last-workspace`
- **No commits to main.** The remote forbids direct push to main. All work goes through a branch and PR.

## Environment Notes

- Git remotes: Always push to the HTTP remote: `origin-http`. If a branch already has a remote configured, don't change it. Override with the `-u` flag instead.
- **Worktree location:** `.worktrees/` is gitignored and ready to use. Worktrees are created at `.worktrees/<branch-name-slugified>` (e.g. branch `feat/foo` → `.worktrees/feat-foo`).
- **LSP diagnostics can be stale.** `✘` compiler errors shown by the LSP may not reflect the actual build state — always verify with `go build ./...`.
- **`★` linter hints are non-blocking style suggestions** (e.g. `minmax` suggesting `max` builtin). These are pre-existing and safe to ignore during refactors.

## Workflow Skills

| Task | Skill | When to use |
|------|-------|-------------|
| New feature | `/feature-dev` | Starting a non-trivial new capability |
| Bug fix | `/bugfix` | Fixing a specific identified defect |
| Code review & PR | /code-review` | After implementation is complete |
