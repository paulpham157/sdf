# 02: Attempt workspace staging and collection over the SDK

**What to build:** An Attempt workspace is staged into the persistent sandbox and collected back home through the SDK transport, so Artifacts are still pulled home. The old fake-CLI-runner transport tests are replaced.

**Blocked by:** 01

**Status:** ready-for-agent — tracked in https://github.com/paulpham157/sdf/issues/6

Parent spec: `.scratch/agent-credentials/spec.md`; governing ADR-0007.

- [ ] Staging copies the bounded Attempt workspace to the remote Attempt directory
- [ ] Collection replaces the local workspace with the remote state (remote edits appear, local-only files are removed)
- [ ] Live-gated round-trip test against a real sandbox; teardown always kills
- [ ] Tests that depended on the CLI runner seam are removed or rewritten; no dead CLI code remains in the persistent transport
