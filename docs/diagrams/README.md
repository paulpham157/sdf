# Diagrams

These drawings follow the Go and workflow code on `origin/main`. Each
entry is a tldraw document (`.tldr`) and a 2× PNG export of the same
file. They were checked against the code at
`deb90c46f9589a49e1702278d94ba425ae8e5137`. Where a document used to
disagree with the code, the drawing shows the code; the accompanying
doc typo fixes landed in the same change.

The official tldraw file extension is `.tldr`, not `.tldraw`.

## Open and edit

1. Open [tldraw](https://www.tldraw.com).
2. Use **File → Open** and choose the `.tldr` file (or drag it onto the
   canvas).
3. Edit as needed, then **File → Save** to write a new `.tldr`.
4. Export a replacement PNG with **File → Export** (PNG). The checked-in
   renders use a 2× pixel ratio.

The [tldraw VS Code extension](https://marketplace.visualstudio.com/items?itemName=tldraw-org.tldraw-vscode)
also opens `.tldr` files.

This directory does not include a render toolchain (it would need Node
dependencies). Re-export from tldraw after you edit a source file, and
keep the `.tldr` and `.png` names in lockstep.

## Diagrams

| # | What it shows | Source in the tree | Files | Embedded in |
| --- | --- | --- | --- | --- |
| 01 | Overall architecture: agents export OTLP to `sdf serve`, which redacts content by default and writes local SQLite; the UI and `sdf summary` / `sdf decision` read that store. Optional `sdf task declare`, `sdf verify`, the ledger, and the GitHub Action sit beside collection, not in its path. The Action invokes verify and does not open a ledger. | `README.md` (How it works); `internal/cli/cli.go`; `internal/server`; `.github/actions/sdf-verify` | [01-architecture.tldr](01-architecture.tldr), [01-architecture.png](01-architecture.png) | [README.md](../../README.md) |
| 02 | OTLP receiver and storage: HTTP `:4318` and gRPC `:4317`, guards, `record.Redact` before insert, local SQLite, and the read-only UI. | `docs/receiver.md`; `internal/server`; `internal/store/store.go`; `internal/record/redact.go`; `internal/ui/ui.go` | [02-receiver.tldr](02-receiver.tldr), [02-receiver.png](02-receiver.png) | [receiver.md](../receiver.md) |
| 03 | Stored decision relations and allowed endpoint kinds (`supersedes`, `blocks`, `conflicts_with`, `refines`, `implements`, `validates`, `contradicts`, `constrains`). | `docs/outcomes.md` (Link rules); `internal/decision/decision.go` (`Rel*` constants, `checkKinds`) | [03-decision-relations.tldr](03-decision-relations.tldr), [03-decision-relations.png](03-decision-relations.png) | [outcomes.md](../outcomes.md) |
| 04 | `sdf decision` subcommands in help order, including `pre-push`, and the hooks that run brief or check. | `internal/cli/cli.go` (`decisionCommands`); `docs/outcomes.md`; `docs/agents.md` (Decisions at session start); `skills/sdf-decisions/SKILL.md` | [04-decision-commands.tldr](04-decision-commands.tldr), [04-decision-commands.png](04-decision-commands.png) | this index |
| 05 | Anatomy of one Decisions-tab chapter: start, spine, left and right lanes, node roles, and derived edges. | `docs/outcomes.md` (The decision graph); `internal/decisiongraph/decisiongraph.go` | [05-decision-graph.tldr](05-decision-graph.tldr), [05-decision-graph.png](05-decision-graph.png) | [outcomes.md](../outcomes.md) |
| 06 | `sdf verify` pipeline: `loadTask`, `prepareCheckout`, `checkRevision`, `checkIntegrity`, `scanSecrets`, `prepareChecks`, `runCriterion`, then `finish`. | `docs/verify.md` (What runs, in order); `internal/verify/verify.go` (`run.steps`, `loadTask`, `ledgerPinned`, `finish`) | [06-verify-pipeline.tldr](06-verify-pipeline.tldr), [06-verify-pipeline.png](06-verify-pipeline.png) | [verify.md](../verify.md) |
| 07 | The `sdf-verify` GitHub Action: build the verifier, prepare the runner, check out the pull request head, run `sdf verify` against the protected base task (`--task-ref`, `--no-data-dir`), upload the verdict, and fail the job unless PASS. It does not open a ledger. | `docs/action.md`; `.github/actions/sdf-verify/action.yml` | [07-github-action-verify.tldr](07-github-action-verify.tldr), [07-github-action-verify.png](07-github-action-verify.png) | [action.md](../action.md) |
| 08 | Optional hash-chained ledger: genesis, task, evidence, and verdict entries, plus the difference between `sdf audit verify --expect-prefix` (`Store.VerifyPrefix`, later appends allowed) and `sdf verify`'s exact-head pin (a ledger grown past the pin is `TASK_UNTRUSTED`). | `docs/ledger.md`; `internal/ledger/ledger.go`, `pin.go`; `internal/verify/verify.go` (`ledgerPinned`); `internal/task/ledger.go` | [08-ledger-chain.tldr](08-ledger-chain.tldr), [08-ledger-chain.png](08-ledger-chain.png) | [ledger.md](../ledger.md) |
| 09 | Repository workflows: PR CI (including `release toolchain (go.mod)`), Main CI, Release on `v[0-9]+.[0-9]+.[0-9]+*`, and the Monday scheduled scan. | `CONTRIBUTING.md`; `AGENTS.md`; `.github/workflows/{pr,main,release,schedule}.yml` | [09-ci-workflows.tldr](09-ci-workflows.tldr), [09-ci-workflows.png](09-ci-workflows.png) | [CONTRIBUTING.md](../../CONTRIBUTING.md) |
| 10 | Git hooks installed by `sdf init --git-hooks`: post-commit, post-rewrite, prepare-commit-msg, and pre-push (decision check). | `docs/receiver.md` (Git hooks, Pre-push decision check); `internal/setup/githook.go`, `githook_script.go` | [10-git-hooks.tldr](10-git-hooks.tldr), [10-git-hooks.png](10-git-hooks.png) | this index |
| 11 | `sdf task declare` writes a canonical `sdf.task/v1` hash to `.sdf/tasks`, the data dir, and an optional ledger; `sdf verify` reads the task from a protected `--task-ref`. | `docs/tasks.md`; `internal/task/declare.go`, `jcs.go`; `internal/cli/task.go` | [11-task-declare.tldr](11-task-declare.tldr), [11-task-declare.png](11-task-declare.png) | [tasks.md](../tasks.md) |
| 12 | Claude Code default beta span tree: `claude_code.interaction` with `llm_request` and `tool` children. Hook is not in this tree. | `docs/agents.md` (Claude Code); default keep-list in `docs/summary.md` | [12-claude-code-spans.tldr](12-claude-code-spans.tldr), [12-claude-code-spans.png](12-claude-code-spans.png) | [agents.md](../agents.md) |
| 13 | Grok CLI internal spans that `sdf summary` keeps by default. | `docs/agents.md` (Grok CLI); `docs/summary.md`; `internal/summary` | [13-grok-spans.tldr](13-grok-spans.tldr), [13-grok-spans.png](13-grok-spans.png) | [agents.md](../agents.md) |
| 14 | Qwen Code span types. `qwen-code.tool` has `blocked_on_user` and `execution` children. `subagent` and `hook` are listed as span types; the docs do not specify their parent, so they are drawn as siblings. | `docs/agents.md` (Qwen Code) | [14-qwen-spans.tldr](14-qwen-spans.tldr), [14-qwen-spans.png](14-qwen-spans.png) | [agents.md](../agents.md) |
| 15 | `sdf summary` reads the local store, derives duration and tokens by agent-specific precedence, prices them from the OpenRouter catalog, and can attach GitHub outcomes, cost per accepted, and a local budget. | `docs/summary.md`; `docs/outcomes.md` (`--github`, `--budget`); `internal/summary/summary.go`; `internal/pricing/price.go` | [15-summary-cost.tldr](15-summary-cost.tldr), [15-summary-cost.png](15-summary-cost.png) | [summary.md](../summary.md) |
| 16 | How a session is linked to a commit, a pull request, a decision, and cost per accepted outcome. | `docs/outcomes.md` (Linking a session to a commit, Pull requests and CI); git hook events | [16-session-outcomes.tldr](16-session-outcomes.tldr), [16-session-outcomes.png](16-session-outcomes.png) | [outcomes.md](../outcomes.md) |

## Notes

- **Qwen Code parentage (diagram 14).** `docs/agents.md` states that
  `qwen-code.tool` has `blocked_on_user` and `execution` children.
  `qwen-code.subagent` and `qwen-code.hook` are listed as span types, but
  the docs do not say they are children of `qwen-code.interaction`. They
  are drawn as siblings, with that caveat on the diagram.
- Diagrams 04 and 10 live in this directory so the command map and the
  hook set can be opened in tldraw; they are not inlined in a markdown
  section of their own.

Topics covered only in prose, and not given a separate drawing, include
`sdf check`, security file modes, known gaps, the verify / action threat
models, full per-agent token-precedence lists, and span catalogs for
Codex, OpenCode, Pi, Gemini, and Copilot (only Claude, Grok, and Qwen
had a clear tree in the docs).
