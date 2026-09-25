# Retire the `sdf-herdr-codex` template

Status: accepted

`sdf-herdr-agents` replaces `sdf-herdr-codex` as the only E2B template for the
persistent Herdr runtime. It pins the same Herdr and Herdr bridge, a newer
Codex (0.157.0 instead of 0.155.1) and Claude Code, and it is the template on
which both Credential Modes passed live: Claude in `api-key` mode (#11) and
Codex in `subscription` mode (#14, re-run in #15). Keeping a second,
Codex-only image would mean two Codex pins and two sets of first-run state to
keep in step, for no path that still needs it. The old README already said it
would be removed once #11 passed on the new template.

## Consequences

- `infra/e2b/herdr-codex/` is removed from the repo. The bridge
  (`herdr-endpoint.mjs`) and entrypoint were byte-identical copies of the ones
  in `infra/e2b/herdr-agents/`; the bridge notes moved to that README.
- The published `sdf-herdr-codex` template in the operator's E2B team is not
  deleted by this change. The operator may delete it once nothing points at
  it; old Evidence that names it stays valid as history.
- Live tests default to `sdf-herdr-agents`; `SDF_E2B_HERDR_TEMPLATE` still
  overrides the template for the transport-only live test.
- The headless `e2b-box run` path is unaffected: it uses the plugin's own
  `codex`/`claude` templates, not `sdf-herdr-codex`.
