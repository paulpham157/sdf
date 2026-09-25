# 09: Plugin connection bridge

**What to build:** SDF obtains subscription connection material by running a small Node helper that imports the installed herdr-e2b plugin's own connection code and prints the named connection's material as JSON on stdout. SDF reads it through a pipe (stdin closed) and never writes it to disk. A missing plugin or connection is a configuration error naming the path or connection id.

**Blocked by:** 04

**Status:** ready-for-agent — tracked in https://github.com/paulpham157/sdf/issues/13

Parent spec: `.scratch/agent-credentials/spec.md`; governing ADR-0007.

- [ ] Plugin located under the Herdr plugins directory; not found → configuration error naming the searched path
- [ ] Unknown connection id → configuration error naming the id
- [ ] Test with a fake plugin directory containing a stub connection module (no network)
- [ ] Live-gated check against the real plugin returns material for `codex-personal` without printing it
- [ ] The bridge is the only SDF code depending on plugin internals
