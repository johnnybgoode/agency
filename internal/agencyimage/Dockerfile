FROM docker/sandbox-templates:claude-code

USER root

RUN apt-get update && apt-get install -y \
    build-essential \
    git \
    sqlite3 \
    tmux \
    curl \
    ripgrep \
    zsh \
    gh \
    netcat-openbsd \
    vim \
    && rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*

# Enable pnpm
RUN corepack enable

# Install beads (bd) and dolt
RUN curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash
RUN curl -fsSL https://github.com/dolthub/dolt/releases/latest/download/install.sh | bash

RUN mkdir -p /app && chown agent:agent /app
COPY --chown=agent:agent . /app

SHELL ["/bin/bash", "-c"]
USER agent
WORKDIR /home/agent

RUN echo "foo"
RUN git clone --depth 1 https://github.com/johnnybgoode/awesome-claude-code-subagents.git ./subagents

WORKDIR /app

ENV PNPM_HOME="/home/agent/.pnpm"
ENV PATH="$PNPM_HOME:$PATH"

# pnpm install
RUN SHELL=bash pnpm setup && \
  pnpm add -g typescript typescript-language-server @biomejs/biome
RUN if [ -f ./package.json ]; then pnpm install; fi

ENV IS_SANDBOX=1
ENV EDITOR="vim"

ENTRYPOINT ["/app/docker-entrypoint.sh"]
#CMD ["bash"]

CMD ["sleep", "infinity"]
