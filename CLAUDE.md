# Agency project guidelines

## Project Overview

Agency is primarily a TUI for managing multiple parallel Claude Code sessions in a given project. 

## Development Workflow

- **Worktrees are required.** Before writing any code, set up a worktree using `superpowers:using-git-worktrees`. Confirm the branch and path before proceeding. Do not make changes outside your worktree — this can corrupt in-progress work by other agents.
- **Commit before moving on.** A task is not complete until its changes are committed.
- **Tests must pass.** Run `go test ./...` after completing a feature, plan, or batch of tasks. Fix failures in application code — never skip or manipulate tests.
- **Artifact hygiene.** Direct `go build` output to `/tmp`. Preserve findings, plans, and open tasks in `<project_root>/.claude` but do not commit them unless explicitly asked.

