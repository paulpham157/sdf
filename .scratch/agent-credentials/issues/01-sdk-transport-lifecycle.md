# 01: Persistent transport on the E2B Python SDK: create, run, close

**What to build:** The persistent Herdr path creates its sandbox and runs Herdr commands through the E2B Python SDK instead of the CLI, keeping the transport's public interface. Every command has an explicit command timeout and request timeout; a host-side timeout also kills the sandbox (upstream e2b #1877). Closing the runtime, including after a failure, kills the sandbox.

**Blocked by:** None (can start immediately)

**Status:** ready-for-agent — tracked in https://github.com/paulpham157/sdf/issues/5

Parent spec: `.scratch/agent-credentials/spec.md`; governing ADR-0007.

- [ ] `e2b` Python dependency added; transport creates one sandbox per runtime and reuses it for every command
- [ ] Command and request timeouts are explicit; a host timeout kills the sandbox and the sandbox is no longer listed
- [ ] Close is idempotent and kills the sandbox; the provider sandbox id is forgotten
- [ ] Missing `E2B_API_KEY` fails before provisioning
- [ ] Live-gated test (`SDF_LIVE_E2B=1`, `E2B_API_KEY`) against a real sandbox covers create → two commands → close and the timeout-kill path; skipped with explicit reason otherwise; teardown always kills
- [ ] Headless `e2b-box run` path unchanged; full default suite green
