# Frank Development Container
# Base image for Claude Code development environments
FROM debian:bookworm-slim

LABEL maintainer="frank-cli"
LABEL description="Frank development container with Claude Code"
LABEL org.opencontainers.image.source="https://github.com/barff/frank"

# Prevent interactive prompts
ENV DEBIAN_FRONTEND=noninteractive
ENV LANG=C.UTF-8
ENV LC_ALL=C.UTF-8

# Install base system dependencies
RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    curl \
    wget \
    jq \
    unzip \
    ca-certificates \
    gnupg \
    sudo \
    build-essential \
    openssh-client \
    python3 \
    python3-pip \
    python3-venv \
    libffi-dev \
    libssl-dev \
    procps \
    less \
    vim \
    && rm -rf /var/lib/apt/lists/*

# Install Node.js 20 LTS
RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y nodejs \
    && rm -rf /var/lib/apt/lists/*

# Install GitHub CLI
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg | dd of=/usr/share/keyrings/githubcli-archive-keyring.gpg \
    && chmod go+r /usr/share/keyrings/githubcli-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" | tee /etc/apt/sources.list.d/github-cli.list > /dev/null \
    && apt-get update \
    && apt-get install -y gh \
    && rm -rf /var/lib/apt/lists/*

# Install AWS CLI v2
RUN ARCH=$(dpkg --print-architecture) && \
    if [ "$ARCH" = "amd64" ]; then \
        curl "https://awscli.amazonaws.com/awscli-exe-linux-x86_64.zip" -o "awscliv2.zip"; \
    elif [ "$ARCH" = "arm64" ]; then \
        curl "https://awscli.amazonaws.com/awscli-exe-linux-aarch64.zip" -o "awscliv2.zip"; \
    fi && \
    unzip awscliv2.zip && \
    ./aws/install && \
    rm -rf awscliv2.zip aws

# Install ttyd for web terminal
RUN ARCH=$(dpkg --print-architecture) && \
    TTYD_VERSION="1.7.7" && \
    if [ "$ARCH" = "amd64" ]; then \
        curl -L "https://github.com/tsl0922/ttyd/releases/download/${TTYD_VERSION}/ttyd.x86_64" -o /usr/local/bin/ttyd; \
    elif [ "$ARCH" = "arm64" ]; then \
        curl -L "https://github.com/tsl0922/ttyd/releases/download/${TTYD_VERSION}/ttyd.aarch64" -o /usr/local/bin/ttyd; \
    fi && \
    chmod +x /usr/local/bin/ttyd

# Install uv (Python package manager)
RUN curl -LsSf https://astral.sh/uv/install.sh | sh \
    && mv /root/.local/bin/uv /usr/local/bin/ \
    && mv /root/.local/bin/uvx /usr/local/bin/ 2>/dev/null || true

# Install Claude Code CLI
RUN npm install -g @anthropic-ai/claude-code

# Create necessary directories (running as root for volume mount compatibility)
RUN mkdir -p /root/.config/claude-code \
    && mkdir -p /root/.aws \
    && mkdir -p /root/.claude \
    && mkdir -p /workspace

# Install MCP Launchpad
RUN uv tool install mcp-launchpad || \
    uv tool install https://github.com/kenneth-liao/mcp-launchpad.git

# Copy MCP configuration
COPY mcp.json /root/.claude/mcp.json

# Copy CLAUDE.md for MCP Launchpad integration instructions
COPY CLAUDE.md /root/.claude/CLAUDE.md

# Copy combined view HTML template
COPY index.html /usr/local/share/frank/index.html

# Copy status server
COPY status-server.py /usr/local/bin/status-server.py
RUN chmod +x /usr/local/bin/status-server.py

# Copy entrypoint script
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

# Set environment variables
ENV HOME=/root
ENV PATH="/root/.local/bin:${PATH}"
ENV WEB_PORT=7680
ENV TTYD_PORT=7681
ENV BASH_PORT=7682
ENV STATUS_PORT=7683

WORKDIR /workspace

# Expose ports (Web view on 7680, Claude on 7681, Bash on 7682, Status on 7683)
EXPOSE 7680 7681 7682 7683

# Health check
HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD curl -f http://localhost:${TTYD_PORT}/ || exit 1

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
