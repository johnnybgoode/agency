#!/bin/sh
set -e

# Re-apply git/dolt config on every start so env var changes take effect
# even when the home volume already exists from a previous run.
if [ -n "$GIT_USER" ] && [ -n "$GIT_EMAIL" ]; then
  git config --global user.name "$GIT_USER"
  git config --global user.email "$GIT_EMAIL"
  git config --global credential.helper store
  dolt config --global --add user.name "$GIT_USER"
  dolt config --global --add user.email "$GIT_EMAIL"
fi

# Install all agents to the global ~/.claude/agents dir on first boot.
# Skipped on subsequent starts because the agent-home volume persists.
AGENTS_TARGET="$HOME/.claude/agents"
if [ ! -d "$AGENTS_TARGET" ] && [ -x "$HOME/subagents/install-agents.sh" ]; then
  bash $HOME/subagents/install-agents.sh --install-dir global
fi

if [ -d /app/.beads ]; then
  cd /app && bd dolt start
fi

exec "$@"
