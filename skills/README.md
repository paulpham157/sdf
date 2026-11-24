# Skills

Agent skills that ship with sdf. They are not loaded from this folder automatically; copy or symlink a skill into the location your coding agent reads.

## sdf-decisions

Instructs an agent to write important choices down with `sdf decision add` and `sdf decision link`. That includes decisions the user makes (an approval, a rejection, a choice between options, a constraint) and decisions the agent makes. Each record names who decided, what was decided, the alternatives, a reason only when one was given, and is linked to the session when the store already has it. At session start, the skill requires [`scripts/brief`](sdf-decisions/scripts/brief) (or `sdf decision brief`). Before a push, merge, release, deploy, or architecture change, the skill requires [`scripts/check`](sdf-decisions/scripts/check) (or `sdf decision check <action>`), and the agent stops to ask the user when that command exits non-zero.

The skill lives at [`sdf-decisions/SKILL.md`](sdf-decisions/SKILL.md), with helpers in [`sdf-decisions/scripts/`](sdf-decisions/scripts/). A `cp -R skills/sdf-decisions …` or symlink of the whole skill folder takes the scripts with it. The commands and fields it uses are those of the `sdf` CLI; see [docs/outcomes.md](../docs/outcomes.md).

### Claude Code

Confirmed from the [Claude Code skills docs](https://code.claude.com/docs/en/skills) (also [Create custom skills](https://claude.com/docs/skills/how-to)).

| Scope | Path |
| --- | --- |
| Personal (all projects on this machine) | `~/.claude/skills/sdf-decisions/SKILL.md` |
| Project (this repository) | `.claude/skills/sdf-decisions/SKILL.md` |

```bash
# Personal
mkdir -p ~/.claude/skills
cp -R skills/sdf-decisions ~/.claude/skills/

# Project (run from the repository root)
mkdir -p .claude/skills
cp -R skills/sdf-decisions .claude/skills/
```

Claude Code also follows a symlinked skill folder.

### Codex

Confirmed from the current [Codex / ChatGPT skills docs](https://developers.openai.com/codex/skills/) (`Where Codex loads local skills`). The documented personal location is `$HOME/.agents/skills`; the project location is `.agents/skills` at the launch directory, a parent of it, or the repository root.

| Scope | Path |
| --- | --- |
| Personal | `~/.agents/skills/sdf-decisions/SKILL.md` |
| Project | `.agents/skills/sdf-decisions/SKILL.md` |

```bash
# Personal
mkdir -p ~/.agents/skills
cp -R skills/sdf-decisions ~/.agents/skills/

# Project (run from the repository root)
mkdir -p .agents/skills
cp -R skills/sdf-decisions .agents/skills/
```

`~/.codex/skills` is the legacy personal folder. Codex still loads it for backward compatibility ([openai/codex#10437](https://github.com/openai/codex/pull/10437), [issue #14337](https://github.com/openai/codex/issues/14337)), but current docs recommend `~/.agents/skills` and do not list the old path. Prefer the new folder for new installs. Codex follows symlinked skill folders.

### Qwen Code

Confirmed from the [Qwen Code Agent Skills docs](https://qwenlm.github.io/qwen-code-docs/en/users/features/skills/). Qwen Code does load `SKILL.md` skills. It also reads the shared `.agents/skills` and `~/.agents/skills` folders; when the same name exists in both, the Qwen-specific folder wins.

| Scope | Path |
| --- | --- |
| Personal | `~/.qwen/skills/sdf-decisions/SKILL.md` (also `~/.agents/skills/`) |
| Project | `.qwen/skills/sdf-decisions/SKILL.md` (also `.agents/skills/`) |

```bash
# Personal
mkdir -p ~/.qwen/skills
cp -R skills/sdf-decisions ~/.qwen/skills/

# Project (run from the repository root)
mkdir -p .qwen/skills
cp -R skills/sdf-decisions .qwen/skills/
```

You can also paste the same instructions into `QWEN.md` or `AGENTS.md` if you want them loaded as standing context instead of as a skill.

### After installing

`sdf` must be on `PATH`, and the agent should run the commands against the same store `sdf serve` is using (default `sdf-telemetry.sqlite` in the current directory, or `--db PATH`). The SessionStart hook installed by `sdf init --write` stays an inline `sdf decision brief` command; it does not call these skill scripts, so init still works when the skill folder is not installed.
