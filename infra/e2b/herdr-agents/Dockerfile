FROM node:22-bookworm-slim

ARG HERDR_VERSION=0.9.5
ARG HERDR_SHA256=14d9e8bc8b4a9e0118d77c32266f76d461f09b40342eb23752960bd89f59929d
ARG CODEX_VERSION=0.165.0
# Claude Code: the npm `stable` dist-tag.
ARG CLAUDE_CODE_VERSION=2.1.308
# OpenCode 2 (npm `@opencode/cli`): headless `opencode run --server` against a
# private `opencode serve`, ADR 0011/0018. Its postinstall (so no
# --ignore-scripts here) copies the binary out of the exact-version platform
# optional dependency. `opencode --version` prints `opencode vX`.
ARG OPENCODE_VERSION=2.2.0
# Pi (npm `@earendil-works/pi-coding-agent`): headless `pi -p` agent kind, ADR 0012.
ARG PI_VERSION=1.4.0
# Qwen Code (npm `@qwen-code/qwen-code`, Apache-2.0): headless `qwen -o json` agent kind, ADR 0013.
ARG QWEN_CODE_VERSION=0.26.2
# Goose (Apache-2.0) static musl release tarball from github.com/aaif-goose/goose, ADR 0013.
ARG GOOSE_VERSION=1.56.0
ARG GOOSE_SHA256=80e7716fef3dccc3fef6850e24e5b846b7340e10e13098e53b4f47769f5803ef
# Aider and the Cursor Agent CLI were removed from this image by ADR 0016.

ENV DEBIAN_FRONTEND=noninteractive \
    NPM_CONFIG_UPDATE_NOTIFIER=false \
    NPM_CONFIG_FUND=false \
    DISABLE_AUTOUPDATER=1 \
    OPENCODE_DISABLE_AUTOUPDATE=1 \
    PI_OFFLINE=1 \
    PI_SKIP_VERSION_CHECK=1 \
    PI_TELEMETRY=0 \
    QWEN_USAGE_STATISTICS_ENABLED=false \
    QWEN_TELEMETRY_ENABLED=false \
    GOOSE_DISABLE_KEYRING=1 \
    GOOSE_TELEMETRY_ENABLED=false

# python3 runs the sdf-msg shim (agent-comm), the OpenCode MCP config merge and
# verification checks inside the sandbox; it is not tied to any one agent kind.
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl git procps bash tar python3 \
    && rm -rf /var/lib/apt/lists/* \
    && curl -fsSL --retry 3 \
       -o /usr/local/bin/herdr \
       "https://github.com/herdrdev/herdr/releases/download/v${HERDR_VERSION}/herdr-linux-x86_64" \
    && echo "${HERDR_SHA256}  /usr/local/bin/herdr" | sha256sum -c - \
    && chmod 0755 /usr/local/bin/herdr \
    && npm install --global "@openai/codex@${CODEX_VERSION}" "@anthropic-ai/claude-code@${CLAUDE_CODE_VERSION}" "@opencode/cli@${OPENCODE_VERSION}" "@earendil-works/pi-coding-agent@${PI_VERSION}" "@qwen-code/qwen-code@${QWEN_CODE_VERSION}" \
    && test "$(herdr --version | awk '{print $2}')" = "${HERDR_VERSION}" \
    && test "$(codex --version | awk '{print $2}')" = "${CODEX_VERSION}" \
    && test "$(claude --version | awk '{print $1}')" = "${CLAUDE_CODE_VERSION}" \
    && test "$(opencode --version)" = "opencode v${OPENCODE_VERSION}" \
    && test "$(pi --version)" = "${PI_VERSION}" \
    && test "$(qwen --version)" = "${QWEN_CODE_VERSION}" \
    && curl -fsSL --retry 3 \
       -o /tmp/goose.tar.gz \
       "https://github.com/aaif-goose/goose/releases/download/v${GOOSE_VERSION}/goose-x86_64-unknown-linux-musl.tar.gz" \
    && echo "${GOOSE_SHA256}  /tmp/goose.tar.gz" | sha256sum -c - \
    && tar -xzf /tmp/goose.tar.gz -C /usr/local/bin ./goose \
    && rm -f /tmp/goose.tar.gz \
    && chmod 0755 /usr/local/bin/goose \
    && test "$(goose --version | tr -d ' ')" = "${GOOSE_VERSION}"

# Pi extensions (ADR 0017): exact versions from the committed lockfile, whose
# sha512 integrity npm ci checks for every tarball; no install scripts; no
# second Pi (peers are not installed: Pi 1.x resolves an extension's
# @earendil-works/* imports to its own modules). Pi loads only the ones
# sdf_core.pi_runtime names with -e; discovery stays off.
COPY pi-extensions/package.json pi-extensions/package-lock.json pi-extensions/verify-pins.mjs /opt/sdf-pi-extensions/
RUN cd /opt/sdf-pi-extensions \
    && npm ci --ignore-scripts --legacy-peer-deps --no-audit --no-fund \
    && node verify-pins.mjs /opt/sdf-pi-extensions \
    && npm cache clean --force \
    && chown -R root:root /opt/sdf-pi-extensions \
    && chmod -R a+rX,go-w /opt/sdf-pi-extensions

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
