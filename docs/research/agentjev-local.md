# Local AgentJev-0.6B Judge backend

Setup for the `local` Judge backend (issue #23). Weights and torch stay **outside**
this repo. The default pytest suite never imports torch and never needs a
checkpoint.

## What runs where

| Piece | Location |
| --- | --- |
| Wire translation (`noul`↔`boolean`, criteria↔options) | `sdf_core/judge_agentjev.py` |
| Fail-open HTTP client + 2,048-token state fit | `sdf_core/judge_local.py` |
| AgentJev server code | clone of [malevrigns/agent-jev](https://github.com/malevrigns/agent-jev) (Apache-2.0) |
| Checkpoint + temperatures | operator machine only (see below) |
| Backbone download (`Qwen/Qwen3-0.6B`) | Hugging Face cache on the operator machine |

SDF does not start the model process and does not embed weights. Point
`LocalAgentJevJudge(make_agentjev_post(...))` at a loopback server you already
started.

## Where weights go

Keep checkpoints out of the git worktree (they are large and must not be
committed). A typical layout:

```text
~/.cache/agentjev/
  agentjev_v1.pt          # torch wrap of the published safetensors
  temperatures.json
```

Hugging Face will also cache `Qwen/Qwen3-0.6B` under `~/.cache/huggingface/`
when the server loads the backbone.

Convert the published weights once (from the agent-jev README):

```bash
mkdir -p ~/.cache/agentjev
cd /path/to/agent-jev   # separate clone, not this repo
python -m venv .venv && source .venv/bin/activate
pip install -r requirements.txt huggingface_hub safetensors

python - <<'PY'
from pathlib import Path
from huggingface_hub import hf_hub_download
from safetensors.torch import load_file
import torch

out = Path.home() / ".cache" / "agentjev"
src = hf_hub_download("aimeigaoshou/agent-jev", "model.safetensors")
torch.save({"state_dict": load_file(src)}, out / "agentjev_v1.pt")
hf_hub_download("aimeigaoshou/agent-jev", "temperatures.json", local_dir=out)
print(out)
PY
```

## Start the local process

On Apple Silicon use MPS (the server's default device is CUDA):

```bash
cd /path/to/agent-jev
source .venv/bin/activate
python -m jev_service.server \
  --checkpoint ~/.cache/agentjev/agentjev_v1.pt \
  --model-path Qwen/Qwen3-0.6B \
  --temperatures ~/.cache/agentjev/temperatures.json \
  --device mps \
  --port 8149
```

The process binds `127.0.0.1` only. Check `GET http://127.0.0.1:8149/health`.

Optional env for SDF clients (also listed in `.env.example`):

```bash
# Loopback URL of the AgentJev process (default http://127.0.0.1:8149)
SDF_AGENTJEV_URL=http://127.0.0.1:8149
```

## Fail open and token budget

- Transport errors, timeouts, service error bodies and malformed replies return
  `LocalJudgeResult(available=False)` instead of raising into callers.
- AgentJev rejects input over 2,048 tokens (it never truncates). Call
  `fit_state_for_agentjev(state, questions=...)` (also applied inside
  `LocalAgentJevJudge.ask`) so the evaluate request stays under that budget.
  Estimates use a conservative character heuristic so the default suite needs
  no tokenizer.

Some AgentJev builds return an empty `model` field on `/api/evaluate`. The HTTP
client fills it from `/api/info` (or `AgentJev-0.6B`) so the shared wire parser
can still record a version.

## Opt-in live test

With the server running:

```bash
SDF_LIVE_AGENTJEV=1 uv run pytest -q tests/test_judge_local_live.py
```

Skipped unless `SDF_LIVE_AGENTJEV=1`. Uses one synthetic triage case from
`tests/fixtures/judge/` when present.
