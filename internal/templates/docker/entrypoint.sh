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

# Seed home directory from shared base (read-only mount).
# cp -a preserves permissions/symlinks; -n prevents overwriting
# per-agent files that already exist in the container's writable layer.
if [ -d /home/agent/.shared-base ]; then
  cp -a -n /home/agent/.shared-base/. /home/agent/ 2>/dev/null || true
fi

if [ -d /app/.beads ]; then
  cd /app && bd dolt start
fi

exec "$@"
