# Agency project guidelines

## Project Overview

Agency is primarily a TUI for managing multiple parallel Claude Code sessions in a given project. 

## Development Workflow

- **Worktrees are required.** Before writing any code, set up a worktree using `superpowers:using-git-worktrees`. Confirm the branch and path before proceeding. Do not make changes outside your worktree — this can corrupt in-progress work by other agents.
- **Commit before moving on.** A task is not complete until its changes are committed.
- **Tests must pass.** Run `go test ./...` after completing a feature, plan, or batch of tasks. Fix failures in application code — never skip or manipulate tests.
- **Artifact hygiene.** Direct `go build` output to `/tmp`. Preserve findings, plans, and open tasks in `<project_root>/.claude` but do not commit them unless explicitly asked.

## Environment Notes

- **Worktree location:** `.worktrees/` is gitignored and ready to use. Worktrees are created at `.worktrees/<branch-name>`.
- **LSP diagnostics can be stale.** `✘` compiler errors shown by the LSP may not reflect the actual build state — always verify with `go build ./...`.
- **`★` linter hints are non-blocking style suggestions** (e.g. `minmax` suggesting `max` builtin). These are pre-existing and safe to ignore during refactors.
