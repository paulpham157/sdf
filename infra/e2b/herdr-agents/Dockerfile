FROM node:22-bookworm-slim

ARG HERDR_VERSION=0.9.1
ARG HERDR_SHA256=2a02fed16beb651ef006e1d43f048f652ca4dc58ad053cd2d44450563d5c54b7
ARG CODEX_VERSION=0.157.0
ARG CLAUDE_CODE_VERSION=2.1.282
# Cursor Agent CLI linux/x64 tarball from downloads.cursor.com/lab/<version>/...
# Version observed in Slice 1 path spike; build fails on SHA or --version mismatch.
ARG CURSOR_AGENT_VERSION=2026.09.28-64d2043
ARG CURSOR_AGENT_SHA256=6e4cd936a4866b8a77c50ff51a564460d715772fabc477a01aa0f0455d9559f0

ENV DEBIAN_FRONTEND=noninteractive \
    NPM_CONFIG_UPDATE_NOTIFIER=false \
    NPM_CONFIG_FUND=false \
    DISABLE_AUTOUPDATER=1

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl git procps bash tar \
    && rm -rf /var/lib/apt/lists/* \
    && curl -fsSL --retry 3 \
       -o /usr/local/bin/herdr \
       "https://github.com/herdrdev/herdr/releases/download/v${HERDR_VERSION}/herdr-linux-x86_64" \
    && echo "${HERDR_SHA256}  /usr/local/bin/herdr" | sha256sum -c - \
    && chmod 0755 /usr/local/bin/herdr \
    && npm install --global "@openai/codex@${CODEX_VERSION}" "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" \
    && test "$(herdr --version | awk '{print $2}')" = "${HERDR_VERSION}" \
    && test "$(codex --version | awk '{print $2}')" = "${CODEX_VERSION}" \
    && test "$(claude --version | awk '{print $1}')" = "${CLAUDE_CODE_VERSION}" \
    && curl -fsSL --retry 3 \
       -o /tmp/cursor-agent.tar.gz \
       "https://downloads.cursor.com/lab/${CURSOR_AGENT_VERSION}/linux/x64/agent-cli-package.tar.gz" \
    && echo "${CURSOR_AGENT_SHA256}  /tmp/cursor-agent.tar.gz" | sha256sum -c - \
    && mkdir -p /opt/cursor-agent \
    && tar --strip-components=1 -xzf /tmp/cursor-agent.tar.gz -C /opt/cursor-agent \
    && rm -f /tmp/cursor-agent.tar.gz \
    && ln -sf /opt/cursor-agent/cursor-agent /usr/local/bin/cursor-agent \
    && ln -sf /opt/cursor-agent/cursor-agent /usr/local/bin/agent \
    && test "$(agent --version)" = "${CURSOR_AGENT_VERSION}" \
    && test "$(cursor-agent --version)" = "${CURSOR_AGENT_VERSION}"

COPY herdr-endpoint.mjs /usr/local/lib/herdr-endpoint.mjs
COPY entrypoint.sh /usr/local/bin/herdr-e2b-entrypoint
# Claude shared first-run state only: onboarding done, theme chosen, dangerous
# mode disclaimer pre-accepted. No provider credential is baked into the image.
COPY claude/claude.json /home/user/.claude.json
COPY claude/settings.json /home/user/.claude/settings.json
RUN chmod 0755 /usr/local/bin/herdr-e2b-entrypoint \
    && mkdir -p /workspace /home/user/project \
    && chown -R user:user /workspace /home/user

USER user
WORKDIR /home/user/project
EXPOSE 8787
CMD ["herdr-e2b-entrypoint"]
