# SDF Core

**Software Decision Fabric** links a business goal to engineering work and to independent Evidence that the work actually met the goal. An agent saying it is done is not success: a Task is accepted only when independent verification Evidence says so.

## Verify your repository

This is the user path. It copies your repository into a private snapshot, runs your already logged-in agent inside that snapshot, and accepts the work only when SDF's evaluator runs the project's own checks.

Prerequisites, all of them:

- Python 3.11+ and [uv](https://docs.astral.sh/uv/).
- Linux x86_64 with Landlock. On any other host the command stops before it starts an agent and does not record a pass. Pytest and Python checks are a non-pass off that platform ([ADR 0039](docs/adr/0039-evidence-independence.md)).
- Exactly one of these CLIs, already installed and already logged in on this computer:
  - Codex: `codex` on `PATH` and a regular file at `~/.codex/auth.json`
  - Claude Code: `claude` on `PATH` and a regular file at `~/.claude/.credentials.json`
- The repository contains `sdf-checks.json` or pytest files named `test_*.py` or `*_test.py`. Those checks are frozen before the agent runs. An agent claim is not a result.

SDF does not log you in, does not read those login files, and does not copy them. `opencode`, `pi`, `qwen`, and `goose` are not started by this command. If both Codex and Claude are logged in, pass `--agent codex` or `--agent claude`.

```bash
git clone https://github.com/paulpham157/sdf.git
cd sdf
uv sync
uv run python -m sdf_core.verify /absolute/path/to/your/repository
```

An optional instruction is the last argument and must not start with `-`:

```bash
uv run python -m sdf_core.verify /absolute/path/to/your/repository "Fix the failing tests. Do not edit the existing test files."
```

What the command does:

1. It copies the repository into `$XDG_DATA_HOME/sdf` (or `~/.local/share/sdf`). Symlinks are refused. `.git`, virtualenvs, agent config directories, and secret-shaped files such as `.env` and private keys are not copied. The agent can write only that snapshot. The command does not point `fixture_dir` at the repository.
2. It runs the selected CLI under Landlock. The CLI can read the system, which is how it opens its own login file, and it can write only the snapshot. If Landlock is unavailable the CLI is not started. There is no fake-agent fallback and no unsandboxed fallback. Network egress is not locked on this path.
3. It runs the frozen checks with the `pytest` from this environment (`uv run`), not a pytest installed inside your repository. Exit 0 means the Task status is `succeeded` because the evaluator (`sdf-evaluator`) recorded a pass for those commands, with the command and exit status stored as Evidence. The CLI printing that tests passed is not that result. A test that imports a package this environment does not have will fail.

The same directory holds `sdf.sqlite`. When `SDF_DATABASE_URL` is absent from the process environment, this command, `python -m sdf_core.auth_cli`, and the API share that file. A URL that appears only in the repo-root `.env` does not select the database. The directory is mode 0700. The database and the generated secret file are mode 0600. If `SDF_AUTH_PEPPER` or `SDF_EVIDENCE_MAC_KEY` is unset, local mode generates it into that file and does not print it. Setting either variable, or `SDF_DATABASE_URL`, overrides the default. A value shorter than 32 characters is rejected and is not replaced. A PostgreSQL URL does not turn authentication off and does not generate those secrets.

The same snapshot, instruction, checks, and agent replay the recorded Attempt, including a recorded failure. Change the repository or the instruction to run again.

### When it stops

| Situation | Result |
| --- | --- |
| Not Linux x86_64, or Landlock is missing | exit 2, no agent |
| No logged-in Codex or Claude CLI | exit 2, no agent |
| Both are logged in and `--agent` is omitted | exit 2 |
| No `sdf-checks.json` and no pytest files | exit 2, no agent |
| The tree contains a symlink | exit 2, nothing is copied |
| The CLI cannot run under Landlock, including when it needs to write its config directory | the Attempt fails; that is not a pass |
| The CLI exits 0 but the checks do not pass | the Task is not `succeeded` |
| `opencode`, `pi`, `qwen`, or `goose` | exit 2; they are not started |

A CLI that can only keep its session by writing `~/.codex` or `~/.claude` will fail here. Those directories are not made writable, and their files are not copied into the snapshot.

Maintainer setup, PostgreSQL, and the live E2B loop are in [docs/CONTRIBUTING.md](docs/CONTRIBUTING.md) and [docs/live-full-loop.md](docs/live-full-loop.md). They are not a second way to verify your repository.

## License

[Apache-2.0](LICENSE)
