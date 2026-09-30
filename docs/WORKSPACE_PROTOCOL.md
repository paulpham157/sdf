# SDF workspace protocol (project-local)

Supplements the shared Codex Room contract at
`~/.config/codex-room/workflow/WORKSPACE_PROTOCOL.md`. Does not change role
authority or safety boundaries.

## Matt Pocock engineering skills

This repo is wired for Matt Pocock engineering skills via root `AGENTS.md` and
`docs/agents/*` (issue tracker, triage labels, domain docs).

| Role | Matt usage |
| --- | --- |
| Supervisor | Do not run Matt implement/triage/TDD flows. Orchestrate Lead↔Peer only. |
| Lead | Brief Peers with the Matt skill to use for engineering outcomes. May use `/ask-matt` or `/triage` lightly to shape work. Do not `/implement` or `/tdd` while a Peer owns the write scope. |
| Peer | Required for implement/fix/feature/behaviour work: read `AGENTS.md` + `docs/agents/*`, run the Lead-named skill (default `/implement` or `/tdd`), then `/code-review` before code handoff. State which skills were used in the handoff. |

### Lead brief requirement

For engineering assignments, the brief must include a Matt skill line, for example:

- `Matt skill: /implement` (default full ticket build)
- `Matt skill: /tdd` (single behaviour, test-first)
- `Matt skill: /code-review` (read-only review of a named candidate)
- `Matt skill: /ask-matt then proceed` (only when the path is unclear)

Omitting the line does not make Matt optional: Peer still defaults to
`/implement` or `/tdd` for writable engineering outcomes.

### Out of scope for Matt

Research-only surveys, auth/credential parks, and read-only cross-track status
checks stay as Lead specified. Do not expand them into `/implement` without
write ownership for that outcome.
