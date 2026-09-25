# #16 live results (2026-09-25T14:54:40Z): local, uncommitted

Branch paulpham157/ac-12-turn-completion @ bdde610. Codex model pinned on the test side only: gpt-6-luna (pane shows GPT-6-Luna).

## Codex subscription (codex-personal), 5 consecutive runs on final HEAD
- run 1: PASS (1 passed in 30.82s)
  ```
  {"attempt": "ATTEMPT-0951e3e7262d", "task": "succeeded", "statuses": ["running", "running", "completed", "completed", "completed", "completed", "terminated"], "model": "gpt-6-luna", "credential": {"credential_mode": "subscription", "connection_id": "codex-personal"}, "evidence": [["add returns the sum", "PASS"]], "leaked": [], "events": [[1, "herdr-e2b", "runtime_started", "running"], [2, "herdr-e2b", "runtime_input_sent", "completed"], [3, "herdr-e2b", "runtime_output_observed", "completed"], [4, "herdr-e2b", "runtime_terminated", "terminated"]], "sandboxes": ["i1aa298bs3df6cdpgtwhp"]}
  ```
- run 2: PASS (1 passed in 32.53s)
  ```
  {"attempt": "ATTEMPT-d5fc085cda93", "task": "succeeded", "statuses": ["running", "running", "completed", "completed", "completed", "completed", "terminated"], "model": "gpt-6-luna", "credential": {"credential_mode": "subscription", "connection_id": "codex-personal"}, "evidence": [["add returns the sum", "PASS"]], "leaked": [], "events": [[1, "herdr-e2b", "runtime_started", "running"], [2, "herdr-e2b", "runtime_input_sent", "completed"], [3, "herdr-e2b", "runtime_output_observed", "completed"], [4, "herdr-e2b", "runtime_terminated", "terminated"]], "sandboxes": ["iul4ciwiqzj2i3mqdawfb"]}
  ```
- run 3: PASS (1 passed in 30.80s)
  ```
  {"attempt": "ATTEMPT-6cc96c957737", "task": "succeeded", "statuses": ["running", "running", "completed", "completed", "completed", "completed", "terminated"], "model": "gpt-6-luna", "credential": {"credential_mode": "subscription", "connection_id": "codex-personal"}, "evidence": [["add returns the sum", "PASS"]], "leaked": [], "events": [[1, "herdr-e2b", "runtime_started", "running"], [2, "herdr-e2b", "runtime_input_sent", "completed"], [3, "herdr-e2b", "runtime_output_observed", "completed"], [4, "herdr-e2b", "runtime_terminated", "terminated"]], "sandboxes": ["ihfip5j7ka5ceqx0xtpon"]}
  ```
- run 4: PASS (1 passed in 40.26s)
  ```
  {"attempt": "ATTEMPT-4123f0ecc1e4", "task": "succeeded", "statuses": ["running", "running", "completed", "completed", "completed", "completed", "terminated"], "model": "gpt-6-luna", "credential": {"credential_mode": "subscription", "connection_id": "codex-personal"}, "evidence": [["add returns the sum", "PASS"]], "leaked": [], "events": [[1, "herdr-e2b", "runtime_started", "running"], [2, "herdr-e2b", "runtime_input_sent", "completed"], [3, "herdr-e2b", "runtime_output_observed", "completed"], [4, "herdr-e2b", "runtime_terminated", "terminated"]], "sandboxes": ["i3xl53uaji0gluzv42col"]}
  ```
- run 5: PASS (1 passed in 30.56s)
  ```
  {"attempt": "ATTEMPT-e874caa7e1d6", "task": "succeeded", "statuses": ["running", "running", "completed", "completed", "completed", "completed", "terminated"], "model": "gpt-6-luna", "credential": {"credential_mode": "subscription", "connection_id": "codex-personal"}, "evidence": [["add returns the sum", "PASS"]], "leaked": [], "events": [[1, "herdr-e2b", "runtime_started", "running"], [2, "herdr-e2b", "runtime_input_sent", "completed"], [3, "herdr-e2b", "runtime_output_observed", "completed"], [4, "herdr-e2b", "runtime_terminated", "terminated"]], "sandboxes": ["ifhni18ddpbq478to7uhi"]}
  ```

## Claude api-key (dummy-key wiring + live Attempt)
- PASS (2 passed in 48.24s)
  ```
  {"attempt": "ATTEMPT-ba69a1115a46", "sandbox": ["ic7nksqqmx0737lauc508"], "credential": {"credential_mode": "api-key", "connection_id": null}, "task": "succeeded", "events": [[1, "herdr-e2b", "runtime_started", "running"], [2, "herdr-e2b", "runtime_input_sent", "completed"], [3, "herdr-e2b", "runtime_output_observed", "completed"], [4, "herdr-e2b", "runtime_terminated", "terminated"]], "evidence": [["add returns the sum", "PASS"]]}
  ```

## Cleanup / secrets
- `e2b sandbox list` after every run: No sandboxes found
- Codex: leaked=[] in all runs. Claude: scrollback and event payloads checked by boolean, no key or key tail

## Earlier failing runs (fixed before the final 5)
- 5/5 failed: Codex folder-trust dialog ignored the -c override, fixed by config.toml pre-trust (98f1846)
- 2/5 agent_not_idle history read mid-turn; 3/5 case-sensitive model check, fixed by ee2cb9d and 59b9068
- Claude via ExecutionService: capture_diff crashed on __pycache__ .pyc, fixed by bdde610
