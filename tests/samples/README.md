# Sample OTLP payloads

These JSON files are hand-written sample OpenTelemetry data. They are not
recordings of a real Codex or Claude Code session, and they contain no
prompts.

Every resource carries `sdf.fixture=sample`. The shapes follow the attribute
names documented for each tool (see the repository README) so ingest and
summary tests can run without either CLI.

`tests/test_live_shapes.py` adds a second sample, also marked
`sdf.fixture=sample`, whose field names follow a live OpenRouter capture
(Codex `service.name=codex_exec`, `exec_command` spans, and a metric-tag
rejection for a model id containing `:`). It is not that capture: the ids are
fake and there is no prompt text.
