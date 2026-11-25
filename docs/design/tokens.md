# SDF design tokens

One brand identity across three surfaces: the app UI (`internal/ui`), the docs
(`README.md` and `docs/`), and the landing page (the `sdf-landingpage` repo).
The mark, the cream, charcoal and rust palette, the type families and the tone
are shared. Each surface keeps its own layout and density.

| File | Role |
| --- | --- |
| `design/tokens.json` | Canonical source. W3C Design Tokens–style JSON (`$type`, `$value`, `{alias}` references). |
| `design/tokens.css` | CSS custom properties generated from the JSON. Self-contained: no `@import`, no `url()`, no URLs. |
| `design/tokens.py` | Generator and checker. Python 3 standard library only; not part of the Go build. |
| `docs/design/tokens.md` | This spec. |

Brand assets (mark, favicon) are canonical as they are and are not redrawn or
recolored here. The mark is cream `#F6F1E8`, a charcoal `#2F3640` weave, and
one rust `#C65A2E` thread that leaves as the chosen fork. The tokens follow
that idea: charcoal carries the structure, and rust marks the one path or
state that matters.

```bash
python3 design/tokens.py           # regenerate design/tokens.css
python3 design/tokens.py --check   # fail if tokens.css is stale or a pairing fails
python3 design/tokens.py --table   # print the contrast table below
```

Edit `tokens.json`, then regenerate. Do not edit `tokens.css` by hand.

## Naming

All custom properties use the `--sdf-` prefix, in two layers.

- **Primitive**: raw values. `--sdf-color-cream`, `--sdf-color-charcoal`,
  `--sdf-color-rust`, and the ramps `--sdf-color-{cream,charcoal,rust}-NNN`,
  plus the state hues (green, amber, red, blue, violet).
- **Semantic**: what a value is for. `--sdf-color-*`, `--sdf-graph-*`,
  `--sdf-chart-*`, `--sdf-font-*`, `--sdf-line-height-*`, `--sdf-space-*`,
  `--sdf-radius-*`, `--sdf-border-width-*`, `--sdf-shadow-*`, `--sdf-table-*`.

Components use semantic tokens only. A primitive appears in component CSS only
when no semantic token fits, and that is a sign a semantic token is missing.
In CSS, semantic tokens are `var()` references to primitives, so a theme (for
example a future dark variant) overrides the semantic layer only.

## Primitive palette

Ratios against brand cream and brand charcoal. The neutral ramps are derived
from cream (warm paper) and charcoal (cool slate). The rust ramp runs from a
tint to a text-safe deep rust. Green, amber, red, blue and violet exist only
for states, the decision graph and charts.

| Token | Hex | vs cream | vs charcoal |
| --- | --- | ---: | ---: |
| `--sdf-color-cream` | `#F6F1E8` | 1.00 | 10.84 |
| `--sdf-color-charcoal` | `#2F3640` | 10.84 | 1.00 |
| `--sdf-color-rust` | `#C65A2E` | 3.81 | 2.85 |
| `--sdf-color-cream-50` | `#FBF7F1` | 1.05 | 11.42 |
| `--sdf-color-cream-100` | `#F6F1E8` | 1.00 | 10.84 |
| `--sdf-color-cream-200` | `#EBE3D6` | 1.13 | 9.57 |
| `--sdf-color-cream-300` | `#E2D9CA` | 1.24 | 8.71 |
| `--sdf-color-cream-400` | `#D9CFC0` | 1.37 | 7.92 |
| `--sdf-color-charcoal-200` | `#C9CED4` | 1.41 | 7.70 |
| `--sdf-color-charcoal-300` | `#A3AAB3` | 2.08 | 5.20 |
| `--sdf-color-charcoal-400` | `#7A828C` | 3.46 | 3.13 |
| `--sdf-color-charcoal-500` | `#6B7480` | 4.21 | 2.57 |
| `--sdf-color-charcoal-600` | `#5A636E` | 5.42 | 2.00 |
| `--sdf-color-charcoal-700` | `#4E5864` | 6.43 | 1.69 |
| `--sdf-color-charcoal-900` | `#2F3640` | 10.84 | 1.00 |
| `--sdf-color-rust-50` | `#FBEFE9` | 1.00 | 10.81 |
| `--sdf-color-rust-100` | `#F3E4D8` | 1.10 | 9.81 |
| `--sdf-color-rust-200` | `#F3C7B6` | 1.37 | 7.93 |
| `--sdf-color-rust-500` | `#C65A2E` | 3.81 | 2.85 |
| `--sdf-color-rust-600` | `#A94A23` | 5.06 | 2.14 |
| `--sdf-color-rust-700` | `#8E3E1C` | 6.54 | 1.66 |
| `--sdf-color-rust-800` | `#6F3016` | 8.85 | 1.23 |
| `--sdf-color-green-50` | `#E6F1EA` | 1.03 | 10.53 |
| `--sdf-color-green-700` | `#145C38` | 7.13 | 1.52 |
| `--sdf-color-amber-50` | `#F8ECD8` | 1.04 | 10.44 |
| `--sdf-color-amber-200` | `#ECD2A6` | 1.30 | 8.32 |
| `--sdf-color-amber-700` | `#8A4B08` | 6.04 | 1.79 |
| `--sdf-color-red-50` | `#F8E3E5` | 1.09 | 9.93 |
| `--sdf-color-red-700` | `#9B2335` | 6.95 | 1.56 |
| `--sdf-color-blue-50` | `#E5EDF5` | 1.05 | 10.31 |
| `--sdf-color-blue-700` | `#2B5A86` | 6.42 | 1.69 |
| `--sdf-color-violet-50` | `#ECE9F6` | 1.06 | 10.19 |
| `--sdf-color-violet-700` | `#5B4A9E` | 6.40 | 1.69 |

Notes:

- `rust-500` is the brand rust. At 3.81:1 on cream it passes for UI parts and
  large text (24px, or 18.66px bold), and fails for body text. Use
  `rust-700` (6.54:1) wherever rust has to be read as text.
- `red-700` is crimson, not rust. An error or conflict must never be confused
  with the chosen path, so the two hues are kept apart, and conflict also has
  its own dash pattern.
- Several values match what the landing page already ships (`#FBF7F1`,
  `#EBE3D6`, `#D9CFC0`, `#4E5864`, `#8E3E1C`, `#F3E4D8`, `#F3C7B6`,
  `#145C38`), so adopting the tokens there changes almost nothing visually.

## Semantic tokens

| Token | Resolves to | Hex |
| --- | --- | --- |
| `--sdf-color-bg` | `{color.cream-100}` | `#F6F1E8` |
| `--sdf-color-surface` | `{color.cream-100}` | `#F6F1E8` |
| `--sdf-color-surface-raised` | `{color.cream-50}` | `#FBF7F1` |
| `--sdf-color-surface-sunken` | `{color.cream-200}` | `#EBE3D6` |
| `--sdf-color-text` | `{color.charcoal-900}` | `#2F3640` |
| `--sdf-color-text-muted` | `{color.charcoal-700}` | `#4E5864` |
| `--sdf-color-text-inverse` | `{color.cream-100}` | `#F6F1E8` |
| `--sdf-color-text-inverse-muted` | `{color.cream-400}` | `#D9CFC0` |
| `--sdf-color-surface-inverse` | `{color.charcoal-900}` | `#2F3640` |
| `--sdf-color-border` | `{color.cream-400}` | `#D9CFC0` |
| `--sdf-color-border-strong` | `{color.charcoal-500}` | `#6B7480` |
| `--sdf-color-accent` | `{color.rust-500}` | `#C65A2E` |
| `--sdf-color-accent-soft` | `{color.rust-100}` | `#F3E4D8` |
| `--sdf-color-accent-text` | `{color.rust-700}` | `#8E3E1C` |
| `--sdf-color-link` | `{color.accent-text}` | `#8E3E1C` |
| `--sdf-color-focus` | `{color.rust-500}` | `#C65A2E` |
| `--sdf-color-accent-on-inverse` | `{color.rust-200}` | `#F3C7B6` |
| `--sdf-color-focus-on-inverse` | `{color.rust-200}` | `#F3C7B6` |
| `--sdf-color-success` | `{color.green-700}` | `#145C38` |
| `--sdf-color-success-soft` | `{color.green-50}` | `#E6F1EA` |
| `--sdf-color-warning` | `{color.amber-700}` | `#8A4B08` |
| `--sdf-color-warning-soft` | `{color.amber-50}` | `#F8ECD8` |
| `--sdf-color-error` | `{color.red-700}` | `#9B2335` |
| `--sdf-color-error-soft` | `{color.red-50}` | `#F8E3E5` |
| `--sdf-color-info` | `{color.blue-700}` | `#2B5A86` |
| `--sdf-color-info-soft` | `{color.blue-50}` | `#E5EDF5` |
| `--sdf-graph-node-fill` | `{color.surface-raised}` | `#FBF7F1` |
| `--sdf-graph-node-stroke` | `{color.text}` | `#2F3640` |
| `--sdf-graph-node-text` | `{color.text}` | `#2F3640` |
| `--sdf-graph-chosen` | `{color.accent}` | `#C65A2E` |
| `--sdf-graph-chosen-fill` | `{color.text}` | `#2F3640` |
| `--sdf-graph-not-taken` | `{color.charcoal-500}` | `#6B7480` |
| `--sdf-graph-future` | `{color.charcoal-400}` | `#7A828C` |
| `--sdf-graph-edge` | `{color.charcoal-500}` | `#6B7480` |
| `--sdf-graph-refinement` | `{color.violet-700}` | `#5B4A9E` |
| `--sdf-graph-refinement-soft` | `{color.violet-50}` | `#ECE9F6` |
| `--sdf-graph-conflict` | `{color.error}` | `#9B2335` |
| `--sdf-graph-unknown` | `{color.warning}` | `#8A4B08` |
| `--sdf-graph-hatch-bg` | `{color.amber-50}` | `#F8ECD8` |
| `--sdf-graph-hatch-line` | `{color.amber-200}` | `#ECD2A6` |
| `--sdf-graph-ending` | `{color.success}` | `#145C38` |
| `--sdf-graph-ending-soft` | `{color.success-soft}` | `#E6F1EA` |
| `--sdf-graph-muted-fill` | `{color.surface-sunken}` | `#EBE3D6` |
| `--sdf-graph-muted-stroke` | `{color.charcoal-400}` | `#7A828C` |
| `--sdf-graph-muted-text` | `{color.text}` | `#2F3640` |
| `--sdf-graph-track` | `{color.charcoal-400}` | `#7A828C` |
| `--sdf-chart-series-1` | `{color.charcoal-700}` | `#4E5864` |
| `--sdf-chart-series-2` | `{color.rust-500}` | `#C65A2E` |
| `--sdf-chart-series-3` | `{color.blue-700}` | `#2B5A86` |
| `--sdf-chart-series-4` | `{color.amber-700}` | `#8A4B08` |
| `--sdf-chart-axis` | `{color.border-strong}` | `#6B7480` |
| `--sdf-chart-grid` | `{color.border}` | `#D9CFC0` |
| `--sdf-chart-text` | `{color.text-muted}` | `#4E5864` |

`--sdf-color-bg` and `--sdf-color-surface` share a value today. They are kept
apart so a surface can change later without touching the page background.
`--sdf-color-border` is for decorative dividers only (1.37:1). Anything that
marks the edge of a control a person must find (inputs, toggles, chart axes)
uses `--sdf-color-border-strong` (4.21:1).

### Decision graph

| Meaning | Token | Value | How it is drawn |
| --- | --- | --- | --- |
| Node | `--sdf-graph-node-fill`, `-node-stroke`, `-node-text` | raised cream, charcoal | 1.5px stroke |
| Chosen path | `--sdf-graph-chosen` | rust `#C65A2E` | Edges on the chosen path at `--sdf-border-width-heavy` (3px), plus a rust outline on on-path nodes. This is the mark's rust thread. |
| Chosen decision fill | `--sdf-graph-chosen-fill` | charcoal | Text uses `--sdf-color-text-inverse` (10.84:1). The rust outline sits on the page, not on the fill (rust on charcoal is only 2.85:1). |
| Not taken | `--sdf-graph-not-taken` | charcoal-500 `#6B7480` | Dashed `5 4`, no fill change. 4.21:1. |
| Future / unreached (replay) | `--sdf-graph-future` | charcoal-400 `#7A828C` | Dashed `3 3`, no fill. **3.46:1 on bg, 3.05:1 on sunken; replaces `#b9b0a0` (1.87:1).** |
| Edge (default) | `--sdf-graph-edge` | charcoal-500 | 1px. Replaces `#8a8275`. |
| Refinement | `--sdf-graph-refinement` | violet `#5B4A9E` | Stroke and chip text (6.40:1). |
| Conflict | `--sdf-graph-conflict` | crimson `#9B2335` | 2px with the crack dash `9 3 2 3`. Never color alone. |
| Unknown / locked | `--sdf-graph-unknown`, `-hatch-bg`, `-hatch-line` | amber `#8A4B08` | Locked adds the amber hatch fill; unknown text stays italic. |
| Ending | `--sdf-graph-ending`, `-ending-soft` | green `#145C38` | 3px double border on a green tint. |
| Muted (filtered out) | `--sdf-graph-muted-fill`, `-muted-stroke`, `-muted-text` | sunken cream, charcoal-400, charcoal | Recolored, never faded: text stays 9.57:1. |
| Replay track | `--sdf-graph-track` | charcoal-400 | Replaces `#b9b0a0`. Past ticks use `--sdf-color-text-muted`. |

The graph never relies on color alone: chosen is also heavier, not-taken and
future are dashed with different rhythms, conflict has the crack pattern,
locked has the hatch, and ending has the double border. The key on the
Decisions tab keeps showing those shapes.

### Charts (replaces the green/amber series from #318)

| Token | Value | Use |
| --- | --- | --- |
| `--sdf-chart-series-1` | charcoal-700 `#4E5864` | First series, usually bars (sessions; unlinked commits). |
| `--sdf-chart-series-2` | rust `#C65A2E` | Second series, or a line drawn over bars (cost; unlinked decisions). |
| `--sdf-chart-series-3` | blue `#2B5A86` | Reserved. |
| `--sdf-chart-series-4` | amber `#8A4B08` | Reserved. |
| `--sdf-chart-axis` | border-strong | Axis lines. |
| `--sdf-chart-grid` | border | Gridlines (decorative). |
| `--sdf-chart-text` | text-muted | Ticks and legend (6.43:1). |

Every series passes 3:1 against all surfaces. Series 1 and 2 differ by hue
(gray and rust) but are only 1.69:1 apart in luminance, so adjacent bars keep a
1px surface gap, the legend has swatches with names, and each chart keeps its
data table (`.chart-table`). That table is the text alternative.

Mapping for the classes merged in #318:

| Class in `app.css` | Today | Token |
| --- | --- | --- |
| `.chart-bar-a` | `--green` | `--sdf-chart-series-1` |
| `.chart-bar-b` | `--amber` | `--sdf-chart-series-2` |
| `.chart-line`, `.chart-point` | `--green` | `--sdf-chart-series-2` (rust line, 2px) |
| `.chart-axis` | `--line-strong` | `--sdf-chart-axis` |
| `.chart-grid` | `--line` | `--sdf-chart-grid` |
| `.chart-tick`, `.chart-legend` | `--ink-soft` | `--sdf-chart-text` |
| `.chart-empty` | `--ink-soft` | `--sdf-color-text-muted` |

### Mapping from today's `app.css`

For the restyle PR. Nothing in `internal/ui` changes in this PR.

| Today | Token |
| --- | --- |
| `--paper` `#f3efe6` | `--sdf-color-bg` |
| `--paper-raised` `#faf7f1` | `--sdf-color-surface-raised` |
| `--ink` `#1d1a15` | `--sdf-color-text` |
| `--ink-soft` `#5e584e`, `#4a453d`, `#5e584e` (edge ticks) | `--sdf-color-text-muted` |
| `--line` `#d9d2c4` | `--sdf-color-border` |
| `--line-strong` `#1d1a15` (header and table-head rules) | `--sdf-color-text` for those two heavy rules; `--sdf-color-border-strong` elsewhere |
| `--green` on links, `.span-more summary`, `.dg-linkish` | `--sdf-color-link` (rust-700) |
| `--green` on focus outlines and the keyboard ring | `--sdf-color-focus` (rust) |
| `--green` on `.known-zero`, endings | `--sdf-color-success`, `--sdf-graph-ending` |
| `--green-soft`, `.callout.ok`, replay note (`#0a4f39` text) | `--sdf-color-success-soft`, `--sdf-color-success` |
| `--amber`, `--amber-soft`, `#e4d0aa` | `--sdf-color-warning`, `-warning-soft`; borders use the state color |
| `--error`, `--error-soft`, `#e4b6ad` | `--sdf-color-error`, `-error-soft` |
| `#a3301d` (challenged, crack, red chip) | `--sdf-graph-conflict` |
| `#5b4a9e` | `--sdf-graph-refinement` |
| `#8a8275` (edges, ghost, superseded, pills, progress value) | `--sdf-graph-edge` / `--sdf-graph-not-taken` |
| `#b9b0a0`, `#a39a8a`, `#c9c1b2` (future, track) | `--sdf-graph-future`, `--sdf-graph-track` |
| `#e9e4d9`, `#3a352e` (muted) | `--sdf-graph-muted-fill`, `--sdf-graph-muted-text` |
| `#4a3a22` (locked detail) | `--sdf-color-text` |
| `#f8ecd8`, `#ecd2a6` (hatch) | `--sdf-graph-hatch-bg`, `--sdf-graph-hatch-line` |
| `#e6e0d4` (progress track) | `--sdf-color-surface-sunken` |
| `#d9d2c4` (detail on a chosen node) | `--sdf-color-text-inverse-muted` |
| `#fff` (crack glyph line) | `--sdf-color-surface-raised` |
| `--shadow` | `--sdf-shadow-sm` |
| `.mark-badge` "SDF" box | The mark as inline SVG (CSP forbids images); see the UI section. |

## Contrast

Computed by `python3 design/tokens.py --table` with the WCAG 2.x relative
luminance formula, not by eye. Text needs 4.5:1. Large text and UI parts
(strokes, borders that identify a control, focus rings, chart marks) need
3:1. Rows marked "info only" are decorative or are documented limits that the
rules above work around. `--check` fails if any text or UI row fails.
`--sdf-color-bg` stands for `--sdf-color-surface` too (same value).

| Foreground | Hex | Background | Hex | Ratio | Needs | Result |
| --- | --- | --- | --- | ---: | --- | --- |
| `--sdf-color-text` | `#2F3640` | `--sdf-color-bg` | `#F6F1E8` | 10.84:1 | text 4.5 | pass |
| `--sdf-color-text` | `#2F3640` | `--sdf-color-surface-raised` | `#FBF7F1` | 11.42:1 | text 4.5 | pass |
| `--sdf-color-text` | `#2F3640` | `--sdf-color-surface-sunken` | `#EBE3D6` | 9.57:1 | text 4.5 | pass |
| `--sdf-color-text-muted` | `#4E5864` | `--sdf-color-bg` | `#F6F1E8` | 6.43:1 | text 4.5 | pass |
| `--sdf-color-text-muted` | `#4E5864` | `--sdf-color-surface-raised` | `#FBF7F1` | 6.78:1 | text 4.5 | pass |
| `--sdf-color-text-muted` | `#4E5864` | `--sdf-color-surface-sunken` | `#EBE3D6` | 5.68:1 | text 4.5 | pass |
| `--sdf-color-accent-text` | `#8E3E1C` | `--sdf-color-bg` | `#F6F1E8` | 6.54:1 | text 4.5 | pass |
| `--sdf-color-accent-text` | `#8E3E1C` | `--sdf-color-surface-raised` | `#FBF7F1` | 6.89:1 | text 4.5 | pass |
| `--sdf-color-accent-text` | `#8E3E1C` | `--sdf-color-surface-sunken` | `#EBE3D6` | 5.77:1 | text 4.5 | pass |
| `--sdf-color-success` | `#145C38` | `--sdf-color-bg` | `#F6F1E8` | 7.13:1 | text 4.5 | pass |
| `--sdf-color-success` | `#145C38` | `--sdf-color-surface-raised` | `#FBF7F1` | 7.52:1 | text 4.5 | pass |
| `--sdf-color-success` | `#145C38` | `--sdf-color-surface-sunken` | `#EBE3D6` | 6.30:1 | text 4.5 | pass |
| `--sdf-color-warning` | `#8A4B08` | `--sdf-color-bg` | `#F6F1E8` | 6.04:1 | text 4.5 | pass |
| `--sdf-color-warning` | `#8A4B08` | `--sdf-color-surface-raised` | `#FBF7F1` | 6.37:1 | text 4.5 | pass |
| `--sdf-color-warning` | `#8A4B08` | `--sdf-color-surface-sunken` | `#EBE3D6` | 5.34:1 | text 4.5 | pass |
| `--sdf-color-error` | `#9B2335` | `--sdf-color-bg` | `#F6F1E8` | 6.95:1 | text 4.5 | pass |
| `--sdf-color-error` | `#9B2335` | `--sdf-color-surface-raised` | `#FBF7F1` | 7.33:1 | text 4.5 | pass |
| `--sdf-color-error` | `#9B2335` | `--sdf-color-surface-sunken` | `#EBE3D6` | 6.14:1 | text 4.5 | pass |
| `--sdf-color-info` | `#2B5A86` | `--sdf-color-bg` | `#F6F1E8` | 6.42:1 | text 4.5 | pass |
| `--sdf-color-info` | `#2B5A86` | `--sdf-color-surface-raised` | `#FBF7F1` | 6.76:1 | text 4.5 | pass |
| `--sdf-color-info` | `#2B5A86` | `--sdf-color-surface-sunken` | `#EBE3D6` | 5.67:1 | text 4.5 | pass |
| `--sdf-graph-refinement` | `#5B4A9E` | `--sdf-color-bg` | `#F6F1E8` | 6.40:1 | text 4.5 | pass |
| `--sdf-graph-refinement` | `#5B4A9E` | `--sdf-color-surface-raised` | `#FBF7F1` | 6.74:1 | text 4.5 | pass |
| `--sdf-graph-refinement` | `#5B4A9E` | `--sdf-color-surface-sunken` | `#EBE3D6` | 5.65:1 | text 4.5 | pass |
| `--sdf-chart-text` | `#4E5864` | `--sdf-color-bg` | `#F6F1E8` | 6.43:1 | text 4.5 | pass |
| `--sdf-chart-text` | `#4E5864` | `--sdf-color-surface-raised` | `#FBF7F1` | 6.78:1 | text 4.5 | pass |
| `--sdf-chart-text` | `#4E5864` | `--sdf-color-surface-sunken` | `#EBE3D6` | 5.68:1 | text 4.5 | pass |
| `--sdf-color-text-inverse` | `#F6F1E8` | `--sdf-color-surface-inverse` | `#2F3640` | 10.84:1 | text 4.5 | pass |
| `--sdf-color-text-inverse` | `#F6F1E8` | `--sdf-graph-chosen-fill` | `#2F3640` | 10.84:1 | text 4.5 | pass |
| `--sdf-color-text-inverse` | `#F6F1E8` | `--sdf-color-accent-text` | `#8E3E1C` | 6.54:1 | text 4.5 | pass |
| `--sdf-color-text-inverse-muted` | `#D9CFC0` | `--sdf-color-surface-inverse` | `#2F3640` | 7.92:1 | text 4.5 | pass |
| `--sdf-color-accent-on-inverse` | `#F3C7B6` | `--sdf-color-surface-inverse` | `#2F3640` | 7.93:1 | text 4.5 | pass |
| `--sdf-color-accent-text` | `#8E3E1C` | `--sdf-color-accent-soft` | `#F3E4D8` | 5.92:1 | text 4.5 | pass |
| `--sdf-color-text` | `#2F3640` | `--sdf-color-accent-soft` | `#F3E4D8` | 9.81:1 | text 4.5 | pass |
| `--sdf-color-success` | `#145C38` | `--sdf-color-success-soft` | `#E6F1EA` | 6.93:1 | text 4.5 | pass |
| `--sdf-color-warning` | `#8A4B08` | `--sdf-color-warning-soft` | `#F8ECD8` | 5.82:1 | text 4.5 | pass |
| `--sdf-color-error` | `#9B2335` | `--sdf-color-error-soft` | `#F8E3E5` | 6.37:1 | text 4.5 | pass |
| `--sdf-color-info` | `#2B5A86` | `--sdf-color-info-soft` | `#E5EDF5` | 6.11:1 | text 4.5 | pass |
| `--sdf-graph-refinement` | `#5B4A9E` | `--sdf-graph-refinement-soft` | `#ECE9F6` | 6.02:1 | text 4.5 | pass |
| `--sdf-graph-node-text` | `#2F3640` | `--sdf-graph-node-fill` | `#FBF7F1` | 11.42:1 | text 4.5 | pass |
| `--sdf-graph-node-text` | `#2F3640` | `--sdf-graph-hatch-bg` | `#F8ECD8` | 10.44:1 | text 4.5 | pass |
| `--sdf-graph-node-text` | `#2F3640` | `--sdf-graph-ending-soft` | `#E6F1EA` | 10.53:1 | text 4.5 | pass |
| `--sdf-graph-muted-text` | `#2F3640` | `--sdf-graph-muted-fill` | `#EBE3D6` | 9.57:1 | text 4.5 | pass |
| `--sdf-color-accent` | `#C65A2E` | `--sdf-color-bg` | `#F6F1E8` | 3.81:1 | UI 3.0 | pass |
| `--sdf-color-accent` | `#C65A2E` | `--sdf-color-surface-raised` | `#FBF7F1` | 4.01:1 | UI 3.0 | pass |
| `--sdf-color-accent` | `#C65A2E` | `--sdf-color-surface-sunken` | `#EBE3D6` | 3.36:1 | UI 3.0 | pass |
| `--sdf-color-focus` | `#C65A2E` | `--sdf-color-bg` | `#F6F1E8` | 3.81:1 | UI 3.0 | pass |
| `--sdf-color-focus` | `#C65A2E` | `--sdf-color-surface-raised` | `#FBF7F1` | 4.01:1 | UI 3.0 | pass |
| `--sdf-color-focus` | `#C65A2E` | `--sdf-color-surface-sunken` | `#EBE3D6` | 3.36:1 | UI 3.0 | pass |
| `--sdf-color-border-strong` | `#6B7480` | `--sdf-color-bg` | `#F6F1E8` | 4.21:1 | UI 3.0 | pass |
| `--sdf-color-border-strong` | `#6B7480` | `--sdf-color-surface-raised` | `#FBF7F1` | 4.44:1 | UI 3.0 | pass |
| `--sdf-color-border-strong` | `#6B7480` | `--sdf-color-surface-sunken` | `#EBE3D6` | 3.72:1 | UI 3.0 | pass |
| `--sdf-graph-chosen` | `#C65A2E` | `--sdf-color-bg` | `#F6F1E8` | 3.81:1 | UI 3.0 | pass |
| `--sdf-graph-chosen` | `#C65A2E` | `--sdf-color-surface-raised` | `#FBF7F1` | 4.01:1 | UI 3.0 | pass |
| `--sdf-graph-chosen` | `#C65A2E` | `--sdf-color-surface-sunken` | `#EBE3D6` | 3.36:1 | UI 3.0 | pass |
| `--sdf-graph-not-taken` | `#6B7480` | `--sdf-color-bg` | `#F6F1E8` | 4.21:1 | UI 3.0 | pass |
| `--sdf-graph-not-taken` | `#6B7480` | `--sdf-color-surface-raised` | `#FBF7F1` | 4.44:1 | UI 3.0 | pass |
| `--sdf-graph-not-taken` | `#6B7480` | `--sdf-color-surface-sunken` | `#EBE3D6` | 3.72:1 | UI 3.0 | pass |
| `--sdf-graph-future` | `#7A828C` | `--sdf-color-bg` | `#F6F1E8` | 3.46:1 | UI 3.0 | pass |
| `--sdf-graph-future` | `#7A828C` | `--sdf-color-surface-raised` | `#FBF7F1` | 3.64:1 | UI 3.0 | pass |
| `--sdf-graph-future` | `#7A828C` | `--sdf-color-surface-sunken` | `#EBE3D6` | 3.05:1 | UI 3.0 | pass |
| `--sdf-graph-edge` | `#6B7480` | `--sdf-color-bg` | `#F6F1E8` | 4.21:1 | UI 3.0 | pass |
| `--sdf-graph-edge` | `#6B7480` | `--sdf-color-surface-raised` | `#FBF7F1` | 4.44:1 | UI 3.0 | pass |
| `--sdf-graph-edge` | `#6B7480` | `--sdf-color-surface-sunken` | `#EBE3D6` | 3.72:1 | UI 3.0 | pass |
| `--sdf-graph-conflict` | `#9B2335` | `--sdf-color-bg` | `#F6F1E8` | 6.95:1 | UI 3.0 | pass |
| `--sdf-graph-conflict` | `#9B2335` | `--sdf-color-surface-raised` | `#FBF7F1` | 7.33:1 | UI 3.0 | pass |
| `--sdf-graph-conflict` | `#9B2335` | `--sdf-color-surface-sunken` | `#EBE3D6` | 6.14:1 | UI 3.0 | pass |
| `--sdf-graph-unknown` | `#8A4B08` | `--sdf-color-bg` | `#F6F1E8` | 6.04:1 | UI 3.0 | pass |
| `--sdf-graph-unknown` | `#8A4B08` | `--sdf-color-surface-raised` | `#FBF7F1` | 6.37:1 | UI 3.0 | pass |
| `--sdf-graph-unknown` | `#8A4B08` | `--sdf-color-surface-sunken` | `#EBE3D6` | 5.34:1 | UI 3.0 | pass |
| `--sdf-graph-ending` | `#145C38` | `--sdf-color-bg` | `#F6F1E8` | 7.13:1 | UI 3.0 | pass |
| `--sdf-graph-ending` | `#145C38` | `--sdf-color-surface-raised` | `#FBF7F1` | 7.52:1 | UI 3.0 | pass |
| `--sdf-graph-ending` | `#145C38` | `--sdf-color-surface-sunken` | `#EBE3D6` | 6.30:1 | UI 3.0 | pass |
| `--sdf-graph-track` | `#7A828C` | `--sdf-color-bg` | `#F6F1E8` | 3.46:1 | UI 3.0 | pass |
| `--sdf-graph-track` | `#7A828C` | `--sdf-color-surface-raised` | `#FBF7F1` | 3.64:1 | UI 3.0 | pass |
| `--sdf-graph-track` | `#7A828C` | `--sdf-color-surface-sunken` | `#EBE3D6` | 3.05:1 | UI 3.0 | pass |
| `--sdf-chart-series-1` | `#4E5864` | `--sdf-color-bg` | `#F6F1E8` | 6.43:1 | UI 3.0 | pass |
| `--sdf-chart-series-1` | `#4E5864` | `--sdf-color-surface-raised` | `#FBF7F1` | 6.78:1 | UI 3.0 | pass |
| `--sdf-chart-series-1` | `#4E5864` | `--sdf-color-surface-sunken` | `#EBE3D6` | 5.68:1 | UI 3.0 | pass |
| `--sdf-chart-series-2` | `#C65A2E` | `--sdf-color-bg` | `#F6F1E8` | 3.81:1 | UI 3.0 | pass |
| `--sdf-chart-series-2` | `#C65A2E` | `--sdf-color-surface-raised` | `#FBF7F1` | 4.01:1 | UI 3.0 | pass |
| `--sdf-chart-series-2` | `#C65A2E` | `--sdf-color-surface-sunken` | `#EBE3D6` | 3.36:1 | UI 3.0 | pass |
| `--sdf-chart-series-3` | `#2B5A86` | `--sdf-color-bg` | `#F6F1E8` | 6.42:1 | UI 3.0 | pass |
| `--sdf-chart-series-3` | `#2B5A86` | `--sdf-color-surface-raised` | `#FBF7F1` | 6.76:1 | UI 3.0 | pass |
| `--sdf-chart-series-3` | `#2B5A86` | `--sdf-color-surface-sunken` | `#EBE3D6` | 5.67:1 | UI 3.0 | pass |
| `--sdf-chart-series-4` | `#8A4B08` | `--sdf-color-bg` | `#F6F1E8` | 6.04:1 | UI 3.0 | pass |
| `--sdf-chart-series-4` | `#8A4B08` | `--sdf-color-surface-raised` | `#FBF7F1` | 6.37:1 | UI 3.0 | pass |
| `--sdf-chart-series-4` | `#8A4B08` | `--sdf-color-surface-sunken` | `#EBE3D6` | 5.34:1 | UI 3.0 | pass |
| `--sdf-chart-axis` | `#6B7480` | `--sdf-color-bg` | `#F6F1E8` | 4.21:1 | UI 3.0 | pass |
| `--sdf-chart-axis` | `#6B7480` | `--sdf-color-surface-raised` | `#FBF7F1` | 4.44:1 | UI 3.0 | pass |
| `--sdf-chart-axis` | `#6B7480` | `--sdf-color-surface-sunken` | `#EBE3D6` | 3.72:1 | UI 3.0 | pass |
| `--sdf-graph-muted-stroke` | `#7A828C` | `--sdf-graph-muted-fill` | `#EBE3D6` | 3.05:1 | UI 3.0 | pass |
| `--sdf-color-focus-on-inverse` | `#F3C7B6` | `--sdf-color-surface-inverse` | `#2F3640` | 7.93:1 | UI 3.0 | pass |
| `--sdf-color-text-inverse` | `#F6F1E8` | `--sdf-color-accent` | `#C65A2E` | 3.81:1 | info only | n/a |
| `--sdf-color-accent` | `#C65A2E` | `--sdf-color-surface-inverse` | `#2F3640` | 2.85:1 | info only | n/a |
| `--sdf-graph-chosen` | `#C65A2E` | `--sdf-graph-chosen-fill` | `#2F3640` | 2.85:1 | info only | n/a |
| `--sdf-chart-series-2` | `#C65A2E` | `--sdf-chart-series-1` | `#4E5864` | 1.69:1 | info only | n/a |
| `--sdf-color-border` | `#D9CFC0` | `--sdf-color-bg` | `#F6F1E8` | 1.37:1 | info only | n/a |
| `--sdf-color-border` | `#D9CFC0` | `--sdf-color-surface-raised` | `#FBF7F1` | 1.44:1 | info only | n/a |
| `--sdf-color-border` | `#D9CFC0` | `--sdf-color-surface-sunken` | `#EBE3D6` | 1.21:1 | info only | n/a |
| `--sdf-chart-grid` | `#D9CFC0` | `--sdf-color-bg` | `#F6F1E8` | 1.37:1 | info only | n/a |
| `--sdf-chart-grid` | `#D9CFC0` | `--sdf-color-surface-raised` | `#FBF7F1` | 1.44:1 | info only | n/a |
| `--sdf-chart-grid` | `#D9CFC0` | `--sdf-color-surface-sunken` | `#EBE3D6` | 1.21:1 | info only | n/a |

Pairings that do not pass and are therefore not allowed:

- Rust (`--sdf-color-accent`) as body or small text on any cream: 3.36–4.01:1.
  Use `--sdf-color-accent-text`.
- Cream text on a rust fill: 3.81:1. A rust button or badge uses the
  `--sdf-color-accent-text` fill (6.54:1) or is charcoal with a rust edge.
- Rust on charcoal: 2.85:1. On `--sdf-color-surface-inverse` use
  `--sdf-color-accent-on-inverse` and `--sdf-color-focus-on-inverse` (7.93:1).
- `--sdf-color-border` to mark a control's edge: 1.21–1.44:1.

## Dark variant

Deferred. The UI declares `color-scheme: light`, and the graph, hatch and
chart colors have only been tuned for cream. A `prefers-color-scheme: dark`
block in `tokens.css` would flip the UI the moment the file is loaded, while
the rest of `app.css` still holds light-only values. When it is designed, it
goes in the same file under `@media (prefers-color-scheme: dark)` and
overrides the semantic layer only (including `--sdf-graph-*` and
`--sdf-chart-*`), with its own contrast table.

## Type

Font tokens are system stacks only, because the UI's CSP is
`font-src 'none'`:

| Token | Stack |
| --- | --- |
| `--sdf-font-sans` | `ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif` |
| `--sdf-font-serif` | `Georgia, "Iowan Old Style", Palatino, serif` |
| `--sdf-font-mono` | `ui-monospace, "SF Mono", "Cascadia Mono", Menlo, Consolas, monospace` |

Surfaces that may load webfonts prepend them in their own CSS, under a
surface-specific name. A custom property cannot refer to itself, so do not
redefine `--sdf-font-serif` in terms of itself:

```css
:root {
  --landing-font-serif: "Fraunces Variable", var(--sdf-font-serif);
  --landing-font-sans: "Source Sans 3 Variable", "Source Sans 3", var(--sdf-font-sans);
  --landing-font-mono: "IBM Plex Mono", var(--sdf-font-mono);
}
```

Web pairing: the landing page and the docs site both self-host Fraunces
(display serif), Source Sans 3 (body) and IBM Plex Mono, under the SIL Open
Font License. The landing page loads them through Fontsource with
`font-src 'self'`. The docs site vendors the latin woff2 files in
`docs/fonts/` (licenses beside them) and does not call the Google Fonts CDN.
It echoes the Georgia/Palatino display of the UI. The GitHub social preview
was set in Libre Baskerville and Source Sans; see open questions.

Size scale (`--sdf-font-size-*`), weights (`--sdf-font-weight-*`:
400/500/600/700) and line heights (`--sdf-line-height-*`: tight 1.1, snug
1.35, normal 1.5, relaxed 1.65):

| Token | Size | UI (dense) | Docs site | Landing |
| --- | --- | --- | --- | --- |
| `2xs` | 0.6875rem (11px) | graph labels, axis ticks, chips | — | — |
| `xs` | 0.75rem (12px) | secondary meta, rail detail | captions | fine print, kickers |
| `sm` | 0.8125rem (13px) | dense tables, card headings (caps) | table cells | labels |
| `md` | 0.875rem (14px) | body, default tables | code blocks | small print |
| `base` | 1rem (16px) | — | body | — |
| `lg` | 1.125rem (18px) | — | lead | body (landing uses 1.0625rem today) |
| `xl` | 1.25rem (20px) | panel titles (serif) | h3 | h3 |
| `2xl` | 1.5rem (24px) | page title (serif) | h2 | — |
| `3xl` | 2rem (32px) | big numbers on Overview cards | h1 | h2 floor |
| `4xl` | 3rem (48px) | — | — | h2 ceiling |
| `display` | clamp(2.6rem, 6.2vw, 4.7rem) | — | — | hero h1 |

Serif is for display only (page and panel titles, landing headings). Body,
tables and controls are sans. Ids, hashes, commands and code are mono.
Numbers in tables use `font-variant-numeric: tabular-nums`.

## Space, radius, borders, shadows, density

| Scale | Tokens |
| --- | --- |
| Space (4px base) | `--sdf-space-0` 0, `-half` 2px, `-1` 4px, `-2` 8px, `-3` 12px, `-4` 16px, `-5` 20px, `-6` 24px, `-8` 32px, `-10` 40px, `-12` 48px, `-16` 64px, `-24` 96px (in rem) |
| Radius | `--sdf-radius-none` 0, `-sm` 3px, `-md` 6px, `-lg` 8px, `-pill` 999px |
| Border width | `--sdf-border-width-hairline` 1px, `-strong` 2px (focus, conflict), `-heavy` 3px (chosen edge, ending) |
| Shadow | `--sdf-shadow-sm` (UI cards), `--sdf-shadow-md` (popovers), `--sdf-shadow-stamp` (landing's hard offset; not in the UI or docs) |

Table density:

| Token | Default | Dense |
| --- | --- | --- |
| cell padding y | `--sdf-table-cell-padding-y` 8px | `--sdf-table-cell-padding-y-dense` 4px |
| cell padding x | `--sdf-table-cell-padding-x` 12px | `--sdf-table-cell-padding-x-dense` 8px |
| font size | `--sdf-table-font-size` 14px | `--sdf-table-font-size-dense` 13px |
| line height | `--sdf-table-line-height` 1.5 | `--sdf-table-line-height-dense` 1.35 |

The UI uses dense for the sessions table, span lists and the decision list,
and default for Overview tables and the detail list. Docs and landing use
default.

## Per-surface rules

### App UI (`internal/ui`): dense

- Density: dense tables, `space-1` to `space-4` inside components, `space-6`
  to `space-8` between sections. Radius `sm` and `md`; `pill` for filter
  chips. Hairline borders, `shadow-sm` at most.
- Rust only for accents and states: the chosen path in the graph, the focus
  ring, the current-tab indicator, the selected state, chart series 2, and
  the mark's thread. Links and rust text use `accent-text`. No rust panels, no
  rust body text, no rust for errors.
- The mark replaces the "SDF" text badge in the header, inline as SVG (the CSP
  has `img-src 'none'`). Its colors stay as drawn; style it with classes, not
  inline styles.
- Tone: terse labels, sentence case, state words over symbols ("unknown", not
  "0" or "—"). No exclamation marks. Say what is missing and the command that
  fixes it.

### Docs (`README.md` and `docs/` on GitHub): readable

GitHub renders Markdown and strips CSS, so these tokens never reach this
surface as CSS. The brand shows up through images and structure only.

- Structure: one H1 per page, a short intro, then a table of contents for any
  page longer than about three screens. Commands and config in fenced code
  blocks with a language tag. Tables for reference data.
- Logo: `docs/images/logo.svg` is the canonical mark on its own cream tile,
  so it stays legible on GitHub's light and dark themes. Do not use a
  transparent version.
- Screenshots: capture the UI on its light theme, on a cream background, at
  2x device pixel ratio, cropped to the content with no browser chrome, PNG,
  under about 500 KB. Use synthetic sample data (`tests/samples/`), never a
  real session. Write alt text that says what the image shows.
- Diagrams (Mermaid, rendered by GitHub): every label must sit on a filled
  shape, because the page background can be white or near-black. Use the base
  theme with cream fills, charcoal text and borders, and charcoal-400 lines
  (`#7A828C`: 3.89:1 on GitHub's white, 4.87:1 on its dark `#0D1117`). Rust
  marks one highlighted path at most.

  ```text
  %%{init: {"theme": "base", "themeVariables": {
    "primaryColor": "#FBF7F1", "primaryTextColor": "#2F3640",
    "primaryBorderColor": "#2F3640", "lineColor": "#7A828C",
    "clusterBkg": "#EBE3D6", "clusterBorder": "#6B7480", "titleColor": "#2F3640",
    "edgeLabelBackground": "#F6F1E8",
    "fontFamily": "ui-sans-serif, system-ui, sans-serif"}}}%%
  classDef chosen stroke:#C65A2E,stroke-width:3px;
  ```

- Static SVG diagrams: the same palette, a cream background rectangle in the
  file, and text as paths or system fonts only.
- Redo or keep:
  - `docs/images/decisions-ui.png`: the Decisions page as the UI ships
    today. Overview, Sessions, and Decisions; a chosen node, a not-taken
    alternative, a locked ending, a conflict, and the replay scrubber. No
    Connections tab. Captured from a synthetic store at 2x, cropped to the
    page, no browser chrome. The UI still uses its own paper and green
    palette. Recapture again after the UI restyle.
  - `docs/images/logo.svg`: keep. It is byte-identical to the canonical mark.
  - README "How it works" Mermaid: still without the theme block. Whether
    GitHub keeps honoring `init` is an open question, so the README is
    unchanged. The docs site homepage (`docs/index.md`) includes the block.
- Tone: direct and specific. Second person and imperative ("Run `sdf serve`").
  State limits plainly. No marketing claims in the docs.

### Docs site (MkDocs Material)

Published from this repo (`make docs`, `mkdocs build --strict`) to
`https://docs.softwaredecisionfabric.tech/` when `docs/`, `skills/`, or the
site config changes on `main`. The workflow also rebuilds when
`design/tokens.css` or `design/tokens.json` changes.

It is light only. Dark tokens are still deferred, so `mkdocs.yml` sets a
single `scheme: default` palette with `primary: custom` and `accent: custom`
(no slate scheme, no palette toggle). The page sets `color-scheme: light`.
`primary: custom` is required: a palette entry without it ships
`data-md-color-primary="indigo"`.

How it consumes `tokens.css`:

- `mkdocs_hooks.py` copies `design/tokens.css` to
  `docs/stylesheets/tokens.css` in `on_pre_build` and fails the build if the
  copy is not byte-identical. That path is gitignored. Do not edit it, and do
  not commit a second copy of the values.
- `mkdocs.yml` lists `stylesheets/tokens.css` in `extra_css` ahead of
  `docs/stylesheets/extra.css`.
- `extra.css` maps Material variables onto semantic tokens and does not
  repeat hex values. Page, header, and footer use `--sdf-color-bg` or
  `--sdf-color-surface-sunken`. Text is `--sdf-color-text`. The accent is
  `--sdf-color-accent` (rust-500). Links in the page are
  `--sdf-color-link` (rust-700). In the primary sidebar, only the current
  page uses that accent text (`--sdf-color-accent-text`) at medium weight,
  on a surface-raised background (`--sdf-color-surface-raised`). That pair
  is 6.89:1. The cue is that fill and weight. The item has no inset accent
  edge at rest, hover, or focus, so the label does not shift. Hover keeps
  the same raised background. Keyboard focus keeps the focus outline.
  Ancestor and expanded section titles stay `--sdf-color-text` on a
  transparent background. Rust is a mark (the short rule under the page
  title, focus), not a panel.
- The header is cream with charcoal text. The logo stays the cream-tile
  mark (`docs/overrides/partials/logo.html` sets its `alt` to the site name).
  A control labeled Website (`docs/overrides/partials/header.html`, URL in
  `mkdocs.yml` `extra.website`) links to https://softwaredecisionfabric.tech/.
  It is in the header bar at desktop and phone widths. On wide screens it sits
  beside the GitHub source button. Material hides `.md-header__source` below
  60em and shows that GitHub button in the drawer.
- The content action at the top right of the page is a view link, not an
  edit link (`mkdocs.yml` `content.action.view`, `edit_uri: blob/main/docs/`).
  It uses the same GitHub mark as the header (`fontawesome/brands/github`).
  Its accessible name is "View on GitHub". Resting color is
  `--sdf-color-text-muted`; hover and keyboard focus use
  `--sdf-color-accent-text`, with a `--sdf-color-focus` outline. The mark
  stays `--sdf-space-6` scaled by `--docs-rem` (24px). The link box is
  `(--sdf-space-10 + --sdf-space-1)` scaled the same way (44px), and a
  negative margin gives that box back so the title line does not move.
  A docs page
  opens `https://github.com/paulpham157/sdf/blob/main/docs/<path>.md`. Skill
  pages open `blob/main/skills/…`. The generated scripts index has no source
  link: that page is not a file in the repository. Material 9.8.2's stock
  view partial rewrites `blob` to `raw`, so
  `docs/overrides/partials/actions.html` keeps the blob URL.
- The footer is one row on wide screens
  (`docs/overrides/partials/footer.html`, layout in `extra.css`): Previous,
  then a centered meta block, then Next. The block groups the license and
  site link, the Material credit, and the GitHub icon, and those lines are
  centered inside it. Equal side columns keep that block on the page center
  when a page has no Previous or no Next. Below 60em the same three slots
  stack, and the meta stays centered. Previous and Next are in a footer
  navigation landmark. At 30em the row keeps end and bottom padding so Next
  stays clear of the back-to-top control. Footer fill is
  `--sdf-color-surface-sunken`. Footer links use `--sdf-color-link`, and
  focus uses `--sdf-color-focus`.

Material sets `html { font-size: 125% }`, so `1rem` is 20px inside the theme.
Token `rem` sizes assume 16px. `extra.css` multiplies those lengths by
`--docs-rem` (`0.8`) so body copy is `--sdf-font-size-base` (16px) at
`--sdf-line-height-relaxed` (1.65). The typeset column is `72ch` (inside the
65–75ch range). Headings use Fraunces, body and nav use Source Sans 3, and
code uses IBM Plex Mono, from `docs/fonts/`. Code blocks use
`--sdf-color-surface-sunken` and `--sdf-radius-md`. Syntax colors are
semantic tokens that pass 4.5:1 on that surface (Material's default hues do
not): keywords `--sdf-color-accent-text`, strings `--sdf-color-success`,
numbers `--sdf-color-error`, constants `--sdf-color-info`, functions
`--sdf-graph-refinement`, comments and punctuation `--sdf-color-text-muted`.
Tables use the default table tokens (padding, font size, line height).
Inline code in a table wraps after a hyphen or a `/`, not in the middle of
a token. The File/Role table is the only one with a 13.5rem minimum on the
first column, so those paths stay on one line. Other first columns size to
the text. A wide table still scrolls sideways.
Admonitions use the state colors and their soft fills.

The homepage "How it works" diagram carries the Mermaid `themeVariables`
block from the GitHub section above, for GitHub's renderer. On the built
site, Material renders that fence itself: its script loads Mermaid 11 from
unpkg and injects `themeCSS` into a closed shadow root, which overrides the
diagram's own fills. `extra.css` sets the variables that stylesheet reads so
the picture matches the block: node fill `--sdf-color-surface-raised`, node
stroke and title `--sdf-color-text`, edges `--sdf-color-charcoal-400` (the
documented `lineColor`), cluster fill `--sdf-color-surface-sunken` (Material
uses `--md-default-fg-color--lightest` for that rect), cluster stroke
`--sdf-color-border-strong`, edge-label background `--sdf-color-bg`. Labels
use Source Sans 3. Self-hosting that Mermaid script is open: the URL is
inside Material's bundle.

Layout stays Material's: a left navigation, a right table of contents, and
the typeset column between them.

### Landing (`sdf-landingpage`): airy, message-led

- Density: generous. `space-16` to `space-24` between sections, the `display`
  size for the hero, measures around 36rem for copy. Radius `none` (the
  current square, stamped look) and `shadow-stamp` for featured blocks.
- Rust is the stamp: short rules, kickers, the mark, the one primary call to
  action per view, and `accent-on-inverse` on charcoal bands. Buttons with
  text use the `accent-text` or charcoal fill, never cream on brand rust.
- Tone: message-led and evidence-first. Plain claims the product can back;
  no testimonials, logos or result metrics that do not exist (the landing
  copy checks already enforce this). Mocks are labeled as mocks.

## How each surface consumes the tokens

| Surface | How | Check |
| --- | --- | --- |
| UI | A byte-identical copy at `internal/ui/assets/tokens.css`, served by the existing handler and linked before `app.css`. `app.css` uses the `--sdf-` variables. `//go:embed` cannot reach `../../design`, so a copy is needed. | A Go test compares the two files byte for byte (added with the restyle, not in this PR). `tokens.css` holds no URLs, `@import` or `url()`, so the UI's no-URL rule and CSP hold. |
| Docs (GitHub) | Not as CSS. The palette is applied by hand to images and the Mermaid theme block above. | Review. |
| Docs site | `mkdocs_hooks.py` copies `design/tokens.css` to `docs/stylesheets/tokens.css` at build time and checks the bytes match. `extra.css` maps Material onto the semantic tokens and prepends the self-hosted webfonts. | `make docs` (`mkdocs build --strict`). The hook fails if the copy is not byte-identical. `python3 design/tokens.py --check` still checks the generator output. |
| Landing | Vendor a byte-identical copy (recommended path `src/styles/tokens.css`), imported first in `global.css`. Its own variables become aliases (`--paper: var(--sdf-color-bg)`), and webfonts are prepended as above. | A small Node script (for example `npm run check:tokens`) compares the vendored file's SHA-256 with a recorded `{commit, sha256}` from this repo, offline, in the existing build workflow. A separate scheduled job, or a manual run, fetches `design/tokens.css` at `main` and reports drift. |

Why vendoring for the landing page:

- It is an Astro 7 static site with plain global CSS and no token tooling.
  A copied CSS file needs no new dependency.
- The deploy builds on GitHub Actions and Cloudflare Pages with
  `persist-credentials: false`. A git submodule adds checkout steps there and
  makes every bump a two-repo change anyway.
- A fetch at build time makes builds depend on the network and on whatever
  `main` holds at that moment, which breaks reproducible deploys.
- The landing CSP is `style-src 'self'`, so the file must be served from the
  site itself, which a vendored file is.

Landing variable mapping:

| Landing today | Token |
| --- | --- |
| `--paper` `#f6f1e8` | `--sdf-color-bg` |
| `--paper-2` `#ebe3d6` | `--sdf-color-surface-sunken` |
| `--paper-3` `#fbf7f1` | `--sdf-color-surface-raised` |
| `--ink` `#2f3640` | `--sdf-color-text` |
| `--ink-soft` `#4e5864` | `--sdf-color-text-muted` |
| `--rule` `#d9cfc0` | `--sdf-color-border` |
| `--stamp` `#c65a2e` | `--sdf-color-accent` |
| `--stamp-deep` `#8e3e1c` | `--sdf-color-accent-text` |
| `--pass` `#145c38` | `--sdf-color-success` |
| `--anchor` `#f3e4d8` | `--sdf-color-accent-soft` |
| `#f3c7b6` (light kicker, focus on charcoal) | `--sdf-color-accent-on-inverse`, `--sdf-color-focus-on-inverse` |
| `#e8e2d6` (light lede) | `--sdf-color-text-inverse-muted` (`#D9CFC0`) or `--sdf-color-text-inverse` |
| `--shadow` | `--sdf-shadow-stamp` |
| `#f4fff8`, `#4e5348` | To review with Frontend (verdict text, textarea text). |

## Changing a token

1. Edit `design/tokens.json`.
2. Run `python3 design/tokens.py`, then `--check`. Fix any failing pairing.
3. Update this spec if a meaning changes.
4. Copy `design/tokens.css` to `internal/ui/assets/tokens.css` (once that copy
   exists) and to the landing repo, and update its recorded commit and hash.
   The docs site does not need a committed copy: the next `make docs` copies
   the file itself.

## Open questions

- Social preview type: it uses Libre Baskerville and Source Sans, while the
  landing page ships Fraunces and Source Sans 3. Align the next export of the
  preview on Fraunces, or move the landing page to Libre Baskerville?
- Landing body size: keep 1.0625rem (17px) as a landing-only value, or move to
  `lg` (18px)?
- Generator language: `design/tokens.py` is Python 3 standard library. A Go
  `go run` tool would add a package to the module; a Node script would fit the
  landing repo better. Python is the smallest choice for this repo.
- Mermaid in the README: confirm GitHub keeps honoring the `init` directive
  before the docs PR relies on it.
