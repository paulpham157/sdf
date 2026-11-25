#!/usr/bin/env python3
"""Generate design/tokens.css from design/tokens.json and check contrast.

Python 3 standard library only. Not part of the Go build.

  python3 design/tokens.py            write design/tokens.css
  python3 design/tokens.py --check    fail if tokens.css is stale, a pairing fails,
                                      or docs/stylesheets/extra.css has a raw color
  python3 design/tokens.py --table    print the light and dark contrast tables
"""
import json
import pathlib
import re
import sys

HERE = pathlib.Path(__file__).resolve().parent
SRC, OUT = HERE / "tokens.json", HERE / "tokens.css"
EXTRA = HERE.parent / "docs" / "stylesheets" / "extra.css"
REF = re.compile(r"^\{([^}]+)\}$")
HEX = re.compile(r"^#[0-9A-Fa-f]{6}$")
# extra.css may only name tokens. A raw color would drift from tokens.json.
RAW_COLOR = re.compile(
    r"#[0-9A-Fa-f]{3,8}\b|(?<![\w-])rgba?\(|(?<![\w-])hsla?\(",
    re.IGNORECASE,
)
# Opt-in. A prefers-color-scheme block would flip the UI and the landing page,
# which load this file and do not set either attribute.
DARK_SELECTOR = '[data-sdf-theme="dark"], [data-md-color-scheme="slate"]'


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
    for path, (layer, node) in tokens.items():
        if "$dark" not in node:
            continue
        if layer != "semantic" or node.get("$type") != "color":
            raise SystemExit("$dark is only valid on a semantic color: " + path)
        dark = node["$dark"]
        if not isinstance(dark, str) or not (REF.match(dark) or HEX.match(dark)):
            raise SystemExit("$dark must be a #RRGGBB or {alias}: " + path)
    return tokens


def css_name(path):
    return "--sdf-" + path.replace(".", "-")


def resolve_mode(tokens, path, dark, seen=()):
    node = tokens[path][1]
    if dark and "$dark" in node:
        value = node["$dark"]
    else:
        value = node["$value"]
    m = REF.match(str(value))
    if not m:
        return value
    if m.group(1) in seen:
        raise SystemExit("token cycle at " + path)
    return resolve_mode(tokens, m.group(1), dark, seen + (path,))


def resolve(tokens, path, seen=()):
    return resolve_mode(tokens, path, False, seen)


def css_value(raw):
    m = REF.match(str(raw))
    return "var(" + css_name(m.group(1)) + ")" if m else str(raw)


def render(tokens):
    lines = [
        "/* SDF design tokens. Generated from design/tokens.json by design/tokens.py.",
        "   Do not edit by hand. Spec: docs/design/tokens.md.",
        "   Self-contained on purpose: no imports, no external references. The UI keeps",
        "   a byte-identical copy at internal/ui/assets/tokens.css.",
        "   Dark semantic colors are opt-in under the selector below. There is no",
        "   prefers-color-scheme block, so the UI and the landing page stay light. */",
        ":root {",
    ]
    for layer in ("primitive", "semantic"):
        lines.append("  /* " + layer + " */")
        for path, (lyr, node) in tokens.items():
            if lyr != layer:
                continue
            lines.append("  " + css_name(path) + ": " + css_value(node["$value"]) + ";")
    lines.append("}")
    # A var() inside a custom property is computed where that property is
    # declared. An alias left only on :root would keep the light color, so
    # every semantic color whose dark hex differs is declared again here.
    dark = []
    for path, (lyr, node) in tokens.items():
        if lyr != "semantic" or node.get("$type") != "color":
            continue
        light = str(resolve_mode(tokens, path, False))
        dark_hex = str(resolve_mode(tokens, path, True))
        if light.lower() == dark_hex.lower():
            continue
        if "$dark" in node:
            value = css_value(node["$dark"])
        else:
            value = css_value(node["$value"])
        dark.append((path, value))
    if dark:
        lines.append("")
        lines.append("/* Dark semantic colors. Applied only when a surface opts in. */")
        lines.append(DARK_SELECTOR + " {")
        for path, value in dark:
            lines.append("  " + css_name(path) + ": " + value + ";")
        lines.append("}")
    text = "\n".join(lines) + "\n"
    if re.search(r"https?:|@import|url\(", text):
        raise SystemExit("tokens.css must not contain a URL, @import or url()")
    if "@media" in text:
        raise SystemExit("tokens.css must not use @media; dark is opt-in by selector")
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
# Syntax colors are these tokens. Fenced code is darker than sunken in the dark
# scheme, so a pair that passes on the surfaces also passes on color.code-bg.
# Inline code uses color.code-inline-bg, which is surface-sunken in light and
# surface-raised in dark, both already in the pairs above.


def check_contrast(tokens, dark=False):
    rows, failed = [], False
    for fg, bgs, need in PAIRS:
        for bg in bgs:
            r = ratio(resolve_mode(tokens, fg, dark), resolve_mode(tokens, bg, dark))
            ok = r + 1e-9 >= need
            failed |= not ok
            kind = {TEXT: "text 4.5", UI: "UI 3.0"}.get(need, "info only")
            rows.append((fg, resolve_mode(tokens, fg, dark), bg, resolve_mode(tokens, bg, dark),
                         r, kind, ("pass" if ok else "FAIL") if need else "n/a"))
    return rows, failed


def check_extra_css():
    """Fail if the docs theme hard-codes a color instead of a token."""
    if not EXTRA.exists():
        print("missing " + str(EXTRA), file=sys.stderr)
        return True
    bad = []
    for i, line in enumerate(EXTRA.read_text(encoding="utf-8").splitlines(), 1):
        if RAW_COLOR.search(line):
            bad.append(f"docs/stylesheets/extra.css:{i}: {line.strip()}")
    if bad:
        print("extra.css must use tokens, not raw hex or rgb():", file=sys.stderr)
        for item in bad:
            print(item, file=sys.stderr)
    return bool(bad)


def print_table(rows):
    print("| Foreground | Hex | Background | Hex | Ratio | Needs | Result |")
    print("| --- | --- | --- | --- | ---: | --- | --- |")
    for fg, fh, bg, bh, r, kind, res in rows:
        print(f"| `{css_name(fg)}` | `{fh}` | `{css_name(bg)}` | `{bh}` | {r:.2f}:1 | {kind} | {res} |")


def main(argv):
    tokens = load()
    text = render(tokens)
    light_rows, light_failed = check_contrast(tokens, dark=False)
    dark_rows, dark_failed = check_contrast(tokens, dark=True)
    extra_failed = check_extra_css()
    failed = light_failed or dark_failed or extra_failed
    if "--table" in argv:
        print_table(light_rows)
        print()
        print_table(dark_rows)
        return 1 if failed else 0
    if "--check" in argv:
        stale = not OUT.exists() or OUT.read_text(encoding="utf-8") != text
        if stale:
            print("design/tokens.css is stale; run python3 design/tokens.py", file=sys.stderr)
        if light_failed:
            print("a light contrast pairing fails; run with --table", file=sys.stderr)
        if dark_failed:
            print("a dark contrast pairing fails; run with --table", file=sys.stderr)
        return 1 if stale or failed else 0
    OUT.write_text(text, encoding="utf-8")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
