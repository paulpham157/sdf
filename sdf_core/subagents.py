"""Sub-agents are off for every agent kind (ADR-0020).

A pure module: the host-controlled argv / config fragments that keep each
agent CLI from starting a child agent.  Every fragment is passed on the
agent's own command line (or written into SDF's user-level config), so a
project file in the Attempt workspace cannot turn sub-agents back on.  Each
mechanism was found in the installed CLI (versions pinned below) and checked
against a mock model endpoint that records the advertised tool list; see the
ADR for the probe results.

* ``claude`` 2.1.308 - ``--disallowedTools`` removes the ``Agent`` tool (the
  successor of ``Task``) and the multi-agent ``Workflow`` / agent-team tools.
  CLI deny rules beat every settings source, including a project ``allow``.
* ``codex`` 0.165.0 - ``features.multi_agent=false`` removes the v1
  collaboration tools.  Models whose catalog entry sets
  ``multi_agent_version = "v2"`` (Codex's OpenAI defaults) ignore the feature
  flags (upstream openai/codex#50880), so ``features.multi_agent_v2`` is also
  pinned to ``{enabled=false, max_concurrent_threads_per_session=1}``: the
  root thread uses the only slot and every ``spawn_agent`` fails with
  "agent thread limit reached".  ``-c`` overrides outrank a trusted
  project's ``.codex/config.toml``.
* ``opencode`` 2.2.0 - the tool is ``subagent`` (``task`` in 1.x); a ``deny``
  permission removes it from the tool list.  Written into SDF's user config
  (``credential_injection._OPENCODE_CONFIG``), appended last (last match wins).
* ``qwen`` 0.26.2 - ``--exclude-tools`` drops the ``agent`` tool and the
  team / messaging / workflow tools, including from the deferred
  ``tool_search`` catalogue that appears without ``--bare``.
* ``goose`` 1.56.0 - the ``delegate`` tool comes from the ``summon``
  platform extension, which only loads from a profile; ``--no-profile``
  (always passed) skips it even when ``config.yaml`` enables it.  The argv
  never adds ``--with-builtin summon``.
* ``pi`` 1.4.0 - Pi has no built-in sub-agent tool; ``--no-extensions`` plus
  the image-baked allowlist (ADR-0017, none spawns agents; pi-fabric stays
  rejected) keeps it that way.
"""

from __future__ import annotations

CLAUDE_DISALLOWED_TOOLS: tuple[str, ...] = (
    "Agent", "Task", "Workflow", "ListAgents", "SendMessage", "TeamCreate", "TeamDelete",
)
CLAUDE_ARGS: tuple[str, ...] = ("--disallowedTools", ",".join(CLAUDE_DISALLOWED_TOOLS))

CODEX_ARGS: tuple[str, ...] = (
    "-c", "features.multi_agent=false",
    "-c", "features.multi_agent_v2={enabled=false,max_concurrent_threads_per_session=1}",
)

# OpenCode permission actions denied in SDF's user config.
OPENCODE_DENIED_ACTIONS: tuple[str, ...] = ("subagent", "task")

QWEN_EXCLUDED_TOOLS: tuple[str, ...] = (
    "agent", "create_sub_session", "list_agents", "send_message", "team_create", "team_delete",
    "team_plan_approval", "request_shutdown", "workflow",
)
QWEN_ARGS: tuple[str, ...] = ("--exclude-tools", ",".join(QWEN_EXCLUDED_TOOLS))

# Goose builtins SDF may name with ``--with-builtin``; ``summon`` never.
GOOSE_ALLOWED_BUILTINS: tuple[str, ...] = ("developer",)


def start_args(agent: str) -> tuple[str, ...]:
    """Argv fragment that disables sub-agents for a Herdr-pane ``agent``."""

    return {"claude": CLAUDE_ARGS, "codex": CODEX_ARGS}.get(agent, ())
