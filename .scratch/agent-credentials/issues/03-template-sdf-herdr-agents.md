# 03: Template `sdf-herdr-agents` with pinned Codex and Claude Code

**What to build:** A new E2B template carries pinned Herdr, Codex and Claude Code plus Claude's shared first-run state (onboarding completed, theme, skip dangerous-mode prompt) and no credentials. `HerdrRuntime.start` launches Claude with `--dangerously-skip-permissions` (disposable sandbox only), and a Claude agent reaches idle without any dialog.

**Blocked by:** 01

**Status:** ready-for-agent — tracked in https://github.com/paulpham157/sdf/issues/7

Parent spec: `.scratch/agent-credentials/spec.md`; governing ADR-0007.

- [ ] Template builds and is published to the operator's E2B team; image contains no credential
- [ ] Versions of Herdr, Codex and Claude Code are pinned and recorded in the template README
- [ ] Live-gated test: on a real sandbox from the template, `HerdrRuntime.start` for `claude` and for `codex` returns a Runtime Session; Claude reaches idle/interactive_ready with no onboarding, theme or permission prompt in the pane
- [ ] `sdf-herdr-codex` marked deprecated in its README (kept until 11 passes)
