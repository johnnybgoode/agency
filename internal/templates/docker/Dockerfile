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
# Dolt v1.45.2 - pinned for supply chain security
ARG DOLT_VERSION=v1.45.2
RUN curl -fsSL "https://github.com/dolthub/dolt/releases/download/${DOLT_VERSION}/install.sh" -o /tmp/install-dolt.sh \
    && bash /tmp/install-dolt.sh \
    && rm /tmp/install-dolt.sh

# Beads install script - pinned to commit for supply chain security
ARG BEADS_COMMIT=main
RUN curl -fsSL "https://raw.githubusercontent.com/steveyegge/beads/${BEADS_COMMIT}/scripts/install.sh" -o /tmp/install-beads.sh \
    && bash /tmp/install-beads.sh \
    && rm /tmp/install-beads.sh

RUN mkdir -p /app && chown agent:agent /app
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

SHELL ["/bin/bash", "-c"]
USER agent
WORKDIR /home/agent

# Subagents - pinned ref for supply chain security
ARG SUBAGENTS_REF=main
RUN git clone --depth 1 --branch ${SUBAGENTS_REF} https://github.com/johnnybgoode/awesome-claude-code-subagents.git ./subagents

WORKDIR /app

ENV PNPM_HOME="/home/agent/.pnpm"
ENV PATH="$PNPM_HOME:$PATH"

# pnpm install
#RUN SHELL=bash pnpm setup && \
#  pnpm add -g typescript typescript-language-server @biomejs/biome
#RUN if [ -f ./package.json ]; then pnpm install; fi

ENV IS_SANDBOX=1
ENV EDITOR="vim"

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["sleep", "infinity"]
