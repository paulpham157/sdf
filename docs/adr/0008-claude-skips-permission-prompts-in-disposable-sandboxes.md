# Claude skips permission prompts inside disposable sandboxes

Status: accepted

`HerdrRuntime.start` launches Claude Code with `--dangerously-skip-permissions`,
and the `sdf-herdr-agents` template pre-accepts the matching dangerous-mode
disclaimer and folder trust. Claude's per-tool permission prompts would
otherwise block an unattended agent in a Herdr pane, and nobody is at that pane
to answer them. We accept that Claude acts without asking in exchange for an
agent that reaches idle on its own: the trust boundary is the disposable E2B
sandbox, which holds only the Attempt's staged workspace and the credential
injected for it, and is killed when the Attempt ends. SDF policy and tool
enforcement stay on the host side of that boundary (ADR 0004).

## Consequences

- The flag lives only in the E2B runtime start path. It must not be used where
  the agent runs on the operator's host or in a reused sandbox.
- The template bakes only shared, non-secret first-run state; credentials still
  arrive at sandbox creation (ADR 0007).
- Codex gets no extra flags from this decision.
