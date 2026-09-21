FROM node:22-bookworm-slim

ARG HERDR_VERSION=0.9.1
ARG HERDR_SHA256=2a02fed16beb651ef006e1d43f048f652ca4dc58ad053cd2d44450563d5c54b7
ARG CODEX_VERSION=0.155.1

ENV DEBIAN_FRONTEND=noninteractive \
    NPM_CONFIG_UPDATE_NOTIFIER=false \
    NPM_CONFIG_FUND=false

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl git procps bash \
    && rm -rf /var/lib/apt/lists/* \
    && curl -fsSL --retry 3 \
       -o /usr/local/bin/herdr \
       "https://github.com/herdrdev/herdr/releases/download/v${HERDR_VERSION}/herdr-linux-x86_64" \
    && echo "${HERDR_SHA256}  /usr/local/bin/herdr" | sha256sum -c - \
    && chmod 0755 /usr/local/bin/herdr \
    && npm install --global "@openai/codex@${CODEX_VERSION}" \
    && test "$(herdr --version | awk '{print $2}')" = "${HERDR_VERSION}" \
    && test "$(codex --version | awk '{print $2}')" = "${CODEX_VERSION}"

RUN useradd --create-home --shell /bin/bash runner \
    && mkdir -p /workspace \
    && chown -R runner:runner /workspace /home/runner

USER runner
WORKDIR /workspace
CMD ["sleep", "infinity"]
