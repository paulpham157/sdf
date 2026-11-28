#!/usr/bin/env python3
"""Generate design/tokens.css from design/tokens.json and check contrast.

Python 3 standard library only. Not part of the Go build.

  python3 design/tokens.py            write design/tokens.css
  python3 design/tokens.py --check    fail if tokens.css is stale, a pairing fails,
                                      docs/stylesheets/extra.css has a raw color, or
                                      focus-on-inverse is an outline without an inset offset
  python3 design/tokens.py --table    print the light and dark contrast tables
"""
import json
import pathlib
import re
import sys

HERE = pathlib.Path(__file__).resolve().parent
SRC, OUT = HERE / "tokens.json", HERE / "tokens.css"
EXTRA = HERE.parent / "docs" / "stylesheets" / "extra.css"
APP = HERE.parent / "internal" / "ui" / "assets" / "app.css"
# Stylesheets that may paint a focus ring. focus-on-inverse is legal only
# as an inset ring on an inverse surface; an outset ring is on the page.
FOCUS_STYLES = (EXTRA, APP)
FOCUS_ON_INVERSE = "var(--sdf-color-focus-on-inverse)"
REF = re.compile(r"^\{([^}]+)\}$")
HEX = re.compile(r"^#[0-9A-Fa-f]{6}$")
# extra.css may only name tokens. A raw color would drift from tokens.json.
# Comments are removed before the scan. color-mix is allowed when its
# arguments are tokens, transparent, currentColor, or inherit.
RAW_FUNC = re.compile(
    r"#[0-9A-Fa-f]{3,8}\b"
    r"|(?<![\w-])(?:rgba?|hsla?|hwb|lab|lch|oklab|oklch)\("
    r"|(?<![\w-])color\(",
    re.IGNORECASE,
)
NAMED_COLORS = {
    "aliceblue", "antiquewhite", "aqua", "aquamarine", "azure", "beige", "bisque",
    "black", "blanchedalmond", "blue", "blueviolet", "brown", "burlywood",
    "cadetblue", "chartreuse", "chocolate", "coral", "cornflowerblue", "cornsilk",
    "crimson", "cyan", "darkblue", "darkcyan", "darkgoldenrod", "darkgray",
    "darkgreen", "darkgrey", "darkkhaki", "darkmagenta", "darkolivegreen",
    "darkorange", "darkorchid", "darkred", "darksalmon", "darkseagreen",
    "darkslateblue", "darkslategray", "darkslategrey", "darkturquoise",
    "darkviolet", "deeppink", "deepskyblue", "dimgray", "dimgrey", "dodgerblue",
    "firebrick", "floralwhite", "forestgreen", "fuchsia", "gainsboro",
    "ghostwhite", "gold", "goldenrod", "gray", "green", "greenyellow", "grey",
    "honeydew", "hotpink", "indianred", "indigo", "ivory", "khaki", "lavender",
    "lavenderblush", "lawngreen", "lemonchiffon", "lightblue", "lightcoral",
    "lightcyan", "lightgoldenrodyellow", "lightgray", "lightgreen", "lightgrey",
    "lightpink", "lightsalmon", "lightseagreen", "lightskyblue", "lightslategray",
    "lightslategrey", "lightsteelblue", "lightyellow", "lime", "limegreen",
    "linen", "magenta", "maroon", "mediumaquamarine", "mediumblue",
    "mediumorchid", "mediumpurple", "mediumseagreen", "mediumslateblue",
    "mediumspringgreen", "mediumturquoise", "mediumvioletred", "midnightblue",
    "mintcream", "mistyrose", "moccasin", "navajowhite", "navy", "oldlace",
    "olive", "olivedrab", "orange", "orangered", "orchid", "palegoldenrod",
    "palegreen", "paleturquoise", "palevioletred", "papayawhip", "peachpuff",
    "peru", "pink", "plum", "powderblue", "purple", "rebeccapurple", "red",
    "rosybrown", "royalblue", "saddlebrown", "salmon", "sandybrown", "seagreen",
    "seashell", "sienna", "silver", "skyblue", "slateblue", "slategray",
    "slategrey", "snow", "springgreen", "steelblue", "tan", "teal", "thistle",
    "tomato", "turquoise", "violet", "wheat", "white", "whitesmoke", "yellow",
    "yellowgreen",
}
NAMED_COLOR = re.compile(
    r"(?<![\w-])(?:" + "|".join(sorted(NAMED_COLORS, key=len, reverse=True)) + r")(?![\w-])",
    re.IGNORECASE,
)
ALLOWED_KEYWORDS = {"transparent", "currentcolor", "inherit"}
# Strings the scanner must accept or reject. Kept next to the patterns so a
# change to the ban has to update both.
COLOR_SCAN_SAMPLES = (
    ("color: #abc;", True),
    ("color: #aabbcc;", True),
    ("color: rgb(1, 2, 3);", True),
    ("color: rgba(1, 2, 3, 0.5);", True),
    ("color: hsl(20 10% 10%);", True),
    ("color: hwb(20 10% 10%);", True),
    ("color: lab(50% 20 -10);", True),
    ("color: lch(50% 20 40);", True),
    ("color: oklab(0.5 0.1 -0.1);", True),
    ("color: oklch(0.5 0.1 40);", True),
    ("color: color(srgb 1 0 0);", True),
    ("color: red;", True),
    ("color: color-mix(in srgb, red 50%, blue);", True),
    ("color: transparent;", False),
    ("color: currentColor;", False),
    ("color: inherit;", False),
    ("color: var(--sdf-color-text);", False),
    ("white-space: nowrap;", False),
    ("color: color-mix(in srgb, var(--sdf-color-accent) 12%, transparent);", False),
    ("/* #fff and red */\ncolor: var(--sdf-color-bg);", False),
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
        kind = node.get("$type")
        if layer != "semantic" or kind not in ("color", "shadow"):
            raise SystemExit("$dark is only valid on a semantic color or shadow: " + path)
        dark = node["$dark"]
        if kind == "color":
            if not isinstance(dark, str) or not (REF.match(dark) or HEX.match(dark)):
                raise SystemExit("$dark must be a #RRGGBB or {alias}: " + path)
        elif not isinstance(dark, str) or not dark.strip():
            raise SystemExit("$dark shadow must be a CSS shadow: " + path)
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
        "   Do not edit by hand. Spec: design/tokens.md.",
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
        if lyr != "semantic":
            continue
        if node.get("$type") == "shadow":
            if "$dark" in node:
                dark.append((path, css_value(node["$dark"])))
            continue
        if node.get("$type") != "color":
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
        lines.append("/* Dark semantic colors and shadows. Applied only when a surface opts in. */")
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
    # Inset ring on the inverse surface only. On the page this color is
    # under 3:1 (light 2.72:1, dark 2.25:1). check_focus_on_inverse_scope
    # requires a negative outline-offset in the same rule, and checks the
    # page surfaces when a rule uses the token without that inset.
    ("color.focus-on-inverse", ["color.surface-inverse"], UI),
    ("color.text-inverse", ["color.accent"], DECOR),
    ("color.accent", ["color.surface-inverse"], DECOR),
    ("graph.chosen", ["graph.chosen-fill"], DECOR),
    ("chart.series-2", ["chart.series-1"], DECOR),
    ("color.border", SURFACES, DECOR),
    ("chart.grid", SURFACES, DECOR),
]
# Syntax colors are painted on the code surfaces themselves, not only on the page.
CODE_SURFACES = ["color.code-bg", "color.code-inline-bg"]
PAIRS += [(f, CODE_SURFACES, TEXT) for f in (
    "color.text", "color.text-muted", "color.accent-text", "color.success",
    "color.warning", "color.error", "color.info", "graph.refinement")]


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


def strip_comments(text):
    """Drop /* */ comments and keep newlines so later line numbers still match."""
    return re.sub(r"/\*.*?\*/", lambda m: "\n" * m.group(0).count("\n"), text, flags=re.S)


def has_raw_color(text):
    """True when text contains a color that is not a token or an allowed keyword."""
    stripped = strip_comments(text)
    if RAW_FUNC.search(stripped):
        return True
    for match in NAMED_COLOR.finditer(stripped):
        if match.group(0).lower() not in ALLOWED_KEYWORDS:
            return True
    return False


def self_test_colors():
    failed = False
    for sample, expect in COLOR_SCAN_SAMPLES:
        got = has_raw_color(sample)
        if got != expect:
            print(f"color scan self-test failed: {sample!r} -> {got}, want {expect}", file=sys.stderr)
            failed = True
    return failed


def check_extra_css():
    """Fail if the docs theme hard-codes a color instead of a token."""
    if self_test_colors():
        return True
    if not EXTRA.exists():
        print("missing " + str(EXTRA), file=sys.stderr)
        return True
    text = strip_comments(EXTRA.read_text(encoding="utf-8"))
    bad = []
    for i, line in enumerate(text.splitlines(), 1):
        if has_raw_color(line):
            bad.append(f"docs/stylesheets/extra.css:{i}: {line.strip()}")
    if bad:
        print("extra.css must use tokens, not a raw color:", file=sys.stderr)
        for item in bad:
            print(item, file=sys.stderr)
    return bool(bad)


def iter_decl_blocks(text):
    """Yield (prelude, declarations) for each style rule, including inside groups."""
    i, n = 0, len(text)
    while True:
        start = text.find("{", i)
        if start < 0:
            return
        depth, j = 1, start + 1
        while j < n and depth:
            if text[j] == "{":
                depth += 1
            elif text[j] == "}":
                depth -= 1
            j += 1
        body = text[start + 1:j - 1]
        if "{" in body:
            yield from iter_decl_blocks(body)
        else:
            yield text[i:start].strip(), body
        i = j


def declarations(body):
    for part in body.split(";"):
        if ":" not in part:
            continue
        prop, val = part.split(":", 1)
        yield prop.strip().lower(), val.strip()


def offset_is_inset(value):
    """True when an outline-offset is negative, so the ring sits on the element."""
    if re.search(r"(^|[\s,(])-\d", value):
        return True
    return re.search(r"\*\s*-\s*1\b", value) is not None


def focus_outline_problems(text):
    """Rules that paint focus-on-inverse as an outline without an inset offset.

    An outset ring is drawn on the page behind the control. The same
    declaration block has to set a negative outline-offset; a positive or
    inherited offset does not count.
    """
    problems = []
    for prelude, body in iter_decl_blocks(strip_comments(text)):
        props = dict(declarations(body))
        outline = props.get("outline", "")
        color = props.get("outline-color", "")
        if FOCUS_ON_INVERSE not in outline and FOCUS_ON_INVERSE not in color:
            continue
        if not offset_is_inset(props.get("outline-offset", "")):
            problems.append(" ".join(prelude.split()))
    return problems


def check_focus_on_inverse_scope(tokens):
    """Fail when focus-on-inverse is used as a page ring, and self-test the scan."""
    samples = [
        ("button:focus-visible { outline-color: " + FOCUS_ON_INVERSE + "; }", True),
        ("a:focus-visible { outline-color: " + FOCUS_ON_INVERSE + "; outline-offset: 2px; }", True),
        ("a:focus-visible { outline-color: " + FOCUS_ON_INVERSE
         + "; outline-offset: calc(var(--sdf-border-width-strong) * -1); }", False),
        ("@media (min-width: 10em) { a { outline-color: " + FOCUS_ON_INVERSE
         + "; outline-offset: -2px; } }", False),
    ]
    failed = False
    for sample, expect_problem in samples:
        got = bool(focus_outline_problems(sample))
        if got != expect_problem:
            print(f"focus scope self-test failed: {sample!r} -> {got}, want problem {expect_problem}",
                  file=sys.stderr)
            failed = True
    for path in FOCUS_STYLES:
        if not path.exists():
            print("missing " + str(path), file=sys.stderr)
            failed = True
            continue
        problems = focus_outline_problems(path.read_text(encoding="utf-8"))
        if not problems:
            continue
        failed = True
        print(path.relative_to(HERE.parent).as_posix()
              + " uses focus-on-inverse without an inset outline-offset.", file=sys.stderr)
        print("That ring sits on the page. Measured contrast against color.bg:", file=sys.stderr)
        for dark, label in ((False, "light"), (True, "dark")):
            for bg in SURFACES:
                fg_hex = resolve_mode(tokens, "color.focus-on-inverse", dark)
                bg_hex = resolve_mode(tokens, bg, dark)
                r = ratio(fg_hex, bg_hex)
                mark = "FAIL" if r + 1e-9 < UI else "pass"
                print(f"  {label} {fg_hex} on {bg} {bg_hex} {r:.2f}:1 {mark}", file=sys.stderr)
        for prelude in problems:
            print("  " + prelude, file=sys.stderr)
    return failed


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
    focus_failed = check_focus_on_inverse_scope(tokens)
    failed = light_failed or dark_failed or extra_failed or focus_failed
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
