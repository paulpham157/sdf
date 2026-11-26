# Diagrams

These drawings follow the Go and workflow code on `origin/main` at
`80ccd255ffc98cf53376773c55aadcf9ef81da7e`. Where a document used to
disagree with the code, the drawing shows the code.

Each entry is a tldraw document (`.tldr`) and a PNG of the same boxes
and labels. The official tldraw file extension is `.tldr`, not
`.tldraw`.

The `.tldr` sources and the PNG renders stay in `docs/diagrams/`. The
user guides embed those images. This file is the authoring index for
contributors.

The checked-in PNG is a 2× render, 1152 pixels wide, meant to sit in
the docs column at about 576 CSS pixels. The background is `#F6F1E8`.
Text is charcoal `#2F3640`. A rust stroke `#C65A2E` marks one
highlighted step. Secondary ink is `#4E5864`, which stays at or above
4.5:1 on that cream. There is no second title inside the picture; the
page heading is the title.

## Open and edit

1. Open [tldraw](https://www.tldraw.com).
2. Use **File → Open** and choose the `.tldr` file (or drag it onto the
   canvas).
3. Edit as needed, then **File → Save** to write a new `.tldr`.
4. Re-export a PNG at 2×, about 1152 pixels wide, on `#F6F1E8`, with
   the charcoal and rust colors above. tldraw.com’s own export uses its
   default blue, purple, green, and orange, so do not check that file
   in unchanged. Keep each flag (`--task-ref`, `--task-hash`,
   `--no-data-dir`, and the rest) on its own line.

The [tldraw VS Code extension](https://marketplace.visualstudio.com/items?itemName=tldraw-org.tldraw-vscode)
also opens `.tldr` files.

`docs/diagrams/` does not include a render toolchain. Keep the `.tldr`
and `.png` names in lockstep.

## Diagrams

### 01 Architecture

Agents export OTLP to `sdf serve`, which redacts content by default and
writes local SQLite. The UI and `sdf summary` / `sdf decision` read
that store. `sdf task declare`, `sdf verify`, the ledger, and the
GitHub Action sit beside collection. The Action invokes verify and does
not open a ledger.

- Source: `README.md` and `docs/index.md` (How it works); `internal/cli/cli.go`; `internal/server`; `.github/actions/sdf-verify`
- Files: [01-architecture.tldr](../docs/diagrams/01-architecture.tldr), [01-architecture.png](../docs/diagrams/01-architecture.png)
- Embedded in: [README](../README.md) and the [docs home](../docs/index.md)

### 02 Receiver

OTLP on HTTP `:4318` and gRPC `:4317`, guards, `record.Redact` before
insert, local SQLite, and the read-only UI.

- Source: `docs/receiver.md`; `internal/server`; `internal/store/store.go`; `internal/record/redact.go`; `internal/ui/ui.go`
- Files: [02-receiver.tldr](../docs/diagrams/02-receiver.tldr), [02-receiver.png](../docs/diagrams/02-receiver.png)
- Embedded in: [receiver.md](../docs/receiver.md)

### 03 Decision relations

Stored relations and the endpoint kinds they allow: `supersedes`,
`blocks`, `conflicts_with`, `refines`, `implements`, `validates`,
`contradicts`, and `constrains`.

- Source: `docs/outcomes.md` (Link rules); `internal/decision/decision.go` (`Rel*` constants, `checkKinds`)
- Files: [03-decision-relations.tldr](../docs/diagrams/03-decision-relations.tldr), [03-decision-relations.png](../docs/diagrams/03-decision-relations.png)
- Embedded in: [outcomes.md](../docs/outcomes.md)

### 04 Decision commands

`sdf decision` subcommands in help order, including `pre-push`, and the
hooks that run brief or check.

- Source: `internal/cli/cli.go` (`decisionCommands`); `docs/outcomes.md`; `docs/agents.md`; `skills/sdf-decisions/SKILL.md`
- Files: [04-decision-commands.tldr](../docs/diagrams/04-decision-commands.tldr), [04-decision-commands.png](../docs/diagrams/04-decision-commands.png)
- Embedded in: [outcomes.md](../docs/outcomes.md)

### 05 Decision graph

One Decisions-tab chapter: spine, left lane, right lane, roles, and
derived edges.

- Source: `docs/outcomes.md` (The decision graph); `internal/decisiongraph/decisiongraph.go`
- Files: [05-decision-graph.tldr](../docs/diagrams/05-decision-graph.tldr), [05-decision-graph.png](../docs/diagrams/05-decision-graph.png)
- Embedded in: [outcomes.md](../docs/outcomes.md)

### 06 Verify pipeline

`loadTask`, `prepareCheckout`, `checkRevision`, `checkIntegrity`,
`scanSecrets`, `prepareChecks`, `runCriterion`, then `finish`.

- Source: `docs/verify.md` (What runs, in order); `internal/verify/verify.go` (`run.steps`, `loadTask`, `ledgerPinned`, `finish`)
- Files: [06-verify-pipeline.tldr](../docs/diagrams/06-verify-pipeline.tldr), [06-verify-pipeline.png](../docs/diagrams/06-verify-pipeline.png)
- Embedded in: [verify.md](../docs/verify.md)

### 07 GitHub Action

The `sdf-verify` Action builds the verifier, prepares the runner,
checks out the pull request head, and runs `sdf verify` against the
protected base task (`--task-ref`, `--no-data-dir`). It does not open a
ledger.

- Source: `docs/action.md`; `.github/actions/sdf-verify/action.yml`
- Files: [07-github-action-verify.tldr](../docs/diagrams/07-github-action-verify.tldr), [07-github-action-verify.png](../docs/diagrams/07-github-action-verify.png)
- Embedded in: [action.md](../docs/action.md)

### 08 Ledger chain

The optional hash-chained ledger, and the difference between
`sdf audit verify --expect-prefix` (`Store.VerifyPrefix`, later appends
allowed) and `sdf verify`'s exact-head pin (a ledger grown past the pin
is `TASK_UNTRUSTED`).

- Source: `docs/ledger.md`; `internal/ledger/ledger.go`, `pin.go`; `internal/verify/verify.go` (`ledgerPinned`); `internal/task/ledger.go`
- Files: [08-ledger-chain.tldr](../docs/diagrams/08-ledger-chain.tldr), [08-ledger-chain.png](../docs/diagrams/08-ledger-chain.png)
- Embedded in: [ledger.md](../docs/ledger.md)

### 09 Repository workflows

PR CI (including `release toolchain (go.mod)`), Main CI, Docs
(`.github/workflows/docs.yml`), Release on
`v[0-9]+.[0-9]+.[0-9]+*`, and the Monday scheduled scan.

- Source: `CONTRIBUTING.md`; `AGENTS.md`; `.github/workflows/pr.yml`; `.github/workflows/main.yml`; `.github/workflows/docs.yml`; `.github/workflows/release.yml`; `.github/workflows/schedule.yml`
- Files: [09-ci-workflows.tldr](../docs/diagrams/09-ci-workflows.tldr), [09-ci-workflows.png](../docs/diagrams/09-ci-workflows.png)
- Embedded in: [CONTRIBUTING.md](../CONTRIBUTING.md)

### 10 Git hooks

`post-commit`, `post-rewrite`, `prepare-commit-msg`, and `pre-push`
from `sdf init --git-hooks`.

- Source: `docs/receiver.md` (Git hooks, Pre-push decision check); `internal/setup/githook.go`, `githook_script.go`
- Files: [10-git-hooks.tldr](../docs/diagrams/10-git-hooks.tldr), [10-git-hooks.png](../docs/diagrams/10-git-hooks.png)
- Embedded in: [receiver.md](../docs/receiver.md)

### 11 Task declare

`sdf task declare` writes a canonical `sdf.task/v1` hash to
`.sdf/tasks`, the data dir, and an optional ledger. `sdf verify` reads
the task from a protected `--task-ref`.

- Source: `docs/tasks.md`; `internal/task/declare.go`, `jcs.go`; `internal/cli/task.go`
- Files: [11-task-declare.tldr](../docs/diagrams/11-task-declare.tldr), [11-task-declare.png](../docs/diagrams/11-task-declare.png)
- Embedded in: [tasks.md](../docs/tasks.md)

### 12 Claude Code spans

The default beta span tree: `claude_code.interaction` with
`llm_request` and `tool` children. Hook is not in this tree.

- Source: `docs/agents.md` (Claude Code); the default keep-list in `docs/summary.md`
- Files: [12-claude-code-spans.tldr](../docs/diagrams/12-claude-code-spans.tldr), [12-claude-code-spans.png](../docs/diagrams/12-claude-code-spans.png)
- Embedded in: [agents.md](../docs/agents.md)

### 13 Grok spans

Grok CLI internal spans that `sdf summary` keeps by default.

- Source: `docs/agents.md` (Grok CLI); `docs/summary.md`; `internal/summary`
- Files: [13-grok-spans.tldr](../docs/diagrams/13-grok-spans.tldr), [13-grok-spans.png](../docs/diagrams/13-grok-spans.png)
- Embedded in: [agents.md](../docs/agents.md)

### 14 Qwen spans

`qwen-code.tool` has `blocked_on_user` and `execution` children.
`subagent` and `hook` are span types whose parent is not specified, so
they are drawn as siblings.

- Source: `docs/agents.md` (Qwen Code)
- Files: [14-qwen-spans.tldr](../docs/diagrams/14-qwen-spans.tldr), [14-qwen-spans.png](../docs/diagrams/14-qwen-spans.png)
- Embedded in: [agents.md](../docs/agents.md)

### 15 Summary and cost

`sdf summary` reads the local store, derives duration and tokens by
agent-specific precedence, prices them from the OpenRouter catalog, and
can attach GitHub outcomes, cost per accepted, and a local budget.

- Source: `docs/summary.md`; `docs/outcomes.md` (`--github`, `--budget`); `internal/summary/summary.go`; `internal/pricing/price.go`
- Files: [15-summary-cost.tldr](../docs/diagrams/15-summary-cost.tldr), [15-summary-cost.png](../docs/diagrams/15-summary-cost.png)
- Embedded in: [summary.md](../docs/summary.md)

### 16 Session outcomes

How a session is linked to a commit, a pull request, a decision, and
cost per accepted outcome.

- Source: `docs/outcomes.md` (Linking a session to a commit); git hook events
- Files: [16-session-outcomes.tldr](../docs/diagrams/16-session-outcomes.tldr), [16-session-outcomes.png](../docs/diagrams/16-session-outcomes.png)
- Embedded in: [outcomes.md](../docs/outcomes.md)

## Notes

- **Qwen Code parentage (diagram 14).** `docs/agents.md` states that
  `qwen-code.tool` has `blocked_on_user` and `execution` children.
  `qwen-code.subagent` and `qwen-code.hook` are listed as span types, but
  the docs do not say they are children of `qwen-code.interaction`. They
  are drawn as siblings, with that caveat on the diagram.
- Every drawing is inlined on the page that describes that flow.
  Diagram 04 is on [outcomes.md](../docs/outcomes.md) and diagram 10 is
  on [receiver.md](../docs/receiver.md).

Topics covered only in prose, and not given a separate drawing, include
`sdf check`, security file modes, known gaps, the verify and Action
threat models, full per-agent token-precedence lists, and span catalogs
for Codex, OpenCode, Pi, Gemini, and Copilot. Only Claude, Grok, and
Qwen had a clear tree in the docs.
