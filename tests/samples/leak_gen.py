#!/usr/bin/env python3
"""Writes tests/samples/leak_{logs,traces,metrics}.json (run from repo root).

Port of Review's /tmp/r212 harness: every content-bearing value is tagged
SDF-LEAK-NN-<where>; values that must survive are plain.
"""
import json

T = 1800002000000000000
SID = "sample-leak-1"
n = [0]

def tag(where):
    n[0] += 1
    return "SDF-LEAK-%02d-%s" % (n[0], "".join(c if c.isalnum() else "_" for c in where))

def val(v):
    if isinstance(v, bool): return {"boolValue": v}
    if isinstance(v, int): return {"intValue": str(v)}
    if isinstance(v, dict): return {"kvlistValue": {"values": [kv(k, x) for k, x in v.items()]}}
    if isinstance(v, list): return {"arrayValue": {"values": [val(x) for x in v]}}
    return {"stringValue": v}

def kv(k, v): return {"key": k, "value": val(v)}

def res(service, extra=None):
    a = {"service.name": service, "sdf.fixture": "sample"}
    a.update(extra or {})
    return {"attributes": [kv(k, v) for k, v in a.items()]}

LOGKEYS = ["prompt", "request_text", "response_text", "tool_input", "tool_output", "tool_parameters",
           "full_command", "arguments", "result", "response", "completion", "messages", "content", "input", "output",
           "function_args", "gen_ai.prompt", "gen_ai.completion", "gen_ai.prompt.0.content",
           "gen_ai.input.messages.0.content", "llm.input_messages", "llm.output_messages.0.message.content",
           "input.value", "output.value", "gen_ai.tool.definitions", "gen_ai.input.messages_ref",
           "Gen_AI.Input.Messages", "GEN_AI.OUTPUT.MESSAGES", "gen_ai.input.messages", "gen_ai.output.messages",
           "gen_ai.system_instructions", "gen_ai.tool.call.arguments", "gen_ai.tool.call.result", "Prompt", "TOOL_INPUT"]

def log(service, event, attrs, body=None, res_extra=None, at=0):
    a = {"session.id": SID, "event.name": event}
    a.update(attrs)
    rec = {"timeUnixNano": str(T + at), "attributes": [kv(k, v) for k, v in a.items()]}
    rec["body"] = val(body if body is not None else event)
    return {"resource": res(service, res_extra), "scopeLogs": [{"scope": {"name": service}, "logRecords": [rec]}]}

L = []
for svc, ev in [("qwen-code", "qwen-code.api_request"), ("github-copilot", "gen_ai.client.inference.operation.details"),
                ("grok-cli", "grok_code.tool_result"), ("claude-code", "claude_code.user_prompt"), ("codex", "codex.tool_result")]:
    a = {k: tag(svc + "-log-" + k) for k in LOGKEYS}
    # Kept: numeric values under content-like keys, and metadata.
    a.update({"input_tokens": 1200, "prompt_length": 42, "tool_name": "run_shell_command", "model": "m-1"})
    L.append(log(svc, ev, a))
L.append(log("qwen-code", "qwen-code.api_request", {"gen_ai": {"input": {"messages": tag("nested-genai")}}, "meta": {"prompt": tag("nested-prompt"), "count": 3}}))
L.append(log("qwen-code", "qwen-code.api_request", {}, res_extra={"gen_ai.input.messages": tag("resource-genai"), "prompt": tag("resource-prompt")}))
L.append(log("github-copilot", "gen_ai.client.inference.operation.details", {}, body={"gen_ai.input.messages": tag("body-kvlist")}))
L.append(log("github-copilot", "gen_ai.client.inference.operation.details", {}, body=tag("body-string")))
L.append(log("gemini-cli", "gemini_cli.api_response", {}, body="API response " + tag("gemini-body-prose")))
L.append(log("grok-cli", "grok_code.tool_result", {"error_message": tag("grok-error_message")}))
L.append(log("qwen-code", "qwen-code.api_error", {"error": tag("qwen-api_error-error"), "error.message": tag("qwen-api_error-error.message"), "error.type": "rate_limit"}))
L.append(log("claude-code", "claude_code.api_error", {"error": tag("claude-api_error-error"), "status_code": "529"}))
# Claude Code git commit through its tool: the SHA must survive, the command must not.
L.append(log("claude-code", "claude_code.tool_result", {"tool_name": "Bash", "success": "true",
    "tool_parameters": json.dumps({"bash_command": "git commit -m '" + tag("claude-commit-message") + "'", "git_commit_id": "0123456789abcdef0123456789abcdef01234567"})}))

S = []
sp = [0]
def span(name, attrs, status=None, service="qwen-code"):
    sp[0] += 1
    s = {"traceId": "e" * 31 + "1", "spanId": "%016x" % sp[0], "name": name, "kind": 1,
         "startTimeUnixNano": str(T), "endTimeUnixNano": str(T + 10**9),
         "attributes": [kv(k, v) for k, v in dict({"session.id": SID}, **attrs).items()]}
    if status:
        s["status"] = {"code": 2, "message": status}
    S.append(s)
for name in ["qwen-code.llm_request", "chat gpt-5", "execute_tool bash", "invoke_agent"]:
    a = {k: tag("span-" + name + "-" + k) for k in LOGKEYS}
    a.update({"gen_ai.usage.input_tokens": 1200, "gen_ai.usage.output_tokens": 80, "gen_ai.tool.name": "bash", "gen_ai.request.model": "m-1"})
    span(name, a)
span("qwen-code.hook", {"error": tag("hook-error"), "error.message": tag("hook-error.message"), "Error": tag("hook-Error-case")}, status=tag("hook-status"))
for name in ["qwen-code.llm_request", "qwen-code.subagent", "qwen-code.interaction"]:
    span(name, {"error": tag(name + "-error"), "exception.message": tag(name + "-exception"), "error.type": "api_error"}, status=tag(name + "-status"))
span("Qwen-Code.Hook", {"error": tag("casehook-error")}, status=tag("casehook-status"))
traces = {"resourceSpans": [{"resource": res("qwen-code", {"gen_ai.system_instructions": tag("span-resource")}),
                             "scopeSpans": [{"scope": {"name": "x"}, "spans": S}]}]}

pts = [{"asInt": "1", "timeUnixNano": str(T), "startTimeUnixNano": str(T - 10**9),
        "attributes": [kv(k, tag("metric-" + k)) for k in ["gen_ai.input.messages", "prompt", "Gen_AI.Input.Messages", "gen_ai.tool.call.arguments"]] + [kv("type", "input")]}]
metrics = {"resourceMetrics": [{"resource": res("qwen-code"), "scopeMetrics": [{"scope": {"name": "qwen-code"}, "metrics": [
    {"name": "qwen-code.token.usage", "sum": {"aggregationTemporality": 2, "isMonotonic": True, "dataPoints": pts}}]}]}]}

for name, body in [("logs", {"resourceLogs": L}), ("traces", traces), ("metrics", metrics)]:
    with open("tests/samples/leak_%s.json" % name, "w") as f:
        json.dump(body, f, indent=1)
        f.write("\n")
print(n[0], "markers")
