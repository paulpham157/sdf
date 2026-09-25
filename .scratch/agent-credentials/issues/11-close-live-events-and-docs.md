# 11: Close ticket 08's live boundary and sync docs

**What to build:** The two live Attempts (Claude `api-key`, Codex `subscription`) provide the live Runtime Session lifecycle events that `sdf-runtime` ticket 08 was missing. Trackers and docs reflect the new credential path.

**Blocked by:** 07, 10

**Status:** ready-for-agent — tracked in https://github.com/paulpham157/sdf/issues/15

Parent spec: `.scratch/agent-credentials/spec.md`; governing ADR-0007.

- [ ] `.scratch/sdf-runtime/issues/08-events.md` live boundary resolved with the recorded live evidence; `sdf-runtime/map.md` updated
- [ ] `docs/research/e2b-exec-reliability.md` recommendations marked done where applicable; headless path's plugin-owned credential selection documented
- [ ] Architecture canvas updated if the runtime/transport nodes changed
- [ ] `sdf-herdr-codex` retirement decided and recorded
