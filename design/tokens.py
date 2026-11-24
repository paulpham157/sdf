#!/usr/bin/env python3
"""Generate design/tokens.css from design/tokens.json and check contrast.

Python 3 standard library only. Not part of the Go build.

  python3 design/tokens.py            write design/tokens.css
  python3 design/tokens.py --check    fail if tokens.css is stale or a pairing fails
  python3 design/tokens.py --table    print the contrast table (Markdown)
"""
import json
import pathlib
import re
import sys

HERE = pathlib.Path(__file__).resolve().parent
SRC, OUT = HERE / "tokens.json", HERE / "tokens.css"
REF = re.compile(r"^\{([^}]+)\}$")


def flatten(node, path, layer, out):
    if "$value" in node:
        out[".".join(path)] = (layer, node)
        return
    for key, child in node.items():
        if not key.startswith("$"):
            flatten(child, path + [key], layer, out)


def load():
    doc = json.loads(SRC.read_text(encoding="utf-8"))
    tokens = {}
    for layer in ("primitive", "semantic"):
        flatten(doc[layer], [], layer, tokens)
    return tokens


def css_name(path):
    return "--sdf-" + path.replace(".", "-")


def resolve(tokens, path, seen=()):
    value = tokens[path][1]["$value"]
    m = REF.match(str(value))
    if not m:
        return value
    if m.group(1) in seen:
        raise SystemExit("token cycle at " + path)
    return resolve(tokens, m.group(1), seen + (path,))


def render(tokens):
    lines = [
        "/* SDF design tokens. Generated from design/tokens.json by design/tokens.py.",
        "   Do not edit by hand. Spec: docs/design/tokens.md.",
        "   Self-contained on purpose: no imports, no external references. The UI keeps",
        "   a byte-identical copy at internal/ui/assets/tokens.css. */",
        ":root {",
    ]
    for layer in ("primitive", "semantic"):
        lines.append("  /* " + layer + " */")
        for path, (lyr, node) in tokens.items():
            if lyr != layer:
                continue
            m = REF.match(str(node["$value"]))
            value = "var(" + css_name(m.group(1)) + ")" if m else node["$value"]
            lines.append("  " + css_name(path) + ": " + str(value) + ";")
    lines.append("}")
    text = "\n".join(lines) + "\n"
    if re.search(r"https?:|@import|url\(", text):
        raise SystemExit("tokens.css must not contain a URL, @import or url()")
    return text


def lum(hex_):
    h = hex_.lstrip("#")
    out = []
    for i in (0, 2, 4):
        c = int(h[i:i + 2], 16) / 255
        out.append(c / 12.92 if c <= 0.04045 else ((c + 0.055) / 1.055) ** 2.4)
    return 0.2126 * out[0] + 0.7152 * out[1] + 0.0722 * out[2]


def ratio(a, b):
    la, lb = lum(a), lum(b)
    return (max(la, lb) + 0.05) / (min(la, lb) + 0.05)


SURFACES = ["color.bg", "color.surface-raised", "color.surface-sunken"]
TEXT, UI, DECOR = 4.5, 3.0, 0.0
# (foreground, backgrounds, minimum). bg and surface share a value, so bg stands for both.
PAIRS = [(f, SURFACES, TEXT) for f in (
    "color.text", "color.text-muted", "color.accent-text", "color.success",
    "color.warning", "color.error", "color.info", "graph.refinement", "chart.text")]
PAIRS += [
    ("color.text-inverse", ["color.surface-inverse", "graph.chosen-fill", "color.accent-text"], TEXT),
    ("color.text-inverse-muted", ["color.surface-inverse"], TEXT),
    ("color.accent-on-inverse", ["color.surface-inverse"], TEXT),
    ("color.accent-text", ["color.accent-soft"], TEXT),
    ("color.text", ["color.accent-soft"], TEXT),
    ("color.success", ["color.success-soft"], TEXT),
    ("color.warning", ["color.warning-soft"], TEXT),
    ("color.error", ["color.error-soft"], TEXT),
    ("color.info", ["color.info-soft"], TEXT),
    ("graph.refinement", ["graph.refinement-soft"], TEXT),
    ("graph.node-text", ["graph.node-fill", "graph.hatch-bg", "graph.ending-soft"], TEXT),
    ("graph.muted-text", ["graph.muted-fill"], TEXT),
]
PAIRS += [(f, SURFACES, UI) for f in (
    "color.accent", "color.focus", "color.border-strong", "graph.chosen", "graph.not-taken",
    "graph.future", "graph.edge", "graph.conflict", "graph.unknown", "graph.ending",
    "graph.track", "chart.series-1", "chart.series-2", "chart.series-3", "chart.series-4",
    "chart.axis")]
PAIRS += [
    ("graph.muted-stroke", ["graph.muted-fill"], UI),
    ("color.focus-on-inverse", ["color.surface-inverse"], UI),
    ("color.text-inverse", ["color.accent"], DECOR),
    ("color.accent", ["color.surface-inverse"], DECOR),
    ("graph.chosen", ["graph.chosen-fill"], DECOR),
    ("chart.series-2", ["chart.series-1"], DECOR),
    ("color.border", SURFACES, DECOR),
    ("chart.grid", SURFACES, DECOR),
]


def check_contrast(tokens):
    rows, failed = [], False
    for fg, bgs, need in PAIRS:
        for bg in bgs:
            r = ratio(resolve(tokens, fg), resolve(tokens, bg))
            ok = r + 1e-9 >= need
            failed |= not ok
            kind = {TEXT: "text 4.5", UI: "UI 3.0"}.get(need, "info only")
            rows.append((fg, resolve(tokens, fg), bg, resolve(tokens, bg), r, kind,
                         ("pass" if ok else "FAIL") if need else "n/a"))
    return rows, failed


def main(argv):
    tokens = load()
    text = render(tokens)
    rows, failed = check_contrast(tokens)
    if "--table" in argv:
        print("| Foreground | Hex | Background | Hex | Ratio | Needs | Result |")
        print("| --- | --- | --- | --- | ---: | --- | --- |")
        for fg, fh, bg, bh, r, kind, res in rows:
            print(f"| `{css_name(fg)}` | `{fh}` | `{css_name(bg)}` | `{bh}` | {r:.2f}:1 | {kind} | {res} |")
        return 1 if failed else 0
    if "--check" in argv:
        stale = not OUT.exists() or OUT.read_text(encoding="utf-8") != text
        if stale:
            print("design/tokens.css is stale; run python3 design/tokens.py", file=sys.stderr)
        if failed:
            print("a contrast pairing fails; run with --table", file=sys.stderr)
        return 1 if stale or failed else 0
    OUT.write_text(text, encoding="utf-8")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
