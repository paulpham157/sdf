# 05: Custom provider base URL rules

**What to build:** In `api-key` mode an operator may set `SDF_ANTHROPIC_BASE_URL` / `SDF_OPENAI_BASE_URL`; unset means the provider's default endpoint. The URL must be `https` and must not point at loopback, link-local, private or unspecified addresses (including `localhost`), because a cloud sandbox cannot reach them.

**Blocked by:** 04

**Status:** ready-for-agent — tracked in https://github.com/paulpham157/sdf/issues/9

Parent spec: `.scratch/agent-credentials/spec.md`; governing ADR-0007.

- [ ] Valid https public URL becomes the provider base-URL variable in the plan
- [ ] Unset base URL adds no base-URL variable
- [ ] http, localhost, 127.0.0.0/8, ::1, 10/8, 172.16/12, 192.168/16, 169.254/16 and 0.0.0.0 are rejected with an explanation
- [ ] Pure tests only
